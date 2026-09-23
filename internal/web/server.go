// Package web is the wizard: a local HTTP server that walks an operator
// through source → read → review → target → apply, and then shuts itself down.
//
// It is a wizard, not a daemon. It binds the loopback address, hands out one
// URL carrying a one-time token, serves one migration, and exits — because the
// process holds the admin credentials of two panels and, between the read and
// the apply, every subscription token in the book. The fewer minutes it is
// listening, and the fewer interfaces it is listening on, the better.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nexora-vpn/nexora-migrate/internal/apply"
	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/source"
	"github.com/nexora-vpn/nexora-migrate/internal/target"
)

//go:embed assets/*
var assets embed.FS

// Config is how the server was asked to run.
type Config struct {
	Listen      string // host:port
	AllowRemote bool
	TLSCert     string
	TLSKey      string
	OpenBrowser bool
}

// Server is one wizard session. There is exactly one migration in flight, which
// is what the whole tool is for, so the state is a single guarded struct rather
// than a session store.
type Server struct {
	cfg   Config
	token string

	mu       sync.Mutex
	bundle   *bundle.Bundle
	client   *target.Client
	pre      target.Preflight
	result   *apply.Result
	running  bool
	cancel   context.CancelFunc
	events   []apply.Event
	watchers map[chan apply.Event]struct{}

	uploads    []string // temporary files this session wrote, deleted on the way out
	uploadName string   // what the operator called the file they picked

	shutdown chan struct{}
	once     sync.Once
}

// New builds the server and its one-time token.
func New(cfg Config) (*Server, error) {
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("--listen must be host:port, e.g. 127.0.0.1:8787")
	}
	if !isLoopback(host) && !cfg.AllowRemote {
		return nil, fmt.Errorf(
			"refusing to listen on %s: this process holds the admin credentials of two panels and every subscription token in the book.\n"+
				"Keep it on 127.0.0.1 and reach it with an SSH tunnel:\n"+
				"    ssh -L 8787:127.0.0.1:8787 you@your-server\n"+
				"If you really mean to expose it, pass --allow-remote together with --tls-cert and --tls-key.", host)
	}
	if !isLoopback(host) && (cfg.TLSCert == "" || cfg.TLSKey == "") {
		return nil, fmt.Errorf("--allow-remote also needs --tls-cert and --tls-key: a plaintext admin console on a public interface is not something this tool will start")
	}
	return &Server{
		cfg:      cfg,
		token:    newToken(),
		watchers: map[chan apply.Event]struct{}{},
		shutdown: make(chan struct{}),
	}, nil
}

