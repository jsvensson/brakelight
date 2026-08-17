package server

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jsvensson/brakelight/internal/config"
	"github.com/jsvensson/brakelight/internal/db"
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
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /queue", s.handleQueueFragment)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("POST /queue/", s.handleQueueAction)
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
	log.Printf("HTTP server listening on %s", s.srv.Addr)
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
		log.Printf("Template error: %v", err)
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
			log.Printf("Progress fragment error: %v", err)
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

	filepath, logOutput, found, err := s.db.GetJobLog(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	renderTemplate(w, "templates/log.html", logView{Filename: filepath, Log: logOutput})
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

	log.Printf("%s toggled %s", name, map[bool]string{true: "active", false: "inactive"}[!active])
	s.handleQueueFragment(w, r)
}

type dashboard struct {
	EncodingActive bool
	ScanningActive bool
	ReencodeRisk   int
	Pending        []*db.Job
	Processing     []*db.Job
	History        []*db.Job
}

func (s *Server) dashboardData() dashboard {
	pending, _ := s.db.ListPendingJobs()
	processing, _ := s.db.ListProcessingJobs()
	history, _ := s.db.ListRecentHistory(50)
	encodingActive, _ := s.db.IsEncodingActive()
	scanningActive, _ := s.db.IsScanningActive()
	return dashboard{EncodingActive: encodingActive, ScanningActive: scanningActive, ReencodeRisk: s.reencodeRisk(), Pending: pending, Processing: processing, History: history}
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
		log.Printf("Template error: %v", err)
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
	return template.New("index.html").Funcs(funcMap).ParseFS(assets, "templates/index.html", "templates/queue.html", "templates/progress.html", "templates/log.html")
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

