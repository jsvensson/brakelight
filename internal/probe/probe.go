// Package probe inspects media files with ffprobe and resolves track
// selection rules against the probed tracks.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// probeTimeout caps a single ffprobe run.
const probeTimeout = 30 * time.Second

// Track is a single audio or subtitle stream. Num is the 1-based ordinal
// within streams of the same type, matching HandBrakeCLI's -a/-s numbering.
type Track struct {
	Num      int    `json:"num"`
	Type     string `json:"type"` // "audio" or "subtitle"
	Codec    string `json:"codec,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Original bool   `json:"original,omitempty"`
	Dub      bool   `json:"dub,omitempty"`
	Comment  bool   `json:"comment,omitempty"`
}

// File is the probed track listing of a media file.
type File struct {
	Tracks      []Track `json:"tracks"`
	OriginalTag string  `json:"original_tag,omitempty"`
}

// TracksOfType returns the tracks of the given type ("audio" or "subtitle")
// in stream order.
func (f *File) TracksOfType(typ string) []Track {
	if f == nil {
		return nil
	}
	var out []Track
	for _, t := range f.Tracks {
		if t.Type == typ {
			out = append(out, t)
		}
	}
	return out
}

// Probe runs ffprobe on input and returns the parsed track listing.
func Probe(ctx context.Context, ffprobePath, input string) (*File, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-show_streams", "-show_format",
		"-of", "json",
		input,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	return Parse(out)
}

type disposition struct {
	Default  int `json:"default"`
	Original int `json:"original"`
	Dub      int `json:"dub"`
	Comment  int `json:"comment"`
}

type rawStream struct {
	Index       int               `json:"index"`
	CodecType   string            `json:"codec_type"`
	CodecName   string            `json:"codec_name"`
	Tags        map[string]string `json:"tags"`
	Disposition disposition       `json:"disposition"`
}

type rawProbe struct {
	Streams []rawStream `json:"streams"`
	Format  struct {
		Tags map[string]string `json:"tags"`
	} `json:"format"`
}

// Parse decodes ffprobe JSON output into a File. Audio and subtitle streams
// get a 1-based Num matching HandBrakeCLI's track numbering.
func Parse(data []byte) (*File, error) {
	var raw rawProbe
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	f := &File{}
	seen := map[string]int{}
	for _, s := range raw.Streams {
		if s.CodecType != "audio" && s.CodecType != "subtitle" {
			continue
		}
		seen[s.CodecType]++
		f.Tracks = append(f.Tracks, Track{
			Num:      seen[s.CodecType],
			Type:     s.CodecType,
			Codec:    s.CodecName,
			Language: s.Tags["language"],
			Title:    s.Tags["title"],
			Default:  s.Disposition.Default == 1,
			Original: s.Disposition.Original == 1,
			Dub:      s.Disposition.Dub == 1,
			Comment:  s.Disposition.Comment == 1,
		})
	}

	// Some containers record the original language only as a format tag.
	for k, v := range raw.Format.Tags {
		if strings.EqualFold(k, "original_language") && len(v) > 0 {
			f.OriginalTag = NormalizeLanguage(v)
			break
		}
	}
	return f, nil
}

// Marshal serializes the File for storage in the jobs table.
func (f *File) Marshal() (string, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Unmarshal parses a stored track listing (as produced by Marshal). A nil or
// empty input yields a nil File and no error, so callers can treat missing
// probe data uniformly.
func Unmarshal(s *string) (*File, error) {
	if s == nil || len(*s) == 0 {
		return nil, nil
	}
	var f File
	if err := json.Unmarshal([]byte(*s), &f); err != nil {
		return nil, fmt.Errorf("parse stored tracks: %w", err)
	}
	return &f, nil
}

// iso6391to6392 maps common ISO 639-1 codes to the ISO 639-2 codes HandBrake
// uses. Codes not in the map pass through unchanged.
var iso6391to6392 = map[string]string{
	"ar": "ara", "cs": "ces", "da": "dan", "de": "deu", "el": "ell",
	"en": "eng", "es": "spa", "fi": "fin", "fr": "fre", "he": "heb",
	"hi": "hin", "hu": "hun", "id": "ind", "it": "ita", "ja": "jpn",
	"ko": "kor", "nl": "dut", "no": "nor", "pl": "pol", "pt": "por",
	"ru": "rus", "sv": "swe", "th": "tha", "tr": "tur", "uk": "ukr",
	"vi": "vie", "zh": "zho",
}

// NormalizeLanguage lowercases a language tag, drops any region subtag
// ("pt-BR" -> "pt"), and converts ISO 639-1 codes to ISO 639-2 so that
// MP4-style "en" and MKV-style "eng" compare equal.
func NormalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	lang = strings.ReplaceAll(lang, "_", "-")
	// Strip the region/script subtag when a 2- or 3-letter language code
	// precedes it ("pt-BR" -> "pt"); private-use tags like "x-custom" are
	// left intact.
	if i := strings.IndexByte(lang, '-'); i == 2 || i == 3 {
		lang = lang[:i]
	}
	if len(lang) == 2 {
		if mapped, ok := iso6391to6392[lang]; ok {
			return mapped
		}
	}
	return lang
}

// OriginalLanguage determines the file's original audio language: the
// language of an audio stream flagged with the "original" disposition, or a
// container-level ORIGINAL_LANGUAGE tag. Subtitle streams are not
// consulted. It returns "" when neither exists.
func (f *File) OriginalLanguage() string {
	if f == nil {
		return ""
	}
	for _, t := range f.TracksOfType("audio") {
		if t.Original && len(t.Language) > 0 && NormalizeLanguage(t.Language) != "und" {
			return NormalizeLanguage(t.Language)
		}
	}
	if len(f.OriginalTag) > 0 {
		return f.OriginalTag
	}
	return ""
}

// DefaultRule returns the suggested selection rule for a track type:
// "original" when the original language is known, otherwise "all".
func (f *File) DefaultRule(typ string) string {
	if len(f.OriginalLanguage()) > 0 {
		return "original"
	}
	return "all"
}

// Resolve evaluates a selection rule against the probed tracks and returns
// the selected 1-based track numbers of the given type. The second return
// value is false when the rule is unknown or matches nothing, in which case
// the caller should fall back to keeping all tracks.
//
// Rules: "all", "none", "original", "lang:eng,ita", "tracks:1,3".
func Resolve(rule string, f *File, typ string) ([]int, bool) {
	tracks := f.TracksOfType(typ)

	switch {
	case rule == "all":
		return numsOf(tracks), true
	case rule == "none":
		return []int{}, true
	case rule == "original":
		orig := f.OriginalLanguage()
		if len(orig) == 0 {
			return nil, false
		}
		var out []int
		for _, t := range tracks {
			// Commentary tracks never count as "original", including the
			// usual untagged (und) commentary layout.
			if t.Comment {
				continue
			}
			lang := NormalizeLanguage(t.Language)
			isUnd := len(lang) == 0 || lang == "und"
			if lang == orig || t.Original || (isUnd && !t.Dub) {
				out = append(out, t.Num)
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	case strings.HasPrefix(rule, "lang:"):
		want := map[string]bool{}
		for _, l := range strings.Split(strings.TrimPrefix(rule, "lang:"), ",") {
			if len(l) > 0 {
				want[NormalizeLanguage(l)] = true
			}
		}
		var out []int
		for _, t := range tracks {
			if want[NormalizeLanguage(t.Language)] {
				out = append(out, t.Num)
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	case strings.HasPrefix(rule, "tracks:"):
		valid := map[int]bool{}
		for _, t := range tracks {
			valid[t.Num] = true
		}
		var out []int
		for _, part := range strings.Split(strings.TrimPrefix(rule, "tracks:"), ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err == nil && valid[n] {
				out = append(out, n)
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	}
	return nil, false
}

// ValidRule reports whether rule is a well-formed selection rule:
// "all", "none", "original", "lang:eng,ita", or "tracks:1,3".
func ValidRule(rule string) bool {
	switch {
	case rule == "all", rule == "none", rule == "original":
		return true
	case strings.HasPrefix(rule, "lang:"):
		for _, l := range strings.Split(strings.TrimPrefix(rule, "lang:"), ",") {
			if len(strings.TrimSpace(l)) == 0 {
				return false
			}
		}
		return true
	case strings.HasPrefix(rule, "tracks:"):
		for _, p := range strings.Split(strings.TrimPrefix(rule, "tracks:"), ",") {
			if _, err := strconv.Atoi(strings.TrimSpace(p)); err != nil {
				return false
			}
		}
		return true
	}
	return false
}

func numsOf(tracks []Track) []int {
	out := make([]int, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, t.Num)
	}
	return out
}