// Run serves until the wizard finishes or the context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("could not listen on %s: %w", s.cfg.Listen, err)
	}
	url := s.entryURL(ln.Addr().(*net.TCPAddr))

	fmt.Println()
	fmt.Println("  Nexora migration wizard")
	fmt.Println("  ───────────────────────")
	fmt.Println("  Open this in your browser:")
	fmt.Println()
	fmt.Println("    " + url)
	fmt.Println()
	fmt.Println("  The link carries a one-time key. Nobody without it can reach this page.")
	fmt.Println("  The wizard closes this program when you finish, or press Ctrl+C.")
	fmt.Println()

	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 20 * time.Second,
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-s.shutdown:
			// Give the browser a moment to receive the last response.
			time.Sleep(400 * time.Millisecond)
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if s.cfg.OpenBrowser {
		go openBrowser(url)
	}

	// Whatever happens, the uploaded database does not outlive the wizard.
	defer s.discardUploads()

	if s.cfg.TLSCert != "" {
		err = srv.ServeTLS(ln, s.cfg.TLSCert, s.cfg.TLSKey)
	} else {
		err = srv.Serve(ln)
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) entryURL(addr *net.TCPAddr) string {
	scheme := "http"
	if s.cfg.TLSCert != "" {
		scheme = "https"
	}
	host := addr.IP.String()
	if addr.IP.IsUnspecified() || addr.IP.IsLoopback() {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s://%s:%d/?key=%s", scheme, host, addr.Port, s.token)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.Handle("GET /assets/", http.FileServer(http.FS(assets)))

	mux.HandleFunc("GET /api/sources", s.guard(s.handleSources))
	mux.HandleFunc("POST /api/upload", s.guard(s.handleUpload))
	mux.HandleFunc("POST /api/read", s.guard(s.handleRead))
	mux.HandleFunc("GET /api/tree", s.guard(s.handleTree))
	mux.HandleFunc("POST /api/connect", s.guard(s.handleConnect))
	mux.HandleFunc("POST /api/plan", s.guard(s.handlePlan))
	mux.HandleFunc("POST /api/apply", s.guard(s.handleApply))
	mux.HandleFunc("GET /api/events", s.guard(s.handleEvents))
	mux.HandleFunc("GET /api/result", s.guard(s.handleResult))
	mux.HandleFunc("POST /api/cancel", s.guard(s.handleCancel))
	mux.HandleFunc("POST /api/quit", s.guard(s.handleQuit))
	return mux
}

const cookieName = "nexora_migrate"

// handleIndex exchanges the one-time key in the URL for a session cookie and
// serves the wizard.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if key := r.URL.Query().Get("key"); key != "" {
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.token)) != 1 {
			http.Error(w, "wrong key", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: s.token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
			Secure: s.cfg.TLSCert != "",
		})
		// Drop the key out of the address bar so it does not sit in history.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.authorised(r) {
		http.Error(w, "open the link this program printed — it carries the key", http.StatusForbidden)
		return
	}
	page, err := template.ParseFS(assets, "assets/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = page.Execute(w, nil)
}

func (s *Server) authorised(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1
}

// guard rejects anything without the session cookie, and anything that does not
// carry the wizard's own header — a form posted from another page cannot set a
// custom header, so this is the whole CSRF story for a loopback tool.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorised(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this session is not authorised; reopen the link the program printed"})
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Nexora-Migrate") != "1" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing the wizard's own request header"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, source.List())
}

// maxUpload bounds the database an operator can hand over. The largest panel
// databases seen in the wild are a few hundred megabytes; past this it is not a
// panel database and the disk should not fill up finding that out.
const maxUpload = 2 << 30

// sqliteMagic is the first 16 bytes of every SQLite 3 file. Checking it here
// means an operator who picked the wrong file is told so while they are still
// looking at the file picker, not three screens later.
var sqliteMagic = []byte("SQLite format 3\x00")

// handleUpload takes the database file the operator chose in the browser and
// parks it in a temporary file for the reader.
//
// It exists because the wizard is normally opened on a laptop while the panel's
// database is on that same laptop, downloaded from the old server — asking for
// an absolute path is asking the operator to find out what their browser called
// the Downloads folder. The path field stays for the other case, running the
// wizard on the server itself over an SSH tunnel, where the file never left.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.TrimSpace(r.Header.Get("X-File-Name")))
	if name == "." || name == "/" {
		name = "database.db"
	}

	f, err := os.CreateTemp("", "nexora-migrate-upload-*.db")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not open a temporary file: " + err.Error()})
		return
	}
	path := f.Name()
	fail := func(code int, msg string) {
		f.Close()
		os.Remove(path)
		writeJSON(w, code, map[string]string{"error": msg})
	}

	head := make([]byte, 0, len(sqliteMagic))
	body := http.MaxBytesReader(w, r.Body, maxUpload)
	buf := make([]byte, 256<<10)
	var n int64
	for {
		read, rerr := body.Read(buf)
		if read > 0 {
			if len(head) < cap(head) {
				head = append(head, buf[:min(read, cap(head)-len(head))]...)
			}
			if _, werr := f.Write(buf[:read]); werr != nil {
				fail(http.StatusInternalServerError, "could not write the upload to disk: "+werr.Error())
				return
			}
			n += int64(read)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			fail(http.StatusBadRequest, "the upload stopped early: "+rerr.Error())
			return
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not finish writing the upload: " + err.Error()})
		return
	}
	if n == 0 {
		os.Remove(path)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that file is empty"})
		return
	}
	if !bytes.HasPrefix(head, sqliteMagic) {
		os.Remove(path)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("%s is not a SQLite database. These panels keep everything in one .db file — x-ui.db or s-ui.db — so pick that, not an archive or a dump", name)})
		return
	}

	// One upload at a time: picking a second file replaces the first rather
	// than leaving a copy of a panel database lying around.
	s.mu.Lock()
	old := s.uploads
	s.uploads, s.uploadName = []string{path}, name
	s.mu.Unlock()
	for _, p := range old {
		os.Remove(p)
	}

	writeJSON(w, http.StatusOK, map[string]any{"path": path, "name": name, "size": n})
}

