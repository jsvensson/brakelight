package server

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/log"

	"github.com/jsvensson/brakelight/internal/config"
	"github.com/jsvensson/brakelight/internal/db"
	"github.com/jsvensson/brakelight/internal/probe"
	"github.com/jsvensson/brakelight/internal/worker"
)

//go:embed templates/*.html templates/*.js
var assets embed.FS

// Server is the HTTP server for the web UI.
type Server struct {
	db       *db.DB
	config   *config.Service
	progress *worker.Progress
	srv      *http.Server
}

// New creates a new Server.
func New(database *db.DB, cfg *config.Service, progress *worker.Progress) *Server {
	s := &Server{db: database, config: cfg, progress: progress}

	fsys, _ := fs.Sub(assets, "templates")

	mux := http.NewServeMux()
	mux.Handle("GET /htmx.min.js", http.FileServerFS(fsys))
	mux.Handle("GET /sse.js", http.FileServerFS(fsys))
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /queue", s.handleQueueFragment)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("POST /queue/", s.handleQueueAction)
	mux.HandleFunc("GET /queue/{id}/tracks", s.handleTracksForm)
	mux.HandleFunc("POST /queue/{id}/tracks", s.handleTracksSave)
	mux.HandleFunc("POST /queue/bulk-tracks", s.handleBulkTracks)
	mux.HandleFunc("POST /history/", s.handleHistoryAction)
	mux.HandleFunc("GET /history/", s.handleHistoryLog)
	mux.HandleFunc("POST /service/encoding/toggle", s.handleToggleEncoding)
	mux.HandleFunc("POST /service/scanning/toggle", s.handleToggleScanning)
	mux.HandleFunc("POST /history/clear", s.handleClearHistory)

	s.srv = &http.Server{
		Addr:    cfg.Config.ListenAddr,
		Handler: mux,
	}

	return s
}

// Start runs the HTTP server.
func (s *Server) Start() error {
	log.Info("HTTP server listening", "addr", s.srv.Addr)
	return s.srv.ListenAndServe()
}

// Stop shuts down the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	tmpl, err := parseTemplates()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := s.dashboardData()
	if err := tmpl.Execute(w, data); err != nil {
		log.Error("Template error", "err", err)
	}
}

func (s *Server) handleQueueFragment(w http.ResponseWriter, r *http.Request) {
	data := s.dashboardData()
	renderTemplate(w, "templates/queue.html", data)
}

// handleEvents streams live encoding progress as server-sent events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	send := func() bool {
		var buf strings.Builder
		pending, _ := s.db.ListPendingJobs()
		if err := renderProgressFragment(&buf, progressView{Snapshot: s.progress.Snapshot(), Pending: len(pending)}); err != nil {
			log.Error("Progress fragment error", "err", err)
			return false
		}
		fmt.Fprint(w, "event: progress\n")
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			fmt.Fprintf(w, "data: %s\n", line)
		}
		fmt.Fprint(w, "\n")
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

// progressView is the data passed to the progress fragment template.
type progressView struct {
	worker.Snapshot
	Pending int
}

func renderProgressFragment(w io.Writer, view progressView) error {
	tmpl, err := parseTemplates()
	if err != nil {
		return err
	}
	return tmpl.ExecuteTemplate(w, "progress.html", view)
}

