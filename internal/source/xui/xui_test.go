package xui

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

// fixture builds a miniature 3x-ui database: two inbounds sharing one client
// (which is how 3x-ui represents "this person is on both"), a WireGuard inbound
// that has to become an endpoint, and the Xray template that holds routing,
// DNS and the egress list.
func fixture(t *testing.T) string {
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
		email TEXT, up INTEGER, down INTEGER, expiry_time INTEGER, total INTEGER, reset INTEGER,
		reset_day INTEGER, last_online INTEGER)`)

	vlessSettings := `{"clients":[
		{"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","email":"ali","flow":"xtls-rprx-vision","subId":"sub-ali","limitIp":2,"totalGB":0,"expiryTime":0,"enable":true},
		{"id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","email":"reza kh","subId":"sub-reza","limitIp":0,"totalGB":0,"expiryTime":-2592000000,"enable":true}
	],"decryption":"none"}`
	realityStream := `{"network":"tcp","security":"reality","realitySettings":{
		"dest":"www.microsoft.com:443","serverNames":["www.microsoft.com","microsoft.com"],
		"privateKey":"PRIVATE","shortIds":["0123abcd"]}}`
	exec(`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
		VALUES (1,'reality 443',1,'',443,'vless',?,?,'inbound-443','{"enabled":true}')`, vlessSettings, realityStream)

	trojanSettings := `{"clients":[{"password":"trojan-pass","email":"ali"}]}`
	wsStream := `{"network":"ws","security":"tls","wsSettings":{"path":"/ws","headers":{"Host":"cdn.example.com"}},
		"tlsSettings":{"serverName":"cdn.example.com","alpn":["h2","http/1.1"],
		"certificates":[{"certificateFile":"/etc/ssl/f.crt","keyFile":"/etc/ssl/f.key"}]}}`
	exec(`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
		VALUES (2,'ws 8443',1,'',8443,'trojan',?,?,'inbound-8443','')`, trojanSettings, wsStream)

	wg := `{"secretKey":"WGKEY","mtu":1420,"peers":[{"publicKey":"PEERPUB","allowedIPs":["10.0.0.2/32"]}]}`
	exec(`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
		VALUES (3,'wg',1,'',51820,'wireguard',?,'{}','inbound-wg','')`, wg)

	exec(`INSERT INTO client_traffics (inbound_id, enable, email, up, down, expiry_time, total, reset)
		VALUES (1,1,'ali',1000,2000,1772323200000,107374182400,30)`)
	exec(`INSERT INTO client_traffics (inbound_id, enable, email, up, down, expiry_time, total, reset)
		VALUES (1,1,'reza kh',0,0,-2592000000,0,0)`)
	// An orphan: the inbound it belonged to was deleted under it.
	exec(`INSERT INTO client_traffics (inbound_id, enable, email, up, down, expiry_time, total, reset)
		VALUES (9,1,'ghost',5,5,0,0,0)`)

	tpl := `{"log":{"loglevel":"warning"},
		"dns":{"servers":["8.8.8.8",{"address":"1.1.1.1"}]},
		"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}],
		"routing":{"rules":[
			{"type":"field","outboundTag":"blocked","domain":["geosite:category-ads-all","full:bad.example"]},
			{"type":"field","outboundTag":"direct","ip":["geoip:private","10.0.0.0/8"],"port":"80,443"}
		]}}`
	exec(`INSERT INTO settings (key, value) VALUES (?,?)`, "xrayTemplateConfig", tpl)
	exec(`INSERT INTO settings (key, value) VALUES (?,?)`, "subURI", "https://sub.example.com/sub/")
	exec(`INSERT INTO users (id, username, password) VALUES (1,'admin','hash')`)
	return path
}

func read(t *testing.T) *bundle.Bundle {
	t.Helper()
	b, err := source.Read(context.Background(), "3x-ui", source.Options{
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

// REALITY is the protocol most of this market actually sells, and Xray and
// sing-box disagree about nearly every field name in it.
func TestRealityConversion(t *testing.T) {
	b := read(t)
	cfg := payload(t, find(t, b, "inbound:1"))["config"].(map[string]any)
	if cfg["type"] != "vless" {
		t.Fatalf("type = %v", cfg["type"])
	}
	if cfg["listen"] != "::" {
		t.Errorf("listen = %v, want :: — sing-box defaults an empty listen to loopback, which silently kills a public inbound", cfg["listen"])
	}
	tls := cfg["tls"].(map[string]any)
	if tls["server_name"] != "www.microsoft.com" {
		t.Errorf("server_name = %v", tls["server_name"])
	}
	r := tls["reality"].(map[string]any)
	if r["private_key"] != "PRIVATE" {
		t.Errorf("private_key = %v", r["private_key"])
	}
	ids, _ := r["short_id"].([]any)
	if len(ids) != 1 || ids[0] != "0123abcd" {
		t.Errorf("short_id = %v", ids)
	}
	hs, ok := r["handshake"].(map[string]any)
	if !ok {
		t.Fatalf("reality has no handshake block: %v", r)
	}
	if hs["server"] != "www.microsoft.com" || hs["server_port"] != float64(443) {
		t.Errorf("handshake = %v, want Xray's dest split into server and port", hs)
	}
}

func TestWebsocketAndTLSConversion(t *testing.T) {
	b := read(t)
	it := find(t, b, "inbound:2")
	cfg := payload(t, it)["config"].(map[string]any)

	tr := cfg["transport"].(map[string]any)
	if tr["type"] != "ws" || tr["path"] != "/ws" {
		t.Errorf("transport = %v", tr)
	}
	h, _ := tr["headers"].(map[string]any)
	if h["Host"] != "cdn.example.com" {
		t.Errorf("ws headers = %v", h)
	}
	tls := cfg["tls"].(map[string]any)
	if tls["certificate_path"] != "/etc/ssl/f.crt" || tls["key_path"] != "/etc/ssl/f.key" {
		t.Errorf("certificate paths = %v", tls)
	}
	// A certificate that is a path on the *old* server is a trap, so it must be
	// called out rather than carried silently.
	if !noteMentions(it, "old server") {
		t.Errorf("a file-path certificate must be flagged; notes were %v", it.Notes)
	}
}

// WireGuard is an inbound in 3x-ui and an endpoint in sing-box.
func TestWireGuardBecomesEndpoint(t *testing.T) {
	b := read(t)
	it := find(t, b, "endpoint:3")
	if it.Kind != bundle.KindEndpoint {
		t.Fatalf("kind = %v, want endpoint", it.Kind)
	}
	cfg := payload(t, it)["config"].(map[string]any)
	if cfg["type"] != "wireguard" || cfg["private_key"] != "WGKEY" {
		t.Errorf("config = %v", cfg)
	}
	peers, _ := cfg["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("peers = %v", peers)
	}
	p := peers[0].(map[string]any)
	if p["public_key"] != "PEERPUB" {
		t.Errorf("peer = %v", p)
	}
}

// One person on a VLESS inbound and a Trojan inbound is one Nexora user with
// both secrets — not two users, and not a conflict.
func TestOneAccountAcrossTwoInbounds(t *testing.T) {
	b := read(t)
	it := find(t, b, "client:ali")
	cfg := payload(t, it)["config"].(map[string]any)
	if cfg["uuid"] != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Errorf("uuid = %v", cfg["uuid"])
	}
	if cfg["password"] != "trojan-pass" {
		t.Errorf("password = %v — the trojan inbound's secret belongs on the same user", cfg["password"])
	}
	if it.Detail["inbounds"] != "2" {
		t.Errorf("inbounds detail = %q, want 2", it.Detail["inbounds"])
	}
	for _, n := range it.Notes {
		if strings.Contains(n, "credential set") {
			t.Errorf("there is no credential conflict here, but one was reported: %s", n)
		}
	}
}

// 3x-ui stores milliseconds, and a negative value means "this much lifetime,
// not started yet". Reading it as a date puts the account in 1901.
func TestNegativeExpiryBecomesDuration(t *testing.T) {
	b := read(t)
	var it bundle.Item
	for _, cand := range b.Items {
		if cand.Kind == bundle.KindClient && cand.SourceName == "reza kh" {
			it = cand
		}
	}
	if it.ID == "" {
		t.Fatal("the second client is missing")
	}
	p := payload(t, it)
	if p["duration"] != float64(2592000) {
		t.Errorf("duration = %v, want 2592000 seconds", p["duration"])
	}
	if p["expiry"] != float64(0) {
		t.Errorf("expiry = %v, want 0", p["expiry"])
	}
	if it.Name == "reza kh" {
		t.Error("a name with a space should have been normalised")
	}
}

func TestMillisecondExpiryAndQuota(t *testing.T) {
	b := read(t)
	p := payload(t, find(t, b, "client:ali"))
	if p["expiry"] != float64(1772323200) {
		t.Errorf("expiry = %v, want seconds not milliseconds", p["expiry"])
	}
	if p["volume"] != float64(107374182400) {
		t.Errorf("volume = %v", p["volume"])
	}
	if p["up"] != float64(1000) || p["down"] != float64(2000) {
		t.Errorf("counters = %v/%v", p["up"], p["down"])
	}
	if p["ipLimit"] != float64(2) {
		t.Errorf("ipLimit = %v", p["ipLimit"])
	}
	if p["subId"] != "sub-ali" {
		t.Errorf("subId = %v — carrying it is what keeps the customer's link working", p["subId"])
	}
	if p["autoReset"] != true || p["resetDays"] != float64(30) {
		t.Errorf("reset = %v/%v", p["autoReset"], p["resetDays"])
	}
}

// A traffic row whose inbound was deleted is still somebody's account. It must
// appear, and it must be blocked rather than imported with a new uuid.
func TestOrphanedTrafficRowIsBlocked(t *testing.T) {
	b := read(t)
	it := find(t, b, "client:ghost")
	if it.Severity != bundle.SevBlocked {
		t.Errorf("severity = %q, want blocked — importing it would mint a new uuid behind the customer's back", it.Severity)
	}
}

func TestRoutingAndDNSConversion(t *testing.T) {
	b := read(t)
	it := find(t, b, "template:3x-ui")
	p := payload(t, it)

	route := p["route"].(map[string]any)
	rules := route["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("rules = %v", rules)
	}
	first := rules[0].(map[string]any)
	if first["action"] != "reject" {
		t.Errorf("an outboundTag of `blocked` should become sing-box's reject action; got %v", first)
	}
	if got, _ := first["domain"].([]any); len(got) != 1 || got[0] != "bad.example" {
		t.Errorf("full: prefix should become an exact domain; got %v", first["domain"])
	}
	if rs, _ := first["rule_set"].([]any); len(rs) != 1 || rs[0] != "geosite-category-ads-all" {
		t.Errorf("a geosite list should become a rule-set reference; got %v", first["rule_set"])
	}
	second := rules[1].(map[string]any)
	if second["ip_is_private"] != true {
		t.Errorf("geoip:private should become ip_is_private; got %v", second)
	}
	if ports, _ := second["port"].([]any); len(ports) != 2 {
		t.Errorf("port list = %v", ports)
	}

	core := p["core"].(map[string]any)
	dns := core["dns"].(map[string]any)
	if servers, _ := dns["servers"].([]any); len(servers) != 2 {
		t.Errorf("dns servers = %v", servers)
	}
	// "direct" is folded into the node's built-in outbound, so only the
	// blackhole is an outbound of its own.
	if refs := it.IDRefs["outboundIds"]; len(refs) != 1 {
		t.Errorf("the template should name the one created outbound; got %v", refs)
	}
}

func TestOutboundsFromTemplate(t *testing.T) {
	b := read(t)
	// A plain freedom tagged "direct" is exactly the outbound Nexora puts on
	// every node; created again, the node refuses its config as a duplicate.
	for _, it := range b.Items {
		if it.Kind == bundle.KindOutbound && strings.EqualFold(it.SourceName, "direct") {
			t.Errorf("the plain direct outbound must not be created again: %s", it.ID)
		}
	}
	blocked := payload(t, find(t, b, "outbound:blocked"))
	if blocked["type"] != "block" {
		t.Errorf("blackhole should become block; got %v", blocked["type"])
	}
}

func noteMentions(it bundle.Item, sub string) bool {
	for _, n := range it.Notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
