package xuiclassic

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/source"

	_ "modernc.org/sqlite"
)

// fixture builds a miniature alireza0/x-ui database: the two tables that
// distinguish this line from 3x-ui (`outbounds` and `routing_rules`), a
// WireGuard inbound whose peers are stored under `clients`, and a Hysteria
// client whose secret is in `auth` rather than `password`.
func fixture(t *testing.T, withTables bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
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
	exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password TEXT)`)
	exec(`CREATE TABLE inbounds (id INTEGER PRIMARY KEY, up INTEGER, down INTEGER, total INTEGER,
		remark TEXT, enable INTEGER, expiry_time INTEGER, listen TEXT, port INTEGER, protocol TEXT,
		settings TEXT, stream_settings TEXT, tag TEXT, sniffing TEXT)`)
	exec(`CREATE TABLE client_traffics (id INTEGER PRIMARY KEY, inbound_id INTEGER, enable INTEGER,
		email TEXT, up INTEGER, down INTEGER, expiry_time INTEGER, total INTEGER, reset INTEGER)`)

	vless := `{"clients":[{"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"ali","flow":"xtls-rprx-vision","subId":"s-ali","limitIp":3,"enable":true}],"decryption":"none"}`
	exec(`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
		VALUES (1,'vless',1,'',443,'vless',?,'{"network":"tcp","security":"none"}','in-443','')`, vless)

	// Hysteria keeps the secret under `auth` in this line.
	hy := `{"clients":[{"auth":"hy-secret","email":"ali"}]}`
	exec(`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
		VALUES (2,'hy',1,'',8443,'hysteria',?,'{"network":"tcp","version":2}','in-8443','')`, hy)

	// WireGuard peers live under `clients` here, not `peers`.
	wg := `{"secretKey":"WGKEY","mtu":1420,"clients":[{"publicKey":"PEERPUB","allowedIPs":["10.0.0.2/32"],"keepAlive":25}]}`
	exec(`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
		VALUES (3,'wg',1,'',51820,'wireguard',?,'{}','in-wg','')`, wg)

	// Dokodemo-door, spelled with a capital D in this line.
	exec(`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
		VALUES (4,'dd',1,'',1080,'Dokodemo-door','{}','{}','in-dd','')`)

	exec(`INSERT INTO client_traffics (inbound_id, enable, email, up, down, expiry_time, total, reset)
		VALUES (1,1,'ali',10,20,1772323200000,107374182400,0)`)

	tpl := `{"log":{"loglevel":"warning"},"dns":{"servers":["8.8.8.8"]},
		"policy":{"levels":{"0":{"handshake":4}}},
		"routing":{"domainStrategy":"IPIfNonMatch","rules":[{"type":"field","outboundTag":"ignored","domain":["template-only.example"]}]}}`
	exec(`INSERT INTO settings (key, value) VALUES (?,?)`, "xrayTemplateConfig", tpl)
	exec(`INSERT INTO settings (key, value) VALUES (?,?)`, "subURI", "https://sub.example.com/sub/")
	exec(`INSERT INTO users (id, username, password) VALUES (1,'admin','hash')`)

	if withTables {
		exec(`CREATE TABLE outbounds (id INTEGER PRIMARY KEY, up INTEGER, down INTEGER, sort INTEGER,
			send_through TEXT, protocol TEXT, settings TEXT, tag TEXT, stream_settings TEXT,
			proxy_settings TEXT, mux TEXT, target_strategy TEXT)`)
		exec(`INSERT INTO outbounds (id, sort, protocol, settings, tag, mux, send_through)
			VALUES (1,1,'freedom','{}','direct','{"enabled":true,"concurrency":8}','192.0.2.7')`)
		exec(`INSERT INTO outbounds (id, sort, protocol, settings, tag, mux, send_through)
			VALUES (2,2,'blackhole','{}','blocked','',' ')`)

		exec(`CREATE TABLE routing_rules (id INTEGER PRIMARY KEY, tag TEXT, sort INTEGER, raw_json TEXT)`)
		exec(`INSERT INTO routing_rules (id, tag, sort, raw_json) VALUES
			(1,'ads',1,'{"type":"field","outboundTag":"blocked","domain":["geosite:category-ads-all"]}')`)
		exec(`INSERT INTO routing_rules (id, tag, sort, raw_json) VALUES
			(2,'lan',2,'{"type":"field","outboundTag":"direct","ip":["geoip:private"]}')`)
		exec(`INSERT INTO routing_rules (id, tag, sort, raw_json) VALUES (3,'broken',3,'not json')`)
	}
	return path
}

func read(t *testing.T, withTables bool) *bundle.Bundle {
	t.Helper()
	b, err := source.Read(context.Background(), "x-ui", source.Options{
		Channel: source.ChannelFile, Path: fixture(t, withTables),
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
	t.Fatalf("no item %q (have %d)", id, len(b.Items))
	return bundle.Item{}
}

func payload(t *testing.T, it bundle.Item) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(it.Payload, &m); err != nil {
		t.Fatalf("payload %s: %v", it.ID, err)
	}
	return m
}

// The two tables are what makes this panel not-3x-ui, so they are what the
// reader has to get right.
func TestOutboundsComeFromTheirOwnTable(t *testing.T) {
	b := read(t, true)
	// This direct binds an address, so it is not Nexora's plain built-in: it
	// stays, under a name that does not collide with the built-in's.
	direct := find(t, b, "outbound:direct_2")
	if payload(t, direct)["type"] != "direct" {
		t.Errorf("freedom should become direct: %v", payload(t, direct))
	}
	if payload(t, find(t, b, "outbound:blocked"))["type"] != "block" {
		t.Error("blackhole should become block")
	}
	// Two settings this line has and sing-box spells differently or not at all
	// must be reported rather than silently lost.
	if !mentions(direct, "sendThrough") {
		t.Errorf("sendThrough must be reported; notes were %v", direct.Notes)
	}
	if !mentions(direct, "mux.cool") {
		t.Errorf("mux.cool must be reported; notes were %v", direct.Notes)
	}
}

func TestRoutingRulesComeFromTheirOwnTableInOrder(t *testing.T) {
	b := read(t, true)
	tplItem := find(t, b, "template:x-ui")
	route := payload(t, tplItem)["route"].(map[string]any)
	rules := route["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("rules = %v, want the two readable rows", rules)
	}
	first := rules[0].(map[string]any)
	if first["action"] != "reject" {
		t.Errorf("a rule pointing at the blackhole outbound should reject: %v", first)
	}
	if rs, _ := first["rule_set"].([]any); len(rs) != 1 {
		t.Errorf("the geosite list should become a rule-set reference: %v", first)
	}
	second := rules[1].(map[string]any)
	if second["ip_is_private"] != true || second["outbound"] != "direct_2" {
		t.Errorf("second rule = %v", second)
	}
	// The template-only rule must NOT be there: the table wins where it exists.
	for _, raw := range rules {
		if d, _ := raw.(map[string]any)["domain"].([]any); len(d) > 0 && d[0] == "template-only.example" {
			t.Error("the template's routing should be ignored when the table has rules")
		}
	}
	// And the rule set the rules name has to actually be created.
	if len(tplItem.IDRefs["ruleSetIds"]) != 1 {
		t.Errorf("ruleSetIds = %v, want the geosite mirror", tplItem.IDRefs["ruleSetIds"])
	}
	find(t, b, "ruleset:geosite-category-ads-all")
}

func TestUnparseableRuleIsReportedNotFatal(t *testing.T) {
	b := read(t, true)
	var found bool
	for _, n := range b.Notes {
		if strings.Contains(n, "routing rule") {
			found = true
		}
	}
	if !found {
		t.Errorf("the unreadable rule must be reported; notes were %v", b.Notes)
	}
}

// An install predating those tables must still get its egress and routing, from
// the Xray template setting, which is where the original x-ui kept them.
func TestFallsBackToTheTemplateWithoutTheTables(t *testing.T) {
	b := read(t, false)
	tplItem := find(t, b, "template:x-ui")
	route := payload(t, tplItem)["route"].(map[string]any)
	rules := route["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("rules = %v, want the template's own rule", rules)
	}
	// A bare Xray domain is a substring match, which sing-box calls
	// domain_keyword — not an exact domain.
	if d, _ := rules[0].(map[string]any)["domain_keyword"].([]any); len(d) != 1 || d[0] != "template-only.example" {
		t.Errorf("the template's rule should be used: %v", rules[0])
	}
}

// WireGuard peers are under `clients` in this line and only renamed when the
// panel writes its Xray config. A reader looking for `peers` finds an endpoint
// with none, which looks fine and carries nobody.
func TestWireGuardPeersAreReadFromClients(t *testing.T) {
	b := read(t, true)
	cfg := payload(t, find(t, b, "endpoint:3"))["config"].(map[string]any)
	if cfg["private_key"] != "WGKEY" {
		t.Errorf("private_key = %v", cfg["private_key"])
	}
	peers, _ := cfg["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("peers = %v, want the one stored under `clients`", peers)
	}
	p := peers[0].(map[string]any)
	if p["public_key"] != "PEERPUB" {
		t.Errorf("peer = %v", p)
	}
	if p["persistent_keepalive_interval"] != float64(25) {
		t.Errorf("keepAlive should carry across: %v", p)
	}
}

// Hysteria keeps its secret under `auth` here, and losing it means the account
// silently stops working.
func TestHysteriaAuthIsTakenAsThePassword(t *testing.T) {
	b := read(t, true)
	cfg := payload(t, find(t, b, "client:ali"))["config"].(map[string]any)
	if cfg["password"] != "hy-secret" {
		t.Errorf("password = %v, want the hysteria `auth` value", cfg["password"])
	}
	if cfg["uuid"] != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Errorf("uuid = %v", cfg["uuid"])
	}
}

func TestDokodemoIsBlockedNotHidden(t *testing.T) {
	b := read(t, true)
	it := find(t, b, "inbound:4")
	if it.Severity != bundle.SevBlocked {
		t.Errorf("severity = %q, want blocked", it.Severity)
	}
}

func TestPolicyBlockIsReported(t *testing.T) {
	b := read(t, true)
	if !mentions(find(t, b, "template:x-ui"), "policy") {
		t.Error("the dropped Xray policy block must be reported on the template")
	}
}

func TestClientCarriesItsSubscriptionAndLimits(t *testing.T) {
	b := read(t, true)
	p := payload(t, find(t, b, "client:ali"))
	if p["subId"] != "s-ali" {
		t.Errorf("subId = %v — carrying it is what keeps the customer's link working", p["subId"])
	}
	if p["ipLimit"] != float64(3) {
		t.Errorf("ipLimit = %v", p["ipLimit"])
	}
	if p["expiry"] != float64(1772323200) {
		t.Errorf("expiry = %v, want seconds not milliseconds", p["expiry"])
	}
	if p["volume"] != float64(107374182400) {
		t.Errorf("volume = %v", p["volume"])
	}
}

func mentions(it bundle.Item, sub string) bool {
	for _, n := range it.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
