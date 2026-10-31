package probe

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// ffprobe output for a typical dual-audio episode: Italian dub as default
// track 1, English original as track 2, plus subtitles.
const episodeProbe = `{
	"streams": [
		{"index": 0, "codec_type": "video", "codec_name": "hevc"},
		{"index": 1, "codec_type": "audio", "codec_name": "eac3",
			"tags": {"language": "ita", "title": "Italian"},
			"disposition": {"default": 1, "dub": 1, "original": 0}},
		{"index": 2, "codec_type": "audio", "codec_name": "eac3",
			"tags": {"language": "eng", "title": "English (US)"},
			"disposition": {"default": 0, "original": 1}},
		{"index": 3, "codec_type": "subtitle", "codec_name": "subrip",
			"tags": {"language": "ita"},
			"disposition": {"default": 1}},
		{"index": 4, "codec_type": "subtitle", "codec_name": "subrip",
			"tags": {"language": "eng"},
			"disposition": {"default": 0}}
	],
	"format": {"tags": {}}
}`

// Parse assigns 1-based HandBrake ordinals per stream type, skipping video,
// and reads language/title/disposition metadata.
func TestParse(t *testing.T) {
	f, err := Parse([]byte(episodeProbe))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Tracks) != 4 {
		t.Fatalf("expected 4 tracks, got %d", len(f.Tracks))
	}

	audio := f.TracksOfType("audio")
	if len(audio) != 2 || audio[0].Num != 1 || audio[1].Num != 2 {
		t.Fatalf("expected audio nums [1 2], got %v", audio)
	}
	if audio[0].Language != "ita" || !audio[0].Default || !audio[0].Dub || audio[0].Original {
		t.Errorf("unexpected track 1: %+v", audio[0])
	}
	if audio[1].Language != "eng" || !audio[1].Original || audio[1].Default {
		t.Errorf("unexpected track 2: %+v", audio[1])
	}

	subs := f.TracksOfType("subtitle")
	if len(subs) != 2 || subs[0].Num != 1 || subs[1].Num != 2 {
		t.Fatalf("expected subtitle nums [1 2], got %v", subs)
	}
}