func (s *Server) handleQueueAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/queue/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	action := parts[1]
	switch action {
	case "move":
		s.handleMove(w, r, id)
	case "retry":
		s.handleRetry(w, r, id)
	case "cancel":
		s.handleCancel(w, r, id)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request, id int64) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	indexStr := r.FormValue("index")
	index, err := strconv.ParseInt(indexStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid index", http.StatusBadRequest)
		return
	}

	if err := s.db.MoveJobToPosition(id, index); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.handleQueueFragment(w, r)
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request, id int64) {
	pos, err := s.db.NextPosition()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.db.ResetJobToPending(id, pos); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.handleQueueFragment(w, r)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request, id int64) {
	if err := s.db.CancelJob(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.handleQueueFragment(w, r)
}

// tracksView is the data passed to the track picker fragment.
type tracksView struct {
	Job          *db.Job
	OriginalLang string
	AudioRule    string
	SubRule      string
	AudioTracks  []probe.Track
	SubTracks    []probe.Track
	AudioChecks  map[int]bool
	SubChecks    map[int]bool
}

// handleTracksForm renders the track picker for a pending review job. The
// picker is pre-filled with the stored selection, or with the "original
// language" suggestion when nothing is stored yet.
func (s *Server) handleTracksForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	job, err := s.db.GetJob(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if job == nil || !job.Review || job.Status != "pending" {
		http.NotFound(w, r)
		return
	}

	tracks, _ := probe.Unmarshal(job.TracksJSON)

	audioRule := tracks.DefaultRule("audio")
	if job.AudioSelection != nil {
		audioRule = *job.AudioSelection
	}
	subRule := tracks.DefaultRule("subtitle")
	if job.SubtitleSelection != nil {
		subRule = *job.SubtitleSelection
	}

	renderTemplate(w, "templates/tracks.html", tracksView{
		Job:          job,
		OriginalLang: tracks.OriginalLanguage(),
		AudioRule:    ruleKind(audioRule),
		SubRule:      ruleKind(subRule),
		AudioTracks:  tracks.TracksOfType("audio"),
		SubTracks:    tracks.TracksOfType("subtitle"),
		AudioChecks:  checkedNums(audioRule, tracks, "audio"),
		SubChecks:    checkedNums(subRule, tracks, "subtitle"),
	})
}

// handleTracksSave stores the chosen audio/subtitle rules for one pending
// review job and re-renders the queue.
func (s *Server) handleTracksSave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	job, err := s.db.GetJob(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if job == nil || !job.Review || job.Status != "pending" {
		http.NotFound(w, r)
		return
	}

	audio := ruleFromForm(r.Form, "audio", "audio_tracks")
	subtitles := ruleFromForm(r.Form, "subtitle", "subtitle_tracks")
	if err := s.db.SetJobTrackSelection(id, audio, subtitles); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Info("Track selection saved", "id", id, "audio", audio, "subtitles", subtitles)

	s.handleQueueFragment(w, r)
}

// handleBulkTracks applies one audio/subtitle rule to many pending review
// jobs at once, then re-renders the queue.
func (s *Server) handleBulkTracks(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	audio := r.FormValue("bulk_audio")
	subtitles := r.FormValue("bulk_subtitles")
	if len(audio) == 0 {
		audio = "all"
	}
	if len(subtitles) == 0 {
		subtitles = "all"
	}

	applied := 0
	for _, idStr := range r.Form["ids"] {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			continue
		}
		job, err := s.db.GetJob(id)
		if err != nil || job == nil || !job.Review || job.Status != "pending" {
			continue
		}
		if err := s.db.SetJobTrackSelection(id, audio, subtitles); err != nil {
			log.Error("Bulk track selection failed for job", "id", id, "err", err)
			continue
		}
		applied++
	}
	log.Info("Bulk track selection applied", "count", applied, "audio", audio, "subtitles", subtitles)

	s.handleQueueFragment(w, r)
}

// ruleKind reduces a stored rule to the picker's select value:
// "all", "none", "original", or "tracks".
func ruleKind(rule string) string {
	switch {
	case strings.HasPrefix(rule, "tracks:"), strings.HasPrefix(rule, "lang:"):
		return "tracks"
	default:
		return rule
	}
}

// ruleFromForm builds a stored rule from the picker form: the select value,
// or "tracks:n,m" from the checked track boxes when "tracks" is selected
// ("none" when no box is checked).
func ruleFromForm(form url.Values, selectName, tracksName string) string {
	rule := form.Get(selectName)
	if rule != "tracks" {
		return rule
	}
	var nums []string
	for _, v := range form[tracksName] {
		if _, err := strconv.Atoi(v); err == nil {
			nums = append(nums, v)
		}
	}
	if len(nums) == 0 {
		return "none"
	}
	return "tracks:" + strings.Join(nums, ",")
}