// discardUploads deletes every temporary file this session wrote. An uploaded
// panel database holds every credential that panel issued, so it is deleted the
// moment the wizard stops, however it stopped.
func (s *Server) discardUploads() {
	s.mu.Lock()
	paths := s.uploads
	s.uploads = nil
	s.mu.Unlock()
	for _, p := range paths {
		os.Remove(p)
	}
}

type readRequest struct {
	Source  string         `json:"source"`
	Options source.Options `json:"options"`
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	var req readRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the form"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	b, err := source.Read(ctx, req.Source, req.Options)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	// An operator who picked a file in the browser should see the name they
	// picked, not the temporary path this process parked it at.
	for _, up := range s.uploads {
		if up == req.Options.Path && s.uploadName != "" {
			b.Source.Origin = s.uploadName
		}
	}
	s.bundle = b
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, summary(b))
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	b := s.bundle
	s.mu.Unlock()
	if b == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nothing has been read yet"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": b.Source,
		"notes":  b.Notes,
		"tree":   b.Tree(),
	})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var opt target.Options
	if err := json.NewDecoder(r.Body).Decode(&opt); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the form"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	c, err := target.Connect(ctx, opt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	pre, err := c.Check(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.client, s.pre = c, pre
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"base": c.Base(), "preflight": pre})
}

type planRequest struct {
	Selected []string `json:"selected"`
}

// handlePlan answers "if I press go, what happens" — the expanded selection,
// what it pulled in, and whether the licence has room. It writes nothing.
func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	var req planRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the selection"})
		return
	}
	s.mu.Lock()
	b, pre, connected := s.bundle, s.pre, s.client != nil
	s.mu.Unlock()
	if b == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nothing has been read yet"})
		return
	}

	items, added := apply.Select(b, toSet(req.Selected))
	counts := map[bundle.Kind]int{}
	names := map[bundle.Kind][]string{}
	for _, it := range items {
		counts[it.Kind]++
		names[it.Kind] = append(names[it.Kind], it.Name)
	}

	var warnings []string
	if connected {
		warnings = append(warnings, licenceWarnings(pre, counts)...)
		warnings = append(warnings, collisionWarnings(pre, names)...)
	}
	addedNames := make([]string, 0, len(added))
	for _, id := range added {
		for _, it := range b.Items {
			if it.ID == id {
				addedNames = append(addedNames, string(it.Kind)+" "+it.Name)
				break
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":    len(items),
		"counts":   counts,
		"added":    addedNames,
		"warnings": warnings,
		"nodes":    pre.Nodes,
	})
}

// licenceWarnings compares what is about to be created against the panel's
// remaining licence headroom, so a run that cannot finish is refused before it
// starts rather than halfway through.
func licenceWarnings(pre target.Preflight, counts map[bundle.Kind]int) []string {
	if !pre.LicenceReadable {
		return []string{"this login cannot read the licence (only the main admin can), so the import could still hit a limit partway through"}
	}
	var out []string
	for kind, resource := range map[bundle.Kind]string{
		bundle.KindClient: "user", bundle.KindInbound: "inbound",
		bundle.KindOutbound: "outbound", bundle.KindEndpoint: "endpoint",
		bundle.KindAdmin: "admin",
	} {
		want := counts[kind]
		if want == 0 {
			continue
		}
		room, ok := pre.Headroom(resource)
		if !ok || room < 0 {
			continue
		}
		if int64(want) > room {
			out = append(out, fmt.Sprintf(
				"the licence has room for %d more %s(s) but %d are selected — the run would stop when it fills up",
				room, resource, want))
		}
	}
	return out
}

