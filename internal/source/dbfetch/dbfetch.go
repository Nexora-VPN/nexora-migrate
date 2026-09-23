// Package dbfetch downloads a panel's own SQLite database over its web API.
//
// It exists so the three file-backed readers can also be pointed at a live
// panel without any of them being rewritten. s-ui, 3x-ui and the classic x-ui
// line all keep everything in one SQLite file and all expose a "download the
// database" endpoint — it is the button behind their own backup feature. So
// the API channel here is not a second reader: it logs in, pulls the same file
// the operator would have copied with scp, and hands the path to the reader
// that already knows how to read it.
//
// That is a deliberate choice over re-reading each panel through its REST API.
// A backup is the panel's own serialisation of its state: complete by
// definition, identical to the file channel byte for byte, and immune to the
// endpoint churn these panels go through every few releases. Reading the same
// data a second way would mean two code paths per panel that have to agree, and
// the one exercised less would be the one that is wrong.
//
// What it costs: the operator's login has to be an admin, and the database
// arrives whole rather than filtered. Both are true of the file channel too.
package dbfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/nexora-vpn/nexora-migrate/internal/httpx"
	"github.com/nexora-vpn/nexora-migrate/internal/source"
)

// Panel selects which login and which download path to use.
type Panel string

const (
	SUI        Panel = "s-ui"
	XUI3       Panel = "3x-ui"
	XUIClassic Panel = "x-ui"
)

// Result is a downloaded database on local disk.
type Result struct {
	// Path is a temporary file. Remove deletes it.
	Path string
	// Origin is what to show the operator instead of the temp path: where the
	// database came from, not where it happens to be parked.
	Origin string
	// Bytes is the size that arrived.
	Bytes int64
	// Note is one sentence about how it was fetched, added to the bundle so the
	// review screen says the data came off a live panel.
	Note string

	remove func()
}

// Remove deletes the temporary file. Always call it, even on a failed read:
// this file is a full copy of somebody's panel including every credential in
// it, and it has no business outliving the migration.
func (r *Result) Remove() {
	if r != nil && r.remove != nil {
		r.remove()
	}
}

// sqliteMagic is the first 16 bytes of every SQLite 3 database. Checking it is
// what turns "the reader said 'file is not a database'" into "the panel
// answered with its login page, so the address or the password is wrong".
var sqliteMagic = []byte("SQLite format 3\x00")

// Fetch logs into the panel and downloads its database.
func Fetch(ctx context.Context, p Panel, opt source.Options) (*Result, error) {
	c, err := httpx.New(opt.BaseURL, opt.Insecure)
	if err != nil {
		return nil, err
	}
	// These panels are routinely installed on a secret base path, and the
	// operator pastes the URL they use in their browser. Keep the path.
	base := c.Base

	var attempts []attempt
	switch p {
	case SUI:
		attempts, err = loginSUI(ctx, c, opt)
	case XUI3:
		attempts, err = loginXUI(ctx, c, opt, true)
	case XUIClassic:
		attempts, err = loginXUI(ctx, c, opt, false)
	default:
		return nil, fmt.Errorf("no API channel for %q", p)
	}
	if err != nil {
		return nil, err
	}

	var tried []string
	var reasons []string
	for _, a := range attempts {
		res, err := download(ctx, c, a, base, string(p))
		if err == nil {
			return res, nil
		}
		if fatal(err) {
			return nil, err
		}
		tried = append(tried, a.path)
		reasons = append(reasons, short(err))
	}
	// When every endpoint failed the same way — a login page on all of them,
	// say — that one sentence is the answer, and listing it three times buries
	// it. Only when they differ is the per-path breakdown worth reading.
	if same(reasons) {
		return nil, fmt.Errorf("logged in, but %s (tried %s)", reasons[0], strings.Join(tried, ", "))
	}
	for i := range tried {
		tried[i] += " (" + reasons[i] + ")"
	}
	return nil, fmt.Errorf(
		"logged in, but no endpoint on this panel handed over its database. Tried: %s.\n"+
			"Older builds of this panel do not have a backup endpoint at all — copy the database file off the server and use the file option instead",
		strings.Join(tried, "; "))
}

// attempt is one download endpoint to try, with whatever headers it wants.
type attempt struct {
	path    string
	headers map[string]string
}