// checkedNums returns which track boxes the picker should pre-check for a
// stored or suggested rule.
func checkedNums(rule string, f *probe.File, typ string) map[int]bool {
	checks := map[int]bool{}
	for _, t := range f.TracksOfType(typ) {
		checks[t.Num] = true
	}
	switch ruleKind(rule) {
	case "all":
		// all checked
	case "none":
		for k := range checks {
			delete(checks, k)
		}
	default:
		nums, ok := probe.Resolve(rule, f, typ)
		if !ok {
			return checks
		}
		checks = map[int]bool{}
		for _, n := range nums {
			checks[n] = true
		}
	}
	return checks
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	if err := s.db.ClearHistory(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.handleQueueFragment(w, r)
}

// handleHistoryLog serves the stored HandBrake CLI output for a history job.
func (s *Server) handleHistoryLog(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/history/"), "/log")
	parts := strings.Split(path, "/")
	if len(parts) != 1 || !strings.HasSuffix(r.URL.Path, "/log") {
		http.NotFound(w, r)
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	jobLog, err := s.db.GetJobLog(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if jobLog == nil {
		http.NotFound(w, r)
		return
	}

	renderTemplate(w, "templates/log.html", logView{Filename: jobLog.Filepath, Log: jobLog.LogOutput})
}

// logView is the data passed to the log fragment template.
type logView struct {
	Filename string
	Log      string
}

func (s *Server) handleHistoryAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/history/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	switch parts[1] {
	case "forget":
		if err := s.db.DeleteJob(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.handleQueueFragment(w, r)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

func (s *Server) handleToggleEncoding(w http.ResponseWriter, r *http.Request) {
	s.handleToggleService(w, r, "Encoding", s.db.IsEncodingActive, s.db.SetEncodingActive)
}

func (s *Server) handleToggleScanning(w http.ResponseWriter, r *http.Request) {
	s.handleToggleService(w, r, "Scanning", s.db.IsScanningActive, s.db.SetScanningActive)
}

func (s *Server) handleToggleService(w http.ResponseWriter, r *http.Request, name string, isActive func() (bool, error), setActive func(bool) error) {
	active, err := isActive()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := setActive(!active); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Info("Service toggled", "service", name, "state", map[bool]string{true: "active", false: "inactive"}[!active])
	s.handleQueueFragment(w, r)
}

type dashboard struct {
	EncodingActive   bool
	ScanningActive   bool
	ReencodeRisk     int
	HasReviewPending bool
	Pending          []*db.Job
	Processing       []*db.Job
	History          []*db.Job
}

func (s *Server) dashboardData() dashboard {
	pending, _ := s.db.ListPendingJobs()
	processing, _ := s.db.ListProcessingJobs()
	history, _ := s.db.ListRecentHistory(50)
	encodingActive, _ := s.db.IsEncodingActive()
	scanningActive, _ := s.db.IsScanningActive()
	hasReview := false
	for _, j := range pending {
		if j.Review {
			hasReview = true
			break
		}
	}
	return dashboard{EncodingActive: encodingActive, ScanningActive: scanningActive, ReencodeRisk: s.reencodeRisk(), HasReviewPending: hasReview, Pending: pending, Processing: processing, History: history}
}

// reencodeRisk counts completed jobs whose source file still exists in a
// watch directory while the output file is gone. Clearing history would
// cause these files to be re-encoded on the next scan.
func (s *Server) reencodeRisk() int {
	jobs, err := s.db.ListCompletedJobs()
	if err != nil {
		return 0
	}

	risk := 0
	for _, job := range jobs {
		if !s.inWatchDir(job.Filepath) {
			continue
		}
		if _, err := os.Stat(job.Filepath); err != nil {
			continue
		}
		if _, err := os.Stat(job.OutputPath); err == nil {
			continue
		}
		risk++
	}
	return risk
}

func (s *Server) inWatchDir(path string) bool {
	for _, w := range s.config.Watch {
		if strings.HasPrefix(path, w.Path+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	tmpl, err := parseTemplates()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := tmpl.ExecuteTemplate(w, filepath.Base(name), data); err != nil {
		log.Error("Template error", "err", err)
	}
}

func parseTemplates() (*template.Template, error) {
	funcMap := template.FuncMap{
		"formatTime":      formatTime,
		"formatDuration":  formatDuration,
		"sizeChange":      sizeChange,
		"sizeChangeClass": sizeChangeClass,
		"base":            filepath.Base,
		"add":             func(a, b int) int { return a + b },
		"sub":             func(a, b int) int { return a - b },
	}
	return template.New("index.html").Funcs(funcMap).ParseFS(assets, "templates/index.html", "templates/queue.html", "templates/progress.html", "templates/log.html", "templates/tracks.html")
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// formatDuration renders the elapsed time between two timestamps, e.g. "1h23m".
func formatDuration(start, end *time.Time) string {
	if start == nil || end == nil {
		return "-"
	}
	d := end.Sub(*start).Round(time.Second)
	if d < 0 {
		return "-"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// sizeChange renders the file size change as a percentage, e.g. "-15%" or
// "+20%". It returns "-" when either size is unknown.
func sizeChange(job *db.Job) string {
	if job.SourceSize == nil || job.OutputSize == nil || *job.SourceSize == 0 {
		return "-"
	}
	pct := float64(*job.OutputSize-*job.SourceSize) / float64(*job.SourceSize) * 100
	return fmt.Sprintf("%+.0f%%", pct)
}

// sizeChangeClass returns the CSS class for the size change direction.
func sizeChangeClass(job *db.Job) string {
	if job.SourceSize == nil || job.OutputSize == nil {
		return ""
	}
	if *job.OutputSize > *job.SourceSize {
		return "size-up"
	}
	return "size-down"
}
