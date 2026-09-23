package sui

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/source"

	_ "modernc.org/sqlite"
)

// fixture builds a miniature s-ui database. It is written with the real driver
// rather than checked in as a binary so the schema this reader expects is
// visible in the test, and so a reader change that stops matching it fails
// here rather than on somebody's server.
func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s-ui.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`CREATE TABLE settings (id INTEGER PRIMARY KEY, key TEXT, value TEXT)`)
	exec(`CREATE TABLE tls (id INTEGER PRIMARY KEY, name TEXT, server TEXT, client TEXT)`)
	exec(`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, type TEXT, tag TEXT, tls_id INTEGER, addrs TEXT, out_json TEXT, options TEXT)`)
	exec(`CREATE TABLE outbounds (id INTEGER PRIMARY KEY, type TEXT, tag TEXT, options TEXT)`)
	exec(`CREATE TABLE endpoints (id INTEGER PRIMARY KEY, type TEXT, tag TEXT, options TEXT, ext TEXT)`)
	exec(`CREATE TABLE services (id INTEGER PRIMARY KEY, type TEXT, tag TEXT, options TEXT)`)
	exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password TEXT, last_logins TEXT)`)
	exec(`CREATE TABLE clients (id INTEGER PRIMARY KEY, enable INTEGER, name TEXT, config TEXT,
		inbounds TEXT, volume INTEGER, expiry INTEGER, down INTEGER, up INTEGER, "desc" TEXT,
		"group" TEXT, remark TEXT, created_at INTEGER, online_at INTEGER, delay_start INTEGER,
		auto_reset INTEGER, reset_days INTEGER, next_reset INTEGER, total_up INTEGER, total_down INTEGER)`)

	baseConfig := `{"log":{"level":"info"},"dns":{"servers":[{"tag":"google","address":"8.8.8.8"}]},
		"route":{"rules":[{"action":"sniff"}]},"experimental":{}}`
	exec(`INSERT INTO settings (key, value) VALUES (?,?)`, "config", baseConfig)
	exec(`INSERT INTO settings (key, value) VALUES (?,?)`, "subURI", "https://sub.example.com/sub/")
	exec(`INSERT INTO settings (key, value) VALUES (?,?)`, "version", "1.2.3")

	exec(`INSERT INTO tls (id, name, server) VALUES (1,'main','{"enabled":true,"server_name":"example.com"}')`)
	exec(`INSERT INTO inbounds (id, type, tag, tls_id, addrs, out_json, options) VALUES
		(1,'vless','vless in',1,'[]','{"server":"edge.example.com"}','{"listen":"::","listen_port":443}')`)
	exec(`INSERT INTO outbounds (id, type, tag, options) VALUES (1,'direct','direct','{}')`)
	exec(`INSERT INTO endpoints (id, type, tag, options, ext) VALUES (1,'warp','warp-1','{"mtu":1408}','{}')`)
	exec(`INSERT INTO services (id, type, tag, options) VALUES (1,'derp','derp-1','{}')`)
	exec(`INSERT INTO users (id, username, password) VALUES (1,'admin','hash')`)

	cfg := `{"vless":{"name":"ali","uuid":"11111111-1111-1111-1111-111111111111","flow":"xtls-rprx-vision"},
		"trojan":{"name":"ali","password":"secret-trojan"},
		"shadowsocks":{"name":"ali","password":"other-secret","method":"aes-128-gcm"}}`
	exec(`INSERT INTO clients (id, enable, name, config, inbounds, volume, expiry, down, up,
		"desc", "group", remark, created_at, online_at, delay_start, auto_reset, reset_days, next_reset, total_up, total_down)
		VALUES (1,1,'ali',?,'[1]',107374182400,1772323200,500,700,'note','vip','r',1,2,0,1,30,0,0,0)`, cfg)
	// A second client whose lifetime has not begun, and whose name needs work.
	exec(`INSERT INTO clients (id, enable, name, config, inbounds, volume, expiry, down, up,
		"desc", "group", remark, created_at, online_at, delay_start, auto_reset, reset_days, next_reset, total_up, total_down)
		VALUES (2,0,'reza ok?','{"vless":{"uuid":"22222222-2222-2222-2222-222222222222"}}','[1]',0,2592000,0,0,'','vip','',1,0,1,0,0,0,0,0)`)
	return path
}

func read(t *testing.T) *bundle.Bundle {
	t.Helper()
	b, err := source.Read(context.Background(), "s-ui", source.Options{
		Channel: source.ChannelFile, Path: fixture(t),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return b
}

func find(t *testing.T, b *bundle.Bundle, id string) bundle.Item {
	t.Helper()
	for _, it := range b.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %q in bundle (have %d items)", id, len(b.Items))
	return bundle.Item{}
}

func payload(t *testing.T, it bundle.Item) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(it.Payload, &m); err != nil {
		t.Fatalf("payload of %s: %v", it.ID, err)
	}
	return m
}

func TestReadsEveryKind(t *testing.T) {
	b := read(t)
	if b.Source.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", b.Source.Version)
	}
	counts := b.Counts()
	for kind, want := range map[bundle.Kind]int{
		bundle.KindInbound: 1,
		// s-ui's plain "direct" is the outbound Nexora puts on every node
		// itself; creating it again makes the node refuse its config.
		bundle.KindOutbound: 0,
		bundle.KindTemplate: 1,
		bundle.KindAdmin:    1,
		bundle.KindClient:   2,
		// the warp endpoint plus the unsupported service, which is listed as a
		// blocked endpoint rather than hidden
		bundle.KindEndpoint: 2,
	} {
		if counts[kind] != want {
			t.Errorf("%s count = %d, want %d", kind, counts[kind], want)
		}
	}
}

// The TLS profile an inbound points at by foreign key has to end up inside the
// inbound's config, because Nexora has no separate TLS table.
func TestInboundAbsorbsTLSAndClientConfig(t *testing.T) {
	b := read(t)
	it := find(t, b, "inbound:1")
	p := payload(t, it)

	cfg, _ := p["config"].(map[string]any)
	tls, ok := cfg["tls"].(map[string]any)
	if !ok {
		t.Fatalf("inbound config has no tls block: %v", cfg)
	}
	if tls["server_name"] != "example.com" {
		t.Errorf("server_name = %v, want example.com", tls["server_name"])
	}
	if cfg["type"] != "vless" || cfg["tag"] != it.Name {
		t.Errorf("config type/tag = %v/%v, want vless/%s", cfg["type"], cfg["tag"], it.Name)
	}
	if _, ok := p["clientConfig"]; !ok {
		t.Error("out_json should become clientConfig, which is Nexora's name for the same thing")
	}
	// "vless in" has a space, which a sing-box tag must not.
	if it.Name == it.SourceName {
		t.Errorf("tag %q should have been normalised", it.Name)
	}
}

// Nexora keeps one credential set per user. s-ui keeps one per protocol, so the
// reader must pick, and must say which protocol it dropped — silently changing
// a paying customer's Shadowsocks password is the failure this guards.
func TestCredentialCollapseIsReported(t *testing.T) {
	b := read(t)
	it := find(t, b, "client:1")
	p := payload(t, it)
	cfg, _ := p["config"].(map[string]any)

	if cfg["uuid"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("uuid = %v", cfg["uuid"])
	}
	if cfg["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %v", cfg["flow"])
	}
	if cfg["password"] != "secret-trojan" {
		t.Errorf("password = %v, want the trojan one (trojan outranks shadowsocks)", cfg["password"])
	}
	if it.Severity != bundle.SevWarn {
		t.Errorf("severity = %q, want warn", it.Severity)
	}
	var found bool
	for _, n := range it.Notes {
		if contains(n, "shadowsocks") {
			found = true
		}
	}
	if !found {
		t.Errorf("the dropped shadowsocks password must be named in the notes; got %v", it.Notes)
	}
}

// In s-ui the client name *is* the subscription id, so carrying the name
// carries the customer's existing link. That is the single most valuable thing
// this whole tool does.
func TestSubscriptionIDIsCarried(t *testing.T) {
	b := read(t)
	p := payload(t, find(t, b, "client:1"))
	if p["subId"] != "ali" {
		t.Errorf("subId = %v, want the s-ui client name", p["subId"])
	}
}

// delay_start means expiry is a length, not a date. Getting this backwards
// turns a 30-day account into one that expired in 1970.
func TestDelayStartBecomesDuration(t *testing.T) {
	b := read(t)
	p := payload(t, find(t, b, "client:2"))
	if p["duration"] != float64(2592000) {
		t.Errorf("duration = %v, want 2592000", p["duration"])
	}
	if p["expiry"] != float64(0) {
		t.Errorf("expiry = %v, want 0 — a duration account has no date yet", p["expiry"])
	}
}

func TestNameNormalisationIsReported(t *testing.T) {
	b := read(t)
	it := find(t, b, "client:2")
	if it.Name == "reza ok?" {
		t.Fatal("a name with a space and a question mark should have been cleaned")
	}
	if it.SourceName != "reza ok?" {
		t.Errorf("sourceName = %q, want the original", it.SourceName)
	}
	if len(it.Notes) == 0 {
		t.Error("a renamed account must carry a note saying so")
	}
}

// A template carries routing and DNS and names the inbounds it selects. The ids
// are not known until apply time, so the item must reference them by bundle id.
func TestTemplateCarriesRoutingAndReferencesInbounds(t *testing.T) {
	b := read(t)
	it := find(t, b, "template:s-ui")
	p := payload(t, it)
	if _, ok := p["route"]; !ok {
		t.Error("the template should carry s-ui's route block")
	}
	core, _ := p["core"].(map[string]any)
	if _, ok := core["dns"]; !ok {
		t.Errorf("the template should carry the dns block under core; got %v", core)
	}
	if got := it.IDRefs["inboundIds"]; len(got) != 1 || got[0] != "inbound:1" {
		t.Errorf("inboundIds refs = %v, want [inbound:1]", got)
	}
	if len(it.DependsOn) != 1 {
		t.Errorf("dependsOn = %v, want the inbound", it.DependsOn)
	}
}

// An s-ui service has no Nexora equivalent. It must still be listed — an
// operator who runs one needs to know it did not come — and must not be
// selectable.
func TestUnsupportedServiceIsBlockedNotHidden(t *testing.T) {
	b := read(t)
	it := find(t, b, "service:1")
	if it.Severity != bundle.SevBlocked {
		t.Errorf("severity = %q, want blocked", it.Severity)
	}
	if len(it.Notes) == 0 {
		t.Error("a blocked item must say why")
	}
}

func TestTreeGroupsByKindAndProtocol(t *testing.T) {
	b := read(t)
	roots := b.Tree()
	if len(roots) == 0 {
		t.Fatal("no tree")
	}
	// Apply order decides the tree order, so inbounds come before clients.
	var kinds []bundle.Kind
	for _, r := range roots {
		kinds = append(kinds, r.Kind)
	}
	if kinds[0] != bundle.KindInbound {
		t.Errorf("first tree root = %q, want inbounds (apply order)", kinds[0])
	}
	for _, r := range roots {
		if r.Kind == bundle.KindClient {
			if r.Count != 2 {
				t.Errorf("client root count = %d, want 2", r.Count)
			}
			if len(r.Children) != 1 || r.Children[0].Label != "vip" {
				t.Errorf("clients should be grouped by their s-ui group; got %v", r.Children)
			}
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
