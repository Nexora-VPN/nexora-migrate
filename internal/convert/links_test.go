package convert

import (
	"encoding/base64"
	"testing"
)

// These links are the only way to learn a Marzneshin account's real
// credentials, so parsing them wrong means handing a paying customer a new uuid
// without telling them.

func TestParseVLESS(t *testing.T) {
	c, ok := ParseShareLink("vless://aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa@1.2.3.4:443?type=tcp&security=reality&flow=xtls-rprx-vision#name")
	if !ok {
		t.Fatal("not parsed")
	}
	if c.UUID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Errorf("uuid = %q", c.UUID)
	}
	if c.Flow != "xtls-rprx-vision" {
		t.Errorf("flow = %q", c.Flow)
	}
}

func TestParseTrojan(t *testing.T) {
	c, ok := ParseShareLink("trojan://my%2Fsecret@host:443#label")
	if !ok {
		t.Fatal("not parsed")
	}
	if c.Password != "my/secret" {
		t.Errorf("password = %q, want the percent-decoding applied", c.Password)
	}
}

func TestParseVMess(t *testing.T) {
	doc := `{"v":"2","ps":"x","add":"1.2.3.4","port":"443","id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","aid":"0","net":"ws"}`
	link := "vmess://" + base64.StdEncoding.EncodeToString([]byte(doc))
	c, ok := ParseShareLink(link)
	if !ok {
		t.Fatal("not parsed")
	}
	if c.UUID != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Errorf("uuid = %q", c.UUID)
	}
}

func TestParseShadowsocksSIP002(t *testing.T) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:hunter2"))
	c, ok := ParseShareLink("ss://" + userinfo + "@1.2.3.4:8388#x")
	if !ok {
		t.Fatal("not parsed")
	}
	if c.Method != "aes-256-gcm" || c.Password != "hunter2" {
		t.Errorf("got %+v", c)
	}
}

// A whole subscription collapses to one credential set, with the protocols
// whose secret had to be dropped named.
func TestMergeLinksReportsConflicts(t *testing.T) {
	uuid, password, flow, method, conflicts := MergeLinks([]string{
		"vless://11111111-1111-1111-1111-111111111111@h:443?flow=xtls-rprx-vision#a",
		"trojan://pw-one@h:443#b",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:pw-two")) + "@h:8388#c",
	})
	if uuid != "11111111-1111-1111-1111-111111111111" || flow != "xtls-rprx-vision" {
		t.Errorf("uuid/flow = %q/%q", uuid, flow)
	}
	if password != "pw-one" {
		t.Errorf("password = %q, want the trojan one (it comes first)", password)
	}
	if method != "" {
		t.Errorf("method = %q — it belongs to the password that was kept, not a discarded one", method)
	}
	if len(conflicts) != 1 || conflicts[0] != "shadowsocks" {
		t.Errorf("conflicts = %v, want shadowsocks named", conflicts)
	}
}

// Every panel in this space serves the link list base64-wrapped; some do not.
func TestSplitSubscriptionAcceptsBothForms(t *testing.T) {
	plain := "vless://a@h:1#x\ntrojan://b@h:2#y\n"
	if got := SplitSubscription([]byte(plain)); len(got) != 2 {
		t.Errorf("plain: %v", got)
	}
	wrapped := base64.StdEncoding.EncodeToString([]byte(plain))
	if got := SplitSubscription([]byte(wrapped)); len(got) != 2 {
		t.Errorf("base64: %v", got)
	}
	if got := SplitSubscription([]byte("   ")); got != nil {
		t.Errorf("empty: %v", got)
	}
}

func TestRejectsNonsense(t *testing.T) {
	for _, s := range []string{"", "http://example.com", "vless://", "vmess://not-base64!!"} {
		if _, ok := ParseShareLink(s); ok {
			t.Errorf("%q should not parse", s)
		}
	}
}

// The DNS server shape is the one place a wrong guess reached a real panel
// during development: sing-box 1.14 removed the legacy `{"address": …}` server
// and Nexora rejects a template carrying one. These pin the typed form.
func TestDNSServersAreTyped(t *testing.T) {
	out, _ := DNS(Obj{"servers": []any{
		"8.8.8.8",
		Obj{"address": "1.1.1.1:5353"},
		"tls://dns.google",
		"https+local://dns.google/dns-query",
		"localhost",
	}})
	servers, _ := out["servers"].([]Obj)
	if len(servers) != 5 {
		t.Fatalf("got %d servers: %+v", len(servers), servers)
	}
	for i, s := range servers {
		if _, ok := s["address"]; ok {
			t.Errorf("server %d still carries the legacy `address` key: %+v", i, s)
		}
		if s["type"] == nil || s["type"] == "" {
			t.Errorf("server %d has no type: %+v", i, s)
		}
	}
	if servers[0]["type"] != "udp" || servers[0]["server"] != "8.8.8.8" {
		t.Errorf("plain address: %+v", servers[0])
	}
	if servers[1]["server"] != "1.1.1.1" || servers[1]["server_port"] != 5353 {
		t.Errorf("host:port should split: %+v", servers[1])
	}
	if servers[2]["type"] != "tls" {
		t.Errorf("tls:// : %+v", servers[2])
	}
	if servers[3]["type"] != "https" || servers[3]["path"] != "/dns-query" {
		t.Errorf("https:// with a path: %+v", servers[3])
	}
	if servers[4]["type"] != "local" {
		t.Errorf("localhost: %+v", servers[4])
	}
}

func TestUnknownDNSSchemeIsReportedNotEmitted(t *testing.T) {
	out, notes := DNS(Obj{"servers": []any{"dnscrypt://example", "8.8.8.8"}})
	servers, _ := out["servers"].([]Obj)
	if len(servers) != 1 {
		t.Errorf("got %d servers, want only the one that converts", len(servers))
	}
	if len(notes) == 0 {
		t.Error("a dropped server must be reported")
	}
}

// The `dns` outbound went away in sing-box 1.13 and Nexora refuses a config
// carrying one, so it must never be produced.
func TestDNSOutboundIsNotProduced(t *testing.T) {
	_, _, notes, ok := Outbound(Obj{"tag": "dns-out", "protocol": "dns"})
	if ok {
		t.Fatal("the dns outbound must not be produced — sing-box removed it in 1.13")
	}
	if len(notes) == 0 {
		t.Error("it must say what to do instead")
	}
}

// A converted geo reference has to come with a rule set that actually exists,
// or the panel silently drops the rule naming it when it builds a node config.
func TestRoutingReportsItsRuleSets(t *testing.T) {
	_, _, sets := Routing(Obj{"rules": []any{
		Obj{"outboundTag": "block", "domain": []any{"geosite:category-ads-all"}},
		Obj{"outboundTag": "direct", "ip": []any{"geoip:ir"}},
	}}, RouteContext{})
	if len(sets) != 2 {
		t.Fatalf("rule sets = %v, want both the geosite and the geoip list", sets)
	}
	for _, tag := range sets {
		if RuleSetURL(tag) == "" {
			t.Errorf("no mirror URL for %q", tag)
		}
	}
	if RuleSetURL("something-else") != "" {
		t.Error("an unknown tag must have no URL rather than a guessed one")
	}
}