func collisionWarnings(pre target.Preflight, names map[bundle.Kind][]string) []string {
	var out []string
	for kind, list := range names {
		existing := map[string]bool{}
		for _, n := range pre.Existing[string(kind)] {
			existing[strings.ToLower(n)] = true
		}
		var clash []string
		for _, n := range list {
			if existing[strings.ToLower(n)] {
				clash = append(clash, n)
			}
		}
		if len(clash) > 0 {
			shown := clash
			if len(shown) > 5 {
				shown = shown[:5]
			}
			out = append(out, fmt.Sprintf(
				"%d %s(s) already exist in Nexora with the same name and will be left alone: %s%s",
				len(clash), kind, strings.Join(shown, ", "), more(len(clash)-len(shown))))
		}
	}
	return out
}

func more(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" and %d more", n)
}

type applyRequest struct {
	Selected   []string `json:"selected"`
	PauseNodes *bool    `json:"pauseNodes"`
	// Code is a fresh authenticator code, presented before the run so a
	// two-factor session may create admins.
	Code string `json:"code"`
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the selection"})
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a run is already in progress"})
		return
	}
	b, c := s.bundle, s.client
	if b == nil || c == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read a source and connect to Nexora first"})
		return
	}
	items, _ := apply.Select(b, toSet(req.Selected))
	if len(items) == 0 {
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nothing is selected"})
		return
	}
	if req.Code != "" {
		// Under the lock, so a second apply cannot start meanwhile; the call is
		// one short request.
		cctx, ccancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := c.Confirm(cctx, req.Code)
		ccancel()
		if err != nil {
			s.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running, s.cancel, s.events, s.result = true, cancel, nil, nil
	s.mu.Unlock()

	opt := apply.Options{PauseNodes: true}
	if req.PauseNodes != nil {
		opt.PauseNodes = *req.PauseNodes
	}

	go func() {
		defer cancel()
		res := apply.Run(ctx, c, items, opt, s.broadcast)
		s.mu.Lock()
		s.result, s.running = &res, false
		s.mu.Unlock()
		s.broadcast(apply.Event{Phase: "finished", Status: apply.StatusCreated,
			Done: len(items), Total: len(items)})
	}()

	writeJSON(w, http.StatusOK, map[string]any{"started": true, "total": len(items)})
}

// handleEvents streams the run as it happens.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "this server cannot stream"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan apply.Event, 256)
	s.mu.Lock()
	backlog := append([]apply.Event(nil), s.events...)
	s.watchers[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.watchers, ch)
		s.mu.Unlock()
	}()

	send := func(e apply.Event) bool {
		raw, err := json.Marshal(e)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for _, e := range backlog {
		if !send(e) {
			return
		}
	}
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if !send(e) {
				return
			}
			if e.Phase == "finished" {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) broadcast(e apply.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	for ch := range s.watchers {
		select {
		case ch <- e:
		default: // a slow reader must not stall the run
		}
	}
	s.mu.Unlock()
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	res, running := s.result, s.running
	s.mu.Unlock()
	if res == nil {
		writeJSON(w, http.StatusOK, map[string]any{"running": running})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": running, "result": res})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

// handleQuit is the last step of the wizard: the operator is done, so the
// program stops listening and exits.
func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"closing": true})
	s.once.Do(func() { close(s.shutdown) })
}

func summary(b *bundle.Bundle) map[string]any {
	counts := map[string]int{}
	blocked := 0
	for _, it := range b.Items {
		counts[string(it.Kind)]++
		if it.Severity == bundle.SevBlocked {
			blocked++
		}
	}
	return map[string]any{
		"source": b.Source, "counts": counts, "blocked": blocked,
		"total": len(b.Items), "notes": b.Notes,
	}
}

func toSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		panic("no randomness available: refusing to start an unprotected console")
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func isLoopback(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// openBrowser is best effort on all three desktop platforms. A failure is not
// an error: the URL is printed either way.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