// Parse rejects malformed JSON.
func TestParseInvalid(t *testing.T) {
	if _, err := Parse([]byte("{nope")); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// OriginalLanguage returns the language of the stream flagged "original".
func TestOriginalLanguageFromDisposition(t *testing.T) {
	f, err := Parse([]byte(episodeProbe))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := f.OriginalLanguage(); got != "eng" {
		t.Errorf("expected eng, got %q", got)
	}
	if got := f.DefaultRule("audio"); got != "original" {
		t.Errorf("expected default rule original, got %q", got)
	}
}

// OriginalLanguage falls back to a container-level ORIGINAL_LANGUAGE tag when
// no stream carries the original disposition.
func TestOriginalLanguageFromFormatTag(t *testing.T) {
	f, err := Parse([]byte(`{
		"streams": [
			{"index": 1, "codec_type": "audio", "tags": {"language": "ita"}, "disposition": {"default": 1}},
			{"index": 2, "codec_type": "audio", "tags": {"language": "en"}, "disposition": {}}
		],
		"format": {"tags": {"ORIGINAL_LANGUAGE": "en"}}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := f.OriginalLanguage(); got != "eng" {
		t.Errorf("expected eng (normalized from tag), got %q", got)
	}
}

// OriginalLanguage is empty when neither a disposition nor a tag exists.
func TestOriginalLanguageUnknown(t *testing.T) {
	f, err := Parse([]byte(`{
		"streams": [
			{"index": 1, "codec_type": "audio", "tags": {"language": "ita"}, "disposition": {"default": 1}}
		],
		"format": {"tags": {}}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := f.OriginalLanguage(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := f.DefaultRule("audio"); got != "all" {
		t.Errorf("expected default rule all, got %q", got)
	}
}

// NormalizeLanguage maps ISO 639-1 to 639-2 and lowercases; unknown and
// already-639-2 codes pass through.
func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"en": "eng", "EN": "eng", "it": "ita", "eng": "eng", "und": "und", "": "", "pt-BR": "pt-br",
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Errorf("NormalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

// Resolve "original" keeps the tracks matching the original language (here
// eng), including untagged non-dub tracks, and drops the dub.
func TestResolveOriginal(t *testing.T) {
	f, err := Parse([]byte(episodeProbe))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	nums, ok := Resolve("original", f, "audio")
	if !ok || !reflect.DeepEqual(nums, []int{2}) {
		t.Errorf("expected [2] ok, got %v ok=%v", nums, ok)
	}
}

// Resolve "original" includes und-language tracks that are not flagged dub.
func TestResolveOriginalIncludesUnd(t *testing.T) {
	f, err := Parse([]byte(`{
		"streams": [
			{"index": 1, "codec_type": "audio", "tags": {"language": "eng"}, "disposition": {"original": 1}},
			{"index": 2, "codec_type": "audio", "tags": {}, "disposition": {}},
			{"index": 3, "codec_type": "audio", "tags": {"language": "ita"}, "disposition": {"dub": 1}}
		],
		"format": {"tags": {}}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	nums, ok := Resolve("original", f, "audio")
	if !ok || !reflect.DeepEqual(nums, []int{1, 2}) {
		t.Errorf("expected [1 2] ok, got %v ok=%v", nums, ok)
	}
}

// Resolve handles "all", "none", "lang:", and "tracks:" rules, and reports
// failure for unknown or non-matching rules.
func TestResolveRules(t *testing.T) {
	f, err := Parse([]byte(episodeProbe))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if nums, ok := Resolve("all", f, "audio"); !ok || !reflect.DeepEqual(nums, []int{1, 2}) {
		t.Errorf("all: got %v ok=%v", nums, ok)
	}
	if nums, ok := Resolve("none", f, "audio"); !ok || len(nums) != 0 {
		t.Errorf("none: got %v ok=%v", nums, ok)
	}
	if nums, ok := Resolve("lang:ita", f, "audio"); !ok || !reflect.DeepEqual(nums, []int{1}) {
		t.Errorf("lang:ita: got %v ok=%v", nums, ok)
	}
	// ISO 639-1 in the rule matches 639-2 tags
	if nums, ok := Resolve("lang:en", f, "subtitle"); !ok || !reflect.DeepEqual(nums, []int{2}) {
		t.Errorf("lang:en: got %v ok=%v", nums, ok)
	}
	if nums, ok := Resolve("tracks:2", f, "audio"); !ok || !reflect.DeepEqual(nums, []int{2}) {
		t.Errorf("tracks:2: got %v ok=%v", nums, ok)
	}
	// out-of-range track numbers are dropped
	if nums, ok := Resolve("tracks:9", f, "audio"); ok || nums != nil {
		t.Errorf("tracks:9: expected not ok, got %v ok=%v", nums, ok)
	}
	// unknown rule
	if _, ok := Resolve("bogus", f, "audio"); ok {
		t.Errorf("bogus rule: expected not ok")
	}
	// language with no match
	if _, ok := Resolve("lang:deu", f, "audio"); ok {
		t.Errorf("lang:deu: expected not ok")
	}
}

// Marshal/Unmarshal round-trips a File through JSON storage.
func TestMarshalRoundTrip(t *testing.T) {
	f, err := Parse([]byte(episodeProbe))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, err := f.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := Unmarshal(&s)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(f.Tracks, back.Tracks) {
		t.Errorf("round trip mismatch:\n%v\n%v", f.Tracks, back.Tracks)
	}

	// nil input yields nil File without error
	got, err := Unmarshal(nil)
	if err != nil || got != nil {
		t.Errorf("Unmarshal(nil): expected nil, nil; got %v, %v", got, err)
	}
}

// Probe against a real file: ffmpeg synthesizes a tiny MKV with an Italian
// dub as default track 1 and an English original as track 2, mimicking the
// release layout. The test is skipped when ffmpeg/ffprobe are not installed.
func TestProbeRealFile(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}

	mkv := filepath.Join(t.TempDir(), "test.mkv")
	cmd := exec.Command(ffmpeg,
		"-v", "error",
		"-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=0.1",
		"-map", "2:v", "-map", "0:a", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast",
		"-c:a", "aac",
		"-metadata:s:a:0", "language=ita", "-metadata:s:a:0", "title=Italian",
		"-disposition:a:0", "default+dub",
		"-metadata:s:a:1", "language=eng", "-metadata:s:a:1", "title=English (US)",
		"-disposition:a:1", "original",
		"-t", "0.2",
		mkv,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}

	f, err := Probe(context.Background(), ffprobe, mkv)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	audio := f.TracksOfType("audio")
	if len(audio) != 2 {
		t.Fatalf("expected 2 audio tracks, got %v", audio)
	}
	if audio[0].Num != 1 || audio[0].Language != "ita" || !audio[0].Default || !audio[0].Dub {
		t.Errorf("unexpected track 1: %+v", audio[0])
	}
	if audio[1].Num != 2 || audio[1].Language != "eng" || !audio[1].Original {
		t.Errorf("unexpected track 2: %+v", audio[1])
	}
	if got := f.OriginalLanguage(); got != "eng" {
		t.Errorf("expected original language eng, got %q", got)
	}
	nums, ok := Resolve("original", f, "audio")
	if !ok || !reflect.DeepEqual(nums, []int{2}) {
		t.Errorf("expected original rule to select [2], got %v ok=%v", nums, ok)
	}
}
