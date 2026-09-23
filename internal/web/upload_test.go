package web

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The file picker is the normal way an operator hands over a panel database, so
// its whole round trip — upload, park, read, delete — is worth a test of its
// own.

func post(t *testing.T, url, key, name string, body []byte) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: cookieName, Value: key})
	req.Header.Set("X-Nexora-Migrate", "1")
	req.Header.Set("X-File-Name", name)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, decode(t, raw)
}

func TestUploadParksTheDatabaseAndReportsIt(t *testing.T) {
	s, ts := newServer(t)

	content := make([]byte, 8192)
	copy(content, "SQLite format 3\x00")
	code, out := post(t, ts.URL+"/api/upload", s.token, "x-ui.db", content)
	if code != 200 {
		t.Fatalf("upload refused: %d %v", code, out)
	}
	path, _ := out["path"].(string)
	if path == "" {
		t.Fatal("no path came back for the reader to use")
	}
	if out["name"] != "x-ui.db" {
		t.Fatalf("the file name did not survive: %v", out["name"])
	}
	if n, _ := out["size"].(float64); int(n) != len(content) {
		t.Fatalf("size %v, uploaded %d bytes", out["size"], len(content))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("what was parked is not readable: %v", err)
	}
	if len(got) != len(content) {
		t.Fatalf("parked %d bytes of %d", len(got), len(content))
	}

	// A second file replaces the first: an uploaded panel database holds every
	// credential that panel issued, and there is no reason to keep two.
	code, out2 := post(t, ts.URL+"/api/upload", s.token, "s-ui.db", content)
	if code != 200 {
		t.Fatalf("second upload refused: %d %v", code, out2)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the first upload was left on disk")
	}

	// And nothing outlives the wizard.
	second, _ := out2["path"].(string)
	s.discardUploads()
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("%s survived the wizard closing", second)
	}
}

// Picking the wrong file is the single most likely mistake on this screen, and
// the moment to say so is while the operator is still looking at the picker.
func TestUploadRejectsSomethingThatIsNotADatabase(t *testing.T) {
	s, ts := newServer(t)

	code, out := post(t, ts.URL+"/api/upload", s.token, "backup.tar.gz", []byte("\x1f\x8b\x08 not a database at all"))
	if code != 400 {
		t.Fatalf("expected a refusal, got %d %v", code, out)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "not a SQLite database") || !strings.Contains(msg, "backup.tar.gz") {
		t.Fatalf("the refusal should name the file and the reason: %q", msg)
	}
}

func TestUploadRejectsAnEmptyFile(t *testing.T) {
	s, ts := newServer(t)
	code, out := post(t, ts.URL+"/api/upload", s.token, "x-ui.db", nil)
	if code != 400 || !strings.Contains(out["error"].(string), "empty") {
		t.Fatalf("got %d %v", code, out)
	}
}

// The upload endpoint is behind the same door as everything else: a page on
// another origin cannot set the wizard's header, and nothing without the
// session cookie gets in at all.
func TestUploadIsGuarded(t *testing.T) {
	_, ts := newServer(t)
	code, _ := post(t, ts.URL+"/api/upload", "not-the-key", "x.db", []byte("SQLite format 3\x00"))
	if code != http.StatusForbidden {
		t.Fatalf("an unauthorised upload was accepted with %d", code)
	}
}

// decode is the one-line JSON reader these tests share with server_test.go.
func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the server did not answer JSON: %s", raw)
	}
	return out
}
