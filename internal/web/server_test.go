package web

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/nexora-vpn/nexora-migrate/internal/source/sui"

	_ "modernc.org/sqlite"
)

// This is the end-to-end test: the wizard's own HTTP surface, driven exactly as
// the browser drives it, from picking a source through to the finished run. It
// is the only test that proves the five steps actually join up.

func newServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(Config{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return s, ts
}

func call(t *testing.T, ts *httptest.Server, method, path string, body any, key string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: key})
	}
	if method != http.MethodGet {
		req.Header.Set("X-Nexora-Migrate", "1")
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	if len(raw) > 0 && raw[0] == '[' {
		var arr []any
		_ = json.Unmarshal(raw, &arr)
		out["_array"] = arr
	} else {
		_ = json.Unmarshal(raw, &out)
	}
	return res.StatusCode, out
}

// suiFixture is the smallest s-ui database that produces one of everything.
func suiFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s-ui.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE settings (id INTEGER PRIMARY KEY, key TEXT, value TEXT)`,
		`CREATE TABLE tls (id INTEGER PRIMARY KEY, name TEXT, server TEXT, client TEXT)`,
		`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, type TEXT, tag TEXT, tls_id INTEGER, addrs TEXT, out_json TEXT, options TEXT)`,
		`CREATE TABLE outbounds (id INTEGER PRIMARY KEY, type TEXT, tag TEXT, options TEXT)`,
		`CREATE TABLE endpoints (id INTEGER PRIMARY KEY, type TEXT, tag TEXT, options TEXT, ext TEXT)`,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password TEXT, last_logins TEXT)`,
		`CREATE TABLE clients (id INTEGER PRIMARY KEY, enable INTEGER, name TEXT, config TEXT,
			inbounds TEXT, volume INTEGER, expiry INTEGER, down INTEGER, up INTEGER, "desc" TEXT,
			"group" TEXT, remark TEXT, created_at INTEGER, online_at INTEGER, delay_start INTEGER,
			auto_reset INTEGER, reset_days INTEGER, next_reset INTEGER, total_up INTEGER, total_down INTEGER)`,
		`INSERT INTO settings (key,value) VALUES ('config','{"route":{"rules":[]}}')`,
		`INSERT INTO inbounds (id,type,tag,tls_id,addrs,out_json,options) VALUES (1,'vless','in-1',0,'[]','{}','{"listen_port":443}')`,
		`INSERT INTO clients (id,enable,name,config,inbounds,volume,expiry,down,up,"desc","group",remark,created_at,online_at,delay_start,auto_reset,reset_days,next_reset,total_up,total_down)
			VALUES (1,1,'ali','{"vless":{"uuid":"11111111-1111-1111-1111-111111111111"}}','[1]',0,0,0,0,'','','',0,0,0,0,0,0,0,0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

// fakeNexora is the smallest panel the wizard can talk to.
func fakeNexora(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	ok := func(v any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	mux.HandleFunc("POST /api/login", ok(map[string]any{"token": "t", "username": "sudo", "role": "sudo"}))
	mux.HandleFunc("GET /api/me", ok(map[string]any{"username": "sudo", "role": "sudo"}))
	mux.HandleFunc("GET /api/license", ok(map[string]any{"usage": []map[string]any{
		{"resource": "user", "used": 0, "max": 5},
	}}))
	mux.HandleFunc("GET /api/nodes", ok([]any{}))
	for _, p := range []string{"/api/users", "/api/inbounds", "/api/outbounds", "/api/endpoints",
		"/api/rule-sets", "/api/templates", "/api/admins"} {
		mux.HandleFunc("GET "+p, ok([]any{}))
		mux.HandleFunc("POST "+p, ok(map[string]any{"id": 7}))
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// Without the one-time key nothing is reachable. This is the whole access story
// for a process holding two panels' admin credentials.
func TestUnauthorisedIsRefused(t *testing.T) {
	_, ts := newServer(t)
	if code, _ := call(t, ts, http.MethodGet, "/api/sources", nil, ""); code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", code)
	}
	if code, _ := call(t, ts, http.MethodGet, "/api/sources", nil, "wrong-key"); code != http.StatusForbidden {
		t.Errorf("with a wrong key: status = %d, want 403", code)
	}
}

// A form posted from another page in the same browser cannot set a custom
// header, so requiring one is the whole CSRF story for a loopback tool.
func TestMutatingCallsNeedTheWizardHeader(t *testing.T) {
	s, ts := newServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/read", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: cookieName, Value: s.token})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without X-Nexora-Migrate", res.StatusCode)
	}
}

func TestKeyInURLBecomesASession(t *testing.T) {
	s, ts := newServer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(ts.URL + "/?key=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want a redirect that drops the key from the address bar", res.StatusCode)
	}
	var got string
	for _, c := range res.Cookies() {
		if c.Name == cookieName {
			got = c.Value
		}
	}
	if got != s.token {
		t.Errorf("no session cookie was set")
	}
}

// Refusing to listen on a public interface is a hard refusal, not a warning:
// the alternative is an unauthenticated admin console on the internet.
func TestPublicListenIsRefusedWithoutTLS(t *testing.T) {
	if _, err := New(Config{Listen: "0.0.0.0:8787"}); err == nil {
		t.Fatal("a non-loopback listen without -allow-remote must be refused")
	}
	if _, err := New(Config{Listen: "0.0.0.0:8787", AllowRemote: true}); err == nil {
		t.Fatal("-allow-remote without TLS must be refused")
	}
	if _, err := New(Config{Listen: "0.0.0.0:8787", AllowRemote: true, TLSCert: "c", TLSKey: "k"}); err != nil {
		t.Fatalf("-allow-remote with TLS should be allowed: %v", err)
	}
}

// The whole wizard, in the order a browser walks it.
func TestFullWizardFlow(t *testing.T) {
	s, ts := newServer(t)
	key := s.token
	panel := fakeNexora(t)

	// 1. the source list
	code, out := call(t, ts, http.MethodGet, "/api/sources", nil, key)
	if code != 200 {
		t.Fatalf("sources: %d", code)
	}
	if len(out["_array"].([]any)) == 0 {
		t.Fatal("no sources are registered")
	}

	// 2. read one
	code, out = call(t, ts, http.MethodPost, "/api/read", map[string]any{
		"source":  "s-ui",
		"options": map[string]any{"channel": "file", "path": suiFixture(t)},
	}, key)
	if code != 200 {
		t.Fatalf("read: %d %v", code, out)
	}
	if out["total"].(float64) < 3 {
		t.Errorf("read produced %v items, expected the inbound, template and client", out["total"])
	}

	// 3. the tree the operator ticks
	code, out = call(t, ts, http.MethodGet, "/api/tree", nil, key)
	if code != 200 {
		t.Fatalf("tree: %d %v", code, out)
	}
	tree, _ := out["tree"].([]any)
	if len(tree) == 0 {
		t.Fatal("the tree is empty")
	}
	var ids []string
	var walk func(nodes []any)
	walk = func(nodes []any) {
		for _, raw := range nodes {
			n := raw.(map[string]any)
			if n["item"] != nil {
				ids = append(ids, n["key"].(string))
			}
			if kids, ok := n["children"].([]any); ok {
				walk(kids)
			}
		}
	}
	walk(tree)
	if len(ids) == 0 {
		t.Fatal("the tree has no leaves to select")
	}

	// 4. connect and preflight
	code, out = call(t, ts, http.MethodPost, "/api/connect", map[string]any{
		"baseUrl": panel.URL, "username": "sudo", "password": "pw",
	}, key)
	if code != 200 {
		t.Fatalf("connect: %d %v", code, out)
	}
	pre := out["preflight"].(map[string]any)
	if pre["identity"].(map[string]any)["role"] != "sudo" {
		t.Errorf("identity = %v", pre["identity"])
	}

	// 5. plan — writes nothing
	code, out = call(t, ts, http.MethodPost, "/api/plan", map[string]any{"selected": ids}, key)
	if code != 200 {
		t.Fatalf("plan: %d %v", code, out)
	}
	if out["total"].(float64) == 0 {
		t.Fatal("the plan selected nothing")
	}

	// 6. apply
	code, out = call(t, ts, http.MethodPost, "/api/apply",
		map[string]any{"selected": ids, "pauseNodes": false}, key)
	if code != 200 {
		t.Fatalf("apply: %d %v", code, out)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out = call(t, ts, http.MethodGet, "/api/result", nil, key)
		if res, ok := out["result"].(map[string]any); ok {
			if res["failed"].(float64) != 0 {
				t.Fatalf("the run reported failures: %v", res["events"])
			}
			if res["created"].(float64) == 0 {
				t.Fatalf("nothing was created: %v", res)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never finished")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Applying before connecting must be refused rather than half-attempted.
func TestApplyBeforeConnectIsRefused(t *testing.T) {
	s, ts := newServer(t)
	code, _ := call(t, ts, http.MethodPost, "/api/apply", map[string]any{"selected": []string{"x"}}, s.token)
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestReadRejectsAnUnknownPanel(t *testing.T) {
	s, ts := newServer(t)
	code, out := call(t, ts, http.MethodPost, "/api/read",
		map[string]any{"source": "nope", "options": map[string]any{}}, s.token)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if !strings.Contains(out["error"].(string), "nope") {
		t.Errorf("the error should name the panel it did not recognise: %v", out["error"])
	}
}

// Quitting closes the program; nothing else should.
func TestQuitSignalsShutdown(t *testing.T) {
	s, ts := newServer(t)
	if code, _ := call(t, ts, http.MethodPost, "/api/quit", map[string]any{}, s.token); code != 200 {
		t.Fatalf("quit: %d", code)
	}
	select {
	case <-s.shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("quit did not signal shutdown")
	}
}
