package apply

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/target"
)

// fakePanel is a Nexora stand-in that records what was asked of it. The applier
// is the one part of this tool that writes to somebody's production panel, so
// its behaviour is pinned against a real HTTP server rather than a mock object.
type fakePanel struct {
	mu       sync.Mutex
	nextID   uint
	created  []createdRow
	nodeOps  []string
	conflict map[string]bool // path → answer 409
	full     map[string]bool // path → answer 402 (licence)
}

type createdRow struct {
	Path string
	Body map[string]any
}

func newPanel() *fakePanel {
	return &fakePanel{conflict: map[string]bool{}, full: map[string]bool{}}
}

func (f *fakePanel) start(t *testing.T) *target.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"token": "tok", "username": "sudo", "role": "sudo"})
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"username": "sudo", "role": "sudo"})
	})
	mux.HandleFunc("GET /api/license", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"usage": []map[string]any{
			{"resource": "user", "used": 1, "max": 10},
			{"resource": "inbound", "used": 0, "max": 0},
		}})
	})
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]any{
			{"id": 1, "name": "de-1", "enabled": true},
			{"id": 2, "name": "nl-1", "enabled": false},
			// Switched off by its usage limit: re-enabling it would clear that.
			{"id": 3, "name": "fi-1", "enabled": true, "limitDisabled": true},
		})
	})
	mux.HandleFunc("POST /api/nodes/{id}/enabled", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.nodeOps = append(f.nodeOps, r.PathValue("id")+":enabled="+boolStr(body.Enabled))
		f.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/nodes/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.nodeOps = append(f.nodeOps, r.PathValue("id")+":sync")
		f.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	})
	// Every list endpoint the preflight touches.
	for _, p := range []string{"/api/users", "/api/inbounds", "/api/outbounds",
		"/api/endpoints", "/api/rule-sets", "/api/templates", "/api/admins"} {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []map[string]any{})
		})
		path := p
		mux.HandleFunc("POST "+p, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.full[path] {
				w.WriteHeader(http.StatusPaymentRequired)
				writeJSON(w, map[string]string{"error": "license limit reached"})
				return
			}
			if f.conflict[path] {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]string{"error": "already exists"})
				return
			}
			f.nextID++
			f.created = append(f.created, createdRow{Path: path, Body: body})
			writeJSON(w, map[string]any{"id": f.nextID})
		})
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := target.Connect(context.Background(), target.Options{
		BaseURL: srv.URL, Username: "sudo", Password: "pw",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c
}

func (f *fakePanel) find(path string) *createdRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.created {
		if f.created[i].Path == path {
			return &f.created[i]
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// testBundle is an inbound, a template that names it, and a client on that
// template — the dependency chain the whole applier exists to get right.
func testBundle() *bundle.Bundle {
	b := &bundle.Bundle{}
	b.Add(bundle.Item{
		ID: "inbound:1", Kind: bundle.KindInbound, Name: "vless-443",
		Payload: json.RawMessage(`{"tag":"vless-443","type":"vless"}`),
	})
	b.Add(bundle.Item{
		ID: "template:1", Kind: bundle.KindTemplate, Name: "main",
		Payload: json.RawMessage(`{"name":"main","inboundIds":[]}`),
		IDRefs:  map[string][]string{"inboundIds": {"inbound:1"}},
	})
	b.Add(bundle.Item{
		ID: "admin:1", Kind: bundle.KindAdmin, Name: "reseller",
		Payload: json.RawMessage(`{"username":"reseller","password":"generated-pw","role":"resale"}`),
	})
	b.Add(bundle.Item{
		ID: "client:1", Kind: bundle.KindClient, Name: "ali",
		Payload: json.RawMessage(`{"name":"ali","allTemplates":false,"templateIds":[]}`),
		IDRefs:  map[string][]string{"templateIds": {"template:1"}},
		IDRef:   map[string]string{"adminId": "admin:1"},
	})
	b.Add(bundle.Item{
		ID: "client:2", Kind: bundle.KindClient, Name: "broken",
		Severity: bundle.SevBlocked,
		Payload:  json.RawMessage(`{"name":"broken"}`),
	})
	b.Validate()
	return b
}

// Choosing the client alone has to bring its template, and that template's
// inbound, or the account lands on a template that routes nowhere.
func TestSelectPullsInDependencies(t *testing.T) {
	b := testBundle()
	items, added := Select(b, map[string]bool{"client:1": true})

	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	want := []string{"inbound:1", "template:1", "admin:1", "client:1"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("selection = %v, want %v (in apply order)", ids, want)
	}
	if len(added) != 3 {
		t.Errorf("added = %v, want the three dependencies", added)
	}
}

// A blocked item is never applied, even when something else depends on it.
func TestSelectNeverIncludesBlocked(t *testing.T) {
	b := testBundle()
	items, _ := Select(b, map[string]bool{"client:2": true})
	if len(items) != 0 {
		t.Errorf("a blocked item was selected: %v", items)
	}
}

// The whole point of the late binding: a template names an inbound by the id
// Nexora assigned it, which is only known once it has been created.
func TestIDsAreSubstitutedAfterCreation(t *testing.T) {
	panel := newPanel()
	c := panel.start(t)
	items, _ := Select(testBundle(), map[string]bool{"client:1": true})

	res := Run(context.Background(), c, items, Options{}, nil)
	if res.Failed != 0 {
		t.Fatalf("run failed: %+v", res.Events)
	}
	if res.Created != 4 {
		t.Errorf("created = %d, want 4", res.Created)
	}

	tpl := panel.find("/api/templates")
	if tpl == nil {
		t.Fatal("no template was created")
	}
	ids, _ := tpl.Body["inboundIds"].([]any)
	if len(ids) != 1 || ids[0] != float64(1) {
		t.Errorf("template inboundIds = %v, want the created inbound's id", tpl.Body["inboundIds"])
	}

	user := panel.find("/api/users")
	if user == nil {
		t.Fatal("no user was created")
	}
	tids, _ := user.Body["templateIds"].([]any)
	if len(tids) != 1 || tids[0] != float64(2) {
		t.Errorf("user templateIds = %v, want the created template's id", user.Body["templateIds"])
	}
	if user.Body["adminId"] != float64(3) {
		t.Errorf("user adminId = %v, want the created admin's id", user.Body["adminId"])
	}
}

// A client whose template was left out of the run must not arrive provisioned
// on nothing. Nexora's "every template" flag is the right answer.
func TestClientWithoutItsTemplateGoesEverywhere(t *testing.T) {
	panel := newPanel()
	c := panel.start(t)

	b := testBundle()
	// Take only the client, then strip its dependencies back out — the shape a
	// selection reaches when the operator has already built templates by hand.
	var only []bundle.Item
	for _, it := range b.Items {
		if it.ID == "client:1" {
			it.IDRefs = map[string][]string{"templateIds": {}}
			it.IDRef = nil
			only = append(only, it)
		}
	}
	res := Run(context.Background(), c, only, Options{}, nil)
	if res.Failed != 0 {
		t.Fatalf("run failed: %+v", res.Events)
	}
	user := panel.find("/api/users")
	if user.Body["allTemplates"] != true {
		t.Errorf("allTemplates = %v, want true", user.Body["allTemplates"])
	}
	if _, ok := user.Body["templateIds"]; ok {
		t.Errorf("an empty templateIds must be removed, not sent: %v", user.Body)
	}
}

// Pausing the fleet is what makes a large import finish in minutes instead of
// hours, and the nodes must come back on afterwards no matter what.
func TestNodesArePausedAndResumed(t *testing.T) {
	panel := newPanel()
	c := panel.start(t)
	items, _ := Select(testBundle(), map[string]bool{"client:1": true})

	res := Run(context.Background(), c, items, Options{PauseNodes: true}, nil)
	var resumed bool
	for _, e := range res.Events {
		resumed = resumed || e.Phase == "nodes resumed"
	}
	if !resumed {
		t.Error("the fleet coming back must be in the final report, not only on the stream")
	}

	panel.mu.Lock()
	ops := strings.Join(panel.nodeOps, " ")
	panel.mu.Unlock()

	// Node 1 was enabled, so it is paused, re-enabled and synced. Node 2 was
	// already off and must be left alone, and so must node 3, which its usage
	// limit switched off.
	for _, want := range []string{"1:enabled=false", "1:enabled=true", "1:sync"} {
		if !strings.Contains(ops, want) {
			t.Errorf("node operations %q missing %q", ops, want)
		}
	}
	if strings.Contains(ops, "2:") {
		t.Errorf("a node that was already disabled must not be touched: %q", ops)
	}
	if strings.Contains(ops, "3:") {
		t.Errorf("a node its usage limit switched off must not be touched: %q", ops)
	}
}

// A name the panel already holds is not an error worth stopping for, but it
// must be reported rather than counted as success.
func TestExistingNameIsReportedNotFailed(t *testing.T) {
	panel := newPanel()
	panel.conflict["/api/users"] = true
	c := panel.start(t)
	items, _ := Select(testBundle(), map[string]bool{"client:1": true})

	res := Run(context.Background(), c, items, Options{}, nil)
	if res.Existing != 1 {
		t.Errorf("existing = %d, want 1", res.Existing)
	}
	if res.Failed != 0 {
		t.Errorf("failed = %d, want 0 — a duplicate name is not a failure", res.Failed)
	}
}

// A full licence must stop the run. Repeating a 402 a few thousand times helps
// nobody and leaves the operator scrolling to find the first one.
func TestLicenceRefusalStopsTheRun(t *testing.T) {
	panel := newPanel()
	panel.full["/api/inbounds"] = true
	c := panel.start(t)
	items, _ := Select(testBundle(), map[string]bool{"client:1": true})

	res := Run(context.Background(), c, items, Options{}, nil)
	if res.Failed != 1 {
		t.Errorf("failed = %d, want 1", res.Failed)
	}
	if res.Skipped != 3 {
		t.Errorf("skipped = %d, want the remaining 3", res.Skipped)
	}
	last := res.Events[len(res.Events)-1]
	if last.Phase != "stopped" {
		t.Errorf("the run should end with a `stopped` phase; got %+v", last)
	}
}

// The generated admin password exists nowhere else, so the run has to hand it
// back or the operator has locked themselves out of an account they just made.
func TestAdminPasswordIsReturned(t *testing.T) {
	panel := newPanel()
	c := panel.start(t)
	items, _ := Select(testBundle(), map[string]bool{"admin:1": true})

	res := Run(context.Background(), c, items, Options{}, nil)
	if res.Passwords["reseller"] != "generated-pw" {
		t.Errorf("passwords = %v, want the generated one for `reseller`", res.Passwords)
	}
}

func TestCancelledRunStopsAndSaysSo(t *testing.T) {
	panel := newPanel()
	c := panel.start(t)
	items, _ := Select(testBundle(), map[string]bool{"client:1": true})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Run(ctx, c, items, Options{}, nil)
	if res.Created != 0 {
		t.Errorf("created = %d, want 0 on an already-cancelled run", res.Created)
	}
	if res.Skipped != len(items) {
		t.Errorf("skipped = %d, want %d", res.Skipped, len(items))
	}
}