func download(ctx context.Context, c *httpx.Client, a attempt, base, panel string) (*Result, error) {
	f, err := os.CreateTemp("", "nexora-migrate-*.db")
	if err != nil {
		return nil, fmt.Errorf("could not open a temporary file to download into: %w", err)
	}
	path := f.Name()
	cleanup := func() {
		f.Close()
		os.Remove(path)
	}

	// The first bytes decide whether this is a database or a login page, so
	// sniff them on the way through rather than reopening the file afterwards.
	head := &headSniffer{}
	n, err := c.Download(ctx, a.path, a.headers, io.MultiWriter(f, head))
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("could not finish writing the downloaded database: %w", err)
	}
	if !bytes.HasPrefix(head.buf, sqliteMagic) {
		os.Remove(path)
		return nil, notADatabase(head.buf, n)
	}

	return &Result{
		Path:   path,
		Origin: base + a.path,
		Bytes:  n,
		Note: fmt.Sprintf(
			"read from a live %s at %s: its own backup endpoint handed over the database (%s), and it was deleted from this machine as soon as it had been read",
			panel, base, size(n)),
		remove: func() { os.Remove(path) },
	}, nil
}

// headSniffer keeps the first bytes of a stream so the download can be checked
// without buffering the whole database in memory.
type headSniffer struct{ buf []byte }

func (h *headSniffer) Write(p []byte) (int, error) {
	if len(h.buf) < 512 {
		room := 512 - len(h.buf)
		if room > len(p) {
			room = len(p)
		}
		h.buf = append(h.buf, p[:room]...)
	}
	return len(p), nil
}

// notADatabase turns the usual failures into the sentence that names the cause.
func notADatabase(head []byte, n int64) error {
	text := strings.TrimSpace(string(head))
	lower := strings.ToLower(text)
	switch {
	case n == 0:
		return fmt.Errorf("the panel answered with an empty body where the database should have been")
	case strings.Contains(lower, "<!doctype html"), strings.Contains(lower, "<html"):
		return fmt.Errorf("the panel answered with a web page rather than its database — the session was not accepted, which usually means the username or password is wrong, or the address is missing the panel's secret path")
	case strings.HasPrefix(text, "{"), strings.HasPrefix(text, "["):
		var probe map[string]any
		if json.Unmarshal([]byte(text), &probe) == nil {
			for _, k := range []string{"msg", "message", "error", "detail"} {
				if s, ok := probe[k].(string); ok && s != "" {
					return fmt.Errorf("the panel refused the download: %s", s)
				}
			}
		}
		return fmt.Errorf("the panel answered with JSON rather than its database: %s", clip(text, 200))
	default:
		return fmt.Errorf("what came back is not a SQLite database (%d bytes, starting %q)", n, clip(text, 60))
	}
}

// fatal marks an error worth stopping on rather than trying the next endpoint:
// a refused download is about this panel, a 404 is about this path.
func fatal(err error) bool {
	switch httpx.Status(err) {
	case http.StatusNotFound, http.StatusMethodNotAllowed, 0:
		return false
	case http.StatusUnauthorized, http.StatusForbidden:
		// The session was rejected. Trying the other paths will not fix that,
		// but the message from the first one is the useful one.
		return true
	}
	return false
}

func short(err error) string { return clip(err.Error(), 200) }

// size is for one sentence in a note, so it rounds rather than being exact.
func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

func same(list []string) bool {
	for _, s := range list[1:] {
		if s != list[0] {
			return false
		}
	}
	return len(list) > 0
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// jsonMsg is the shape all three panels answer their POSTs with. They return
// HTTP 200 on a wrong password and say so in the body, so the body has to be
// read rather than the status.
type jsonMsg struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

func (m jsonMsg) err(what string) error {
	msg := strings.TrimSpace(m.Msg)
	if msg == "" {
		msg = "the panel gave no reason"
	}
	return fmt.Errorf("%s: %s", what, msg)
}

// Source resolves a reader's options to a database on local disk: the file the
// operator picked, or one fetched from a live panel. Readers call this instead
// of branching on the channel themselves, and defer Remove on what comes back.
func Source(ctx context.Context, p Panel, opt source.Options) (*Result, error) {
	if opt.Channel == source.ChannelAPI {
		return Fetch(ctx, p, opt)
	}
	if strings.TrimSpace(opt.Path) == "" {
		return nil, fmt.Errorf("no database file was given")
	}
	return &Result{Path: opt.Path, Origin: opt.Path}, nil
}
