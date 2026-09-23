package dbfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/source"
)

// A minimal but real SQLite header, which is all the sniffing checks. The
// readers are tested against real databases elsewhere; what matters here is
// that the right endpoint was called with the right credentials and that
// anything which is not a database is reported as what it actually was.
func database() []byte {
	body := make([]byte, 4096)
	copy(body, sqliteMagic)
	return body
}

func send(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fetched(t *testing.T, p Panel, opt source.Options) *Result {
	t.Helper()
	res, err := Fetch(context.Background(), p, opt)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	t.Cleanup(res.Remove)
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("the downloaded file is not readable: %v", err)
	}
	if !strings.HasPrefix(string(raw), string(sqliteMagic)) {
		t.Fatal("what was written is not the database that was served")
	}
	return res
}

func TestSUITokenUsesTheV2API(t *testing.T) {
	var gotToken string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apiv2/getdb" {
			t.Errorf("a token login should go straight to the v2 API, not %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		gotToken = r.Header.Get("Token")
		_, _ = w.Write(database())
	}))
	defer ts.Close()

	fetched(t, SUI, source.Options{BaseURL: ts.URL, Token: "tok-123"})
	if gotToken != "tok-123" {
		t.Fatalf("the token was not sent in s-ui's own header: %q", gotToken)
	}
}

// The v1 API is a browser session behind a same-origin check, which a
// cross-site form post cannot pass. The marker header is what tells the two
// apart, so it has to be on the login.
func TestSUILoginUsesTheSessionAPI(t *testing.T) {
	var user, pass, marker string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		user, pass = r.FormValue("user"), r.FormValue("pass")
		marker = r.Header.Get("X-Requested-With")
		send(w, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /api/getdb", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(database())
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	fetched(t, SUI, source.Options{BaseURL: ts.URL, Username: "admin", Password: "s3cret"})
	if user != "admin" || pass != "s3cret" {
		t.Fatalf("s-ui login got user=%q pass=%q", user, pass)
	}
	if marker != "XMLHttpRequest" {
		t.Fatal("the login did not carry the marker s-ui's same-origin check accepts")
	}
}

// 3x-ui v3 guards its login with a session CSRF token fetched from its own
// endpoint. Skipping that step gets a 403 with no explanation.
func TestXUI3TakesTheCSRFTokenAndTheNewPath(t *testing.T) {
	var sentCSRF, body string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /csrf-token", func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]any{"success": true, "obj": "csrf-abc"})
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		sentCSRF = r.Header.Get("X-CSRF-Token")
		if sentCSRF != "csrf-abc" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		raw := make([]byte, 512)
		n, _ := r.Body.Read(raw)
		body = string(raw[:n])
		send(w, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /panel/api/server/getDb", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(database())
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	fetched(t, XUI3, source.Options{BaseURL: ts.URL, Username: "admin", Password: "pw", TwoFactor: "424242"})
	if sentCSRF != "csrf-abc" {
		t.Fatal("the CSRF token was not replayed on the login")
	}
	if !strings.Contains(body, `"twoFactorCode":"424242"`) {
		t.Fatalf("the two-factor code did not reach the panel: %s", body)
	}
}

// Older 3x-ui has no CSRF endpoint and serves the backup from the old path.
// One reader has to cover both generations, because the operator does not know
// which one they are running.
func TestXUI3FallsBackToTheOldPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /server/getDb", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(database())
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	res := fetched(t, XUI3, source.Options{BaseURL: ts.URL, Username: "admin", Password: "pw"})
	if !strings.HasSuffix(res.Origin, "/server/getDb") {
		t.Fatalf("origin should name the endpoint that answered, got %q", res.Origin)
	}
}

// These panels are routinely installed on a secret base path and the operator
// pastes the address they use in the browser, so the path in the address has to
// be kept rather than trimmed to the host.
func TestClassicXUIKeepsTheSecretBasePath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /s3cr3t/login", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("username") != "root" {
			w.WriteHeader(400)
			return
		}
		send(w, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /s3cr3t/server/getDb", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(database())
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	fetched(t, XUIClassic, source.Options{BaseURL: ts.URL + "/s3cr3t", Username: "root", Password: "pw"})
}

// The classic line never had API tokens. Saying so beats letting the operator
// watch three endpoints 404 in turn.
func TestClassicXUIRefusesATokenPlainly(t *testing.T) {
	_, err := Fetch(context.Background(), XUIClassic, source.Options{BaseURL: "https://example.invalid", Token: "x"})
	if err == nil || !strings.Contains(err.Error(), "no API tokens") {
		t.Fatalf("expected a plain refusal, got %v", err)
	}
}

// A wrong password is an HTTP 200 on all three of these panels, with the reason
// in the body. Reading the status alone would report success and then fail
// somewhere far less obvious.
func TestAWrongPasswordIsQuoted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]any{"success": false, "msg": "Wrong username or password"})
	}))
	defer ts.Close()

	_, err := Fetch(context.Background(), XUI3, source.Options{BaseURL: ts.URL, Username: "admin", Password: "no"})
	if err == nil || !strings.Contains(err.Error(), "Wrong username or password") {
		t.Fatalf("the panel's own words should be in the error, got %v", err)
	}
}

// The most common failure in the field: the address is missing the panel's
// secret path, so the login "works" and every page is the login page.
func TestALoginPageIsNotADatabase(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]any{"success": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>sign in</body></html>"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, err := Fetch(context.Background(), XUI3, source.Options{BaseURL: ts.URL, Username: "a", Password: "b"})
	if err == nil || !strings.Contains(err.Error(), "web page rather than its database") {
		t.Fatalf("expected the wrong-address explanation, got %v", err)
	}
}

// vaxilu's original x-ui has no backup endpoint at all. The operator needs to
// be told to copy the file rather than left guessing at the address.
func TestNoBackupEndpointSaysToUseTheFile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]any{"success": true})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	_, err := Fetch(context.Background(), XUIClassic, source.Options{BaseURL: ts.URL, Username: "a", Password: "b"})
	if err == nil || !strings.Contains(err.Error(), "use the file option instead") {
		t.Fatalf("expected the fall-back-to-a-file advice, got %v", err)
	}
}

// The downloaded copy is a whole panel including every credential it issued.
// Nothing about the migration should leave it behind.
func TestRemoveDeletesTheDownload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(database())
	}))
	defer ts.Close()

	res, err := Fetch(context.Background(), SUI, source.Options{BaseURL: ts.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	res.Remove()
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Fatalf("the downloaded database is still on disk at %s", res.Path)
	}
}

// The file channel goes straight through, so readers can call Source
// unconditionally rather than branching.
func TestSourcePassesAFilePathThrough(t *testing.T) {
	res, err := Source(context.Background(), SUI, source.Options{Channel: source.ChannelFile, Path: "/tmp/x.db"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "/tmp/x.db" || res.Origin != "/tmp/x.db" {
		t.Fatalf("got %+v", res)
	}
	res.Remove() // must not panic, and must not delete the operator's own file
}
