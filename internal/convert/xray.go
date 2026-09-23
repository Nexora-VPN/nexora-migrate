// Package convert turns another engine's configuration into the sing-box shapes
// Nexora stores.
//
// The panels split in two. s-ui already runs sing-box, so its inbounds,
// outbounds, endpoints, routing and DNS are carried across almost verbatim.
// 3x-ui, Marzban, Marzneshin and PasarGuard run Xray, and everything they hold
// has to be translated — a different vocabulary for the same ideas.
//
// The translation is deliberately partial and says so. Anything that does not
// have an honest sing-box equivalent is dropped with a note on the item rather
// than guessed at: an inbound that looks right and does not work costs an
// operator more than one that arrives marked "check this".
package convert

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Obj is a decoded JSON object. Xray stores most of its configuration as
// strings containing JSON, so readers hand those here as already-decoded maps.
type Obj = map[string]any

// XrayInbound is one converted inbound: the sing-box type and config Nexora
// will store, plus whatever had to be said about it.
type XrayInbound struct {
	Type   string
	Config Obj
	Notes  []string
	// Blocked is set when there is no sing-box equivalent at all.
	Blocked string
	// Clients are the per-user credentials found inside the inbound, which is
	// where Xray keeps them. Nexora keeps users separately, so these are
	// collected and merged into the client list instead.
	Clients []XrayClient
}

// XrayClient is one entry of an Xray inbound's settings.clients array.
type XrayClient struct {
	Email    string
	UUID     string
	Password string
	Flow     string
	Method   string
	SubID    string
	// LimitIP and TotalGB are 3x-ui's per-client limits, which Nexora has as
	// real columns.
	LimitIP    int
	TotalBytes int64
	ExpiryMS   int64
	Enable     *bool
	Reset      int
	// 3x-ui v3.7: ResetDay turns Reset from an every-N-days interval into a
	// renewal on that day of the month, and TrafficReset/TrafficResetDay are a
	// per-client traffic cycle of their own (never/hourly/daily/weekly/monthly).
	ResetDay        int
	TrafficReset    string
	TrafficResetDay int
	// What 3x-ui keeps about the *person*: the Telegram id the panel's bot
	// messages them on, and a free-text note. Both were read by nobody before
	// Nexora had somewhere to put them.
	TelegramID string
	Comment    string
}

// ProtocolFor maps an Xray/3x-ui inbound protocol onto a sing-box inbound type.
// The second result is false when the protocol has no inbound equivalent.
func ProtocolFor(protocol string, stream Obj) (string, bool) {
	switch strings.ToLower(protocol) {
	case "vless":
		return "vless", true
	case "vmess":
		return "vmess", true
	case "trojan":
		return "trojan", true
	case "shadowsocks":
		return "shadowsocks", true
	case "socks":
		return "socks", true
	case "http":
		return "http", true
	case "mixed":
		return "mixed", true
	case "tuic":
		return "tuic", true
	case "mtproto":
		return "mtproxy", true
	case "hysteria":
		// 3x-ui has no separate hysteria2 protocol: it is plain "hysteria" with
		// streamSettings.version = 2, and the share link scheme follows the
		// version, not the protocol name.
		if num(stream["version"]) == 2 {
			return "hysteria2", true
		}
		return "hysteria", true
	case "wireguard", "amneziawg":
		// Both are endpoints in sing-box, not inbounds. Handled separately.
		return "", false
	}
	return "", false
}

// Inbound converts one Xray inbound. settings and stream are the decoded
// `settings` and `streamSettings` documents; either may be nil.
func Inbound(protocol string, listen string, port int, settings, stream Obj) XrayInbound {
	out := XrayInbound{Config: Obj{}}

	typ, ok := ProtocolFor(protocol, stream)
	if !ok {
		switch strings.ToLower(protocol) {
		case "wireguard", "amneziawg":
			out.Blocked = "WireGuard is an endpoint in Nexora, not an inbound; it is listed under Endpoints instead"
		case "dokodemo-door", "tunnel":
			out.Blocked = "a port-forwarding inbound has no Nexora equivalent — build it as routing instead"
		default:
			out.Blocked = fmt.Sprintf("sing-box has no inbound of type %q", protocol)
		}
		return out
	}
	out.Type = typ
	out.Config["type"] = typ
	if listen != "" && listen != "0.0.0.0" && listen != "::" {
		out.Config["listen"] = listen
	} else {
		// sing-box defaults an empty listen to 127.0.0.1, which would silently
		// make a public inbound unreachable.
		out.Config["listen"] = "::"
	}
	if port > 0 {
		out.Config["listen_port"] = port
	}

	out.Clients = clientsFrom(protocol, settings)
	applyProtocolSettings(&out, protocol, settings)
	applyStream(&out, stream)
	return out
}

// applyProtocolSettings copies the non-user half of an Xray `settings` object.
func applyProtocolSettings(out *XrayInbound, protocol string, settings Obj) {
	if settings == nil {
		return
	}
	switch out.Type {
	case "shadowsocks":
		method := str(settings["method"])
		if method == "" {
			// 2022 ciphers keep the method per inbound; older 3x-ui keeps it on
			// the first client.
			for _, c := range out.Clients {
				if c.Method != "" {
					method = c.Method
					break
				}
			}
		}
		if method != "" {
			out.Config["method"] = method
		}
		if pw := str(settings["password"]); pw != "" {
			out.Config["password"] = pw
		}
	case "vless":
		// VLESS Encryption: the node reads Xray's own server string, grammar and
		// all, as the inbound's `decryption`, so it moves verbatim and the
		// clients' existing `encryption=` keeps matching. The panel refuses a
		// string the node would not parse, in its own words.
		if d := strings.TrimSpace(str(settings["decryption"])); d != "" && d != "none" {
			out.Config["decryption"] = d
			out.Notes = append(out.Notes,
				"VLESS Encryption came across with its keys, so existing clients keep working; Nexora serves no XTLS Vision flow under it, so any vision flow is dropped for this inbound")
		}
	case "tuic", "hysteria", "hysteria2":
		if v := settings["congestion_control"]; v != nil {
			out.Config["congestion_control"] = v
		}
	}

	// Fallbacks are Xray's; Nexora expresses the same idea as an ingress
	// router, which is a different object and cannot be derived from these.
	if fb, ok := settings["fallbacks"].([]any); ok && len(fb) > 0 {
		out.Notes = append(out.Notes,
			fmt.Sprintf("%d fallback target(s) were dropped — Nexora routes this with an ingress inbound, which has to be built by hand", len(fb)))
	}
}

// clientsFrom pulls the per-user credentials out of an Xray inbound.
func clientsFrom(protocol string, settings Obj) []XrayClient {
	if settings == nil {
		return nil
	}
	raw, _ := settings["clients"].([]any)
	out := make([]XrayClient, 0, len(raw))
	for _, entry := range raw {
		c, ok := entry.(Obj)
		if !ok {
			continue
		}
		cl := XrayClient{
			Email:    str(c["email"]),
			UUID:     str(c["id"]),
			Password: firstNonEmpty(str(c["password"]), str(c["auth"])),
			Flow:     str(c["flow"]),
			Method:   str(c["method"]),
			SubID:    str(c["subId"]),
			LimitIP:  int(num(c["limitIp"])),
			ExpiryMS: int64(num(c["expiryTime"])),
			Reset:    int(num(c["reset"])),
			ResetDay: int(num(c["resetDay"])),
			// Written only when set: a plain "never" says nothing.
			TrafficReset:    str(c["trafficReset"]),
			TrafficResetDay: int(num(c["trafficResetDay"])),
			// tgId is a string in some forks and a number in others.
			TelegramID: numericOrString(c["tgId"]),
			Comment:    str(c["comment"]),
		}
		cl.TotalBytes = int64(num(c["totalGB"])) // 3x-ui stores bytes despite the name
		if v, ok := c["enable"].(bool); ok {
			cl.Enable = &v
		}
		// Trojan and Shadowsocks keep the secret in `password`; VLESS/VMess in
		// `id`. A few forks write both.
		if cl.UUID == "" && (protocol == "vless" || protocol == "vmess") {
			cl.UUID = cl.Password
		}
		out = append(out, cl)
	}
	return out
}

// applyStream converts streamSettings: transport plus TLS or REALITY.
func applyStream(out *XrayInbound, stream Obj) {
	if stream == nil {
		return
	}
	network := strings.ToLower(str(stream["network"]))
	if network == "" {
		network = strings.ToLower(str(stream["transport"]))
	}

	switch network {
	case "", "tcp", "raw":
		if hdr := objOf(objOf(stream, "tcpSettings"), "header"); hdr != nil {
			if t := strings.ToLower(str(hdr["type"])); t != "" && t != "none" {
				out.Notes = append(out.Notes,
					fmt.Sprintf("the %q header obfuscation on the TCP transport was dropped — sing-box has no equivalent", t))
			}
		}
	case "ws", "websocket":
		t := Obj{"type": "ws"}
		ws := objOf(stream, "wsSettings")
		if p := str(ws["path"]); p != "" {
			t["path"] = p
		}
		if h := headersOf(ws["headers"]); len(h) > 0 {
			t["headers"] = h
		}
		if n := num(ws["maxEarlyData"]); n > 0 {
			t["max_early_data"] = int(n)
		}
		if h := str(ws["earlyDataHeaderName"]); h != "" {
			t["early_data_header_name"] = h
		}
		out.Config["transport"] = t
	case "grpc":
		t := Obj{"type": "grpc"}
		g := objOf(stream, "grpcSettings")
		if s := str(g["serviceName"]); s != "" {
			t["service_name"] = s
		}
		out.Config["transport"] = t
	case "http", "h2":
		t := Obj{"type": "http"}
		h := objOf(stream, "httpSettings")
		if hosts := stringsOf(h["host"]); len(hosts) > 0 {
			t["host"] = hosts
		}
		if p := str(h["path"]); p != "" {
			t["path"] = p
		}
		out.Config["transport"] = t
	case "httpupgrade":
		t := Obj{"type": "httpupgrade"}
		h := objOf(stream, "httpupgradeSettings")
		if p := str(h["path"]); p != "" {
			t["path"] = p
		}
		if host := str(h["host"]); host != "" {
			t["host"] = host
		}
		if hh := headersOf(h["headers"]); len(hh) > 0 {
			t["headers"] = hh
		}
		out.Config["transport"] = t
	case "xhttp", "splithttp":
		// Nexora's node carries xhttp in its overlay, so this one survives.
		t := Obj{"type": "xhttp"}
		x := objOf(stream, "xhttpSettings")
		if x == nil {
			x = objOf(stream, "splithttpSettings")
		}
		if p := str(x["path"]); p != "" {
			t["path"] = p
		}
		if host := str(x["host"]); host != "" {
			t["host"] = host
		}
		mode := str(x["mode"])
		if mode == "" {
			mode = "auto"
		}
		t["mode"] = mode
		if hh := headersOf(x["headers"]); len(hh) > 0 {
			t["headers"] = hh
		}
		xhttpExtras(t, x, objOf(stream, "sockopt"))
		out.Config["transport"] = t
	case "kcp", "mkcp":
		// mkcp is the node's other overlay transport.
		t := Obj{"type": "mkcp"}
		k := objOf(stream, "kcpSettings")
		copyNum(t, "mtu", k["mtu"])
		copyNum(t, "tti", k["tti"])
		copyNum(t, "uplink_capacity", k["uplinkCapacity"])
		copyNum(t, "downlink_capacity", k["downlinkCapacity"])
		copyNum(t, "read_buffer_size", k["readBufferSize"])
		copyNum(t, "write_buffer_size", k["writeBufferSize"])
		if b, ok := k["congestion"].(bool); ok && b {
			t["congestion"] = true
		}
		if s := str(k["seed"]); s != "" {
			t["seed"] = s
		}
		if hdr := objOf(k, "header"); hdr != nil {
			if ht := str(hdr["type"]); ht != "" && ht != "none" {
				t["header_type"] = ht
			}
		}
		out.Config["transport"] = t
	case "quic":
		out.Notes = append(out.Notes,
			"the QUIC transport was dropped — sing-box removed it; use TUIC or Hysteria2 instead")
	default:
		out.Notes = append(out.Notes, fmt.Sprintf("unknown transport %q was dropped", network))
	}

	applyTLS(out, stream)
}

// applyTLS converts tlsSettings / realitySettings.
func applyTLS(out *XrayInbound, stream Obj) {
	security := strings.ToLower(str(stream["security"]))
	if security == "" || security == "none" {
		return
	}
	tls := Obj{"enabled": true}

	switch security {
	case "tls":
		t := objOf(stream, "tlsSettings")
		if sn := str(t["serverName"]); sn != "" {
			tls["server_name"] = sn
		}
		if alpn := stringsOf(t["alpn"]); len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		if v := str(t["minVersion"]); v != "" {
			tls["min_version"] = v
		}
		if v := str(t["maxVersion"]); v != "" {
			tls["max_version"] = v
		}
		applyCertificates(out, tls, t)
	case "reality":
		r := objOf(stream, "realitySettings")
		names := stringsOf(r["serverNames"])
		if len(names) > 0 {
			tls["server_name"] = names[0]
			if len(names) > 1 {
				out.Notes = append(out.Notes,
					fmt.Sprintf("REALITY had %d server names; sing-box takes one, so %q was kept", len(names), names[0]))
			}
		}
		reality := Obj{"enabled": true}
		if pk := str(r["privateKey"]); pk != "" {
			reality["private_key"] = pk
		}
		if ids := stringsOf(r["shortIds"]); len(ids) > 0 {
			reality["short_id"] = ids
		} else if id := str(r["shortId"]); id != "" {
			reality["short_id"] = []string{id}
		}
		// Xray's `dest` is "host:port" (or just a port); sing-box wants the two
		// apart, under `handshake`.
		if host, port := splitDest(str(r["dest"]), names); host != "" {
			reality["handshake"] = Obj{"server": host, "server_port": port}
		}
		if n := num(r["maxTimeDiff"]); n > 0 {
			reality["max_time_difference"] = fmt.Sprintf("%dms", int64(n))
		}
		tls["reality"] = reality
		if str(r["publicKey"]) == "" && str(r["privateKey"]) == "" {
			out.Notes = append(out.Notes,
				"this REALITY inbound carried no private key, so Nexora generates a new one when it is created — clients of this inbound need their new link")
		}
	default:
		out.Notes = append(out.Notes, fmt.Sprintf("unknown stream security %q was dropped", security))
		return
	}
	out.Config["tls"] = tls
}

// applyCertificates maps Xray's certificates[] onto sing-box's flat pair.
func applyCertificates(out *XrayInbound, tls, t Obj) {
	certs, _ := t["certificates"].([]any)
	for _, entry := range certs {
		c, ok := entry.(Obj)
		if !ok {
			continue
		}
		if f := str(c["certificateFile"]); f != "" {
			tls["certificate_path"] = f
			if k := str(c["keyFile"]); k != "" {
				tls["key_path"] = k
			}
			out.Notes = append(out.Notes,
				fmt.Sprintf("the certificate is a path on the old server (%s) — the file has to exist on the Nexora node too, or assign a panel certificate instead", f))
			return
		}
		if lines := stringsOf(c["certificate"]); len(lines) > 0 {
			tls["certificate"] = lines
			if key := stringsOf(c["key"]); len(key) > 0 {
				tls["key"] = key
			}
			return
		}
	}
	if len(certs) == 0 {
		out.Notes = append(out.Notes,
			"TLS is on but no certificate came across — assign a Nexora panel certificate or the node's own before enabling this inbound")
	}
}

// splitDest turns REALITY's "example.com:443" into its two halves, falling back
// to the first server name and 443 when the dest is only a port.
func splitDest(dest string, names []string) (string, int) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		if len(names) > 0 {
			return names[0], 443
		}
		return "", 0
	}
	host, portStr, found := strings.Cut(dest, ":")
	if !found {
		// A bare port: "443".
		if p, err := strconv.Atoi(dest); err == nil {
			if len(names) > 0 {
				return names[0], p
			}
			return "", 0
		}
		return dest, 443
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port == 0 {
		port = 443
	}
	if host == "" && len(names) > 0 {
		host = names[0]
	}
	return host, port
}

// Outbound converts one Xray outbound into a sing-box outbound. Not everything
// survives; the second result says what to tell the operator, and ok is false
// when there is no equivalent at all.
func Outbound(o Obj) (typ string, config Obj, notes []string, ok bool) {
	protocol := strings.ToLower(str(o["protocol"]))
	settings := objOf(o, "settings")
	switch protocol {
	case "freedom", "direct":
		return "direct", Obj{"type": "direct"}, nil, true
	case "blackhole", "block":
		return "block", Obj{"type": "block"}, nil, true
	case "dns":
		// sing-box removed the `dns` outbound in 1.13; the same job is a route
		// rule with action "hijack-dns", which is not derivable from this
		// object, so it is reported rather than produced.
		return "", nil, []string{
			"the `dns` outbound was removed in sing-box 1.13 — add a route rule with action \"hijack-dns\" instead",
		}, false
	case "socks":
		cfg := Obj{"type": "socks"}
		if srv := firstServer(settings); srv != nil {
			cfg["server"] = str(srv["address"])
			cfg["server_port"] = int(num(srv["port"]))
			if users, _ := srv["users"].([]any); len(users) > 0 {
				if u, ok := users[0].(Obj); ok {
					cfg["username"] = str(u["user"])
					cfg["password"] = str(u["pass"])
				}
			}
		}
		return "socks", cfg, nil, true
	case "http":
		cfg := Obj{"type": "http"}
		if srv := firstServer(settings); srv != nil {
			cfg["server"] = str(srv["address"])
			cfg["server_port"] = int(num(srv["port"]))
		}
		return "http", cfg, nil, true
	case "vless", "vmess", "trojan", "shadowsocks":
		cfg := Obj{"type": protocol}
		if srv := firstServer(settings); srv != nil {
			cfg["server"] = str(srv["address"])
			cfg["server_port"] = int(num(srv["port"]))
			if users, _ := srv["users"].([]any); len(users) > 0 {
				if u, ok := users[0].(Obj); ok {
					if id := str(u["id"]); id != "" {
						cfg["uuid"] = id
					}
					if f := str(u["flow"]); f != "" {
						cfg["flow"] = f
					}
				}
			}
			if pw := str(srv["password"]); pw != "" {
				cfg["password"] = pw
			}
			if m := str(srv["method"]); m != "" {
				cfg["method"] = m
			}
		}
		// The chained outbound's own stream settings ride along.
		stub := XrayInbound{Config: cfg}
		applyStream(&stub, objOf(o, "streamSettings"))
		delete(cfg, "listen")
		return protocol, cfg, stub.Notes, true
	case "wireguard":
		return "", nil, []string{"WireGuard egress is an endpoint in Nexora, not an outbound"}, false
	}
	return "", nil, []string{fmt.Sprintf("sing-box has no outbound of type %q", protocol)}, false
}

func firstServer(settings Obj) Obj {
	if settings == nil {
		return nil
	}
	for _, key := range []string{"servers", "vnext"} {
		if arr, _ := settings[key].([]any); len(arr) > 0 {
			if o, ok := arr[0].(Obj); ok {
				return o
			}
		}
	}
	// Xray's flat form, which 3x-ui and alireza0's x-ui write for VLESS and
	// VMess: the server and its one user directly under settings.
	if str(settings["address"]) != "" {
		srv := Obj{"address": settings["address"], "port": settings["port"],
			"password": settings["password"], "method": settings["method"]}
		if id := str(settings["id"]); id != "" {
			srv["users"] = []any{Obj{"id": id, "flow": settings["flow"]}}
		}
		return srv
	}
	return nil
}

// RouteContext is what the reader knows and the converter cannot work out for
// itself: which outbound tags were converted to a `block` outbound (so the rules
// naming them become sing-box's reject action rather than a dangling
// reference), and how tags were renamed when they had to be normalised.
//
// Without this a rule keeps pointing at a tag that no longer exists, which a
// node refuses its whole configuration over.
type RouteContext struct {
	Blocked map[string]bool
	Renamed map[string]string
}

func (rc RouteContext) tag(name string) string {
	if to, ok := rc.Renamed[name]; ok && to != "" {
		return to
	}
	return name
}

// Routing converts an Xray routing block into a sing-box route object. Xray
// rule fields that sing-box spells differently are renamed; the ones it has no
// concept of are dropped, and every drop is reported.
func Routing(routing Obj, rc RouteContext) (route Obj, notes []string, ruleSets []string) {
	route = Obj{}
	if routing == nil {
		return route, nil, nil
	}
	rules, _ := routing["rules"].([]any)
	var out []Obj
	dropped := map[string]int{}

	for _, entry := range rules {
		r, ok := entry.(Obj)
		if !ok {
			continue
		}
		nr := Obj{}
		if v := stringsOf(r["domain"]); len(v) > 0 {
			assignDomains(nr, v)
		}
		if v := stringsOf(r["ip"]); len(v) > 0 {
			assignIPs(nr, v)
		}
		if v := stringsOf(r["source"]); len(v) > 0 {
			nr["source_ip_cidr"] = v
		}
		if v := portList(r["port"]); len(v) > 0 {
			nr["port"] = v
		}
		if v := portList(r["sourcePort"]); len(v) > 0 {
			nr["source_port"] = v
		}
		if v := stringsOf(r["inboundTag"]); len(v) > 0 {
			renamed := make([]string, 0, len(v))
			for _, t := range v {
				renamed = append(renamed, rc.tag(t))
			}
			nr["inbound"] = renamed
		}
		if v := stringsOf(r["protocol"]); len(v) > 0 {
			nr["protocol"] = v
		}
		switch strings.ToLower(str(r["network"])) {
		case "tcp":
			nr["network"] = []string{"tcp"}
		case "udp":
			nr["network"] = []string{"udp"}
		}
		for _, unsupported := range []string{"user", "attrs", "balancerTag", "domainMatcher"} {
			if r[unsupported] != nil {
				dropped[unsupported]++
			}
		}
		if len(nr) == 0 {
			continue
		}
		tag := str(r["outboundTag"])
		switch {
		case tag == "":
			continue
		case rc.Blocked[tag], strings.EqualFold(tag, "block"),
			strings.EqualFold(tag, "blocked"), strings.EqualFold(tag, "blackhole"):
			// sing-box rejects with an action, not by routing to a sink.
			nr["action"] = "reject"
		default:
			nr["outbound"] = rc.tag(tag)
		}
		out = append(out, nr)
	}
	if len(out) > 0 {
		route["rules"] = out
	}
	for _, r := range out {
		for _, tag := range stringsIn(r["rule_set"]) {
			ruleSets = appendUnique(ruleSets, tag)
		}
	}
	for field, n := range dropped {
		notes = append(notes, fmt.Sprintf("%d rule(s) used %q, which sing-box has no equivalent for; that condition was dropped", n, field))
	}
	if bal, _ := routing["balancers"].([]any); len(bal) > 0 {
		notes = append(notes,
			fmt.Sprintf("%d load balancer(s) were dropped — build them as a selector or urltest outbound in Nexora", len(bal)))
	}
	return route, notes, ruleSets
}

// RuleSetURL is where a converted geosite:/geoip: reference is mirrored from.
//
// These are SagerNet's own rule-set branches — the files sing-box's own
// documentation points at, and the same URLs the panel's preset catalogue uses,
// so a rule set created here is the one an operator would have picked by hand.
func RuleSetURL(tag string) string {
	const (
		geosite = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"
		geoip   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/"
	)
	switch {
	case strings.HasPrefix(tag, "geosite-"):
		return geosite + tag + ".srs"
	case strings.HasPrefix(tag, "geoip-"):
		return geoip + tag + ".srs"
	}
	return ""
}

// assignDomains splits Xray's one `domain` list into sing-box's four.
func assignDomains(rule Obj, domains []string) {
	var exact, suffix, keyword, regex []string
	for _, d := range domains {
		switch {
		case strings.HasPrefix(d, "full:"):
			exact = append(exact, strings.TrimPrefix(d, "full:"))
		case strings.HasPrefix(d, "domain:"):
			suffix = append(suffix, strings.TrimPrefix(d, "domain:"))
		case strings.HasPrefix(d, "regexp:"):
			regex = append(regex, strings.TrimPrefix(d, "regexp:"))
		case strings.HasPrefix(d, "keyword:"):
			keyword = append(keyword, strings.TrimPrefix(d, "keyword:"))
		case strings.HasPrefix(d, "geosite:"):
			// A geosite list is a rule-set in sing-box; the reader adds those
			// separately, so name it and let the rule point at the tag.
			rule["rule_set"] = appendUnique(stringsIn(rule["rule_set"]), "geosite-"+strings.TrimPrefix(d, "geosite:"))
		case strings.HasPrefix(d, "ext:"):
			// An external list file on the old server: nothing to point at.
		default:
			keyword = append(keyword, d)
		}
	}
	setIf(rule, "domain", exact)
	setIf(rule, "domain_suffix", suffix)
	setIf(rule, "domain_keyword", keyword)
	setIf(rule, "domain_regex", regex)
}

func assignIPs(rule Obj, ips []string) {
	var cidr []string
	for _, ip := range ips {
		switch {
		case strings.HasPrefix(ip, "geoip:"):
			name := strings.TrimPrefix(ip, "geoip:")
			if name == "private" {
				rule["ip_is_private"] = true
				continue
			}
			rule["rule_set"] = appendUnique(stringsIn(rule["rule_set"]), "geoip-"+name)
		case strings.HasPrefix(ip, "ext:"):
		default:
			cidr = append(cidr, ip)
		}
	}
	setIf(rule, "ip_cidr", cidr)
}

// DNS converts an Xray dns block into a sing-box dns object.
//
// sing-box 1.14 removed the legacy `{"address": "..."}` server, so every server
// here is emitted in the typed form. Xray writes its servers as one address
// string with the transport folded into a scheme, which is exactly the
// information the typed form wants — it just wants it in two fields.
func DNS(dns Obj) (out Obj, notes []string) {
	if dns == nil {
		return nil, nil
	}
	servers, _ := dns["servers"].([]any)
	var list []Obj
	for i, entry := range servers {
		var address string
		var extra Obj
		switch v := entry.(type) {
		case string:
			address = v
		case Obj:
			address = str(v["address"])
			extra = v
		}
		srv, why := dnsServer(address)
		if srv == nil {
			if why != "" {
				notes = append(notes, why)
			}
			continue
		}
		srv["tag"] = fmt.Sprintf("dns-%d", i+1)
		list = append(list, srv)
		if extra != nil {
			if d := stringsOf(extra["domains"]); len(d) > 0 {
				notes = append(notes,
					"per-server domain lists were dropped — sing-box expresses them as dns.rules, which have to be written by hand")
			}
		}
	}
	if len(list) == 0 {
		return nil, notes
	}
	return Obj{"servers": list}, notes
}

// dnsServer turns one Xray address string into a typed sing-box server. The
// second result explains anything that had no equivalent.
func dnsServer(address string) (Obj, string) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, ""
	}
	// Xray's "+local" suffix means "resolve this server's own name locally",
	// which sing-box expresses with domain_resolver rather than on the server.
	scheme, rest, hasScheme := strings.Cut(address, "://")
	scheme = strings.ToLower(strings.TrimSuffix(scheme, "+local"))
	if !hasScheme {
		switch strings.ToLower(address) {
		case "localhost", "local":
			return Obj{"type": "local"}, ""
		case "fakedns":
			return nil, "the fakedns server was dropped — sing-box's equivalent is a server of type \"fakeip\", which needs address ranges this config does not carry"
		}
		// sing-box wants the port in its own field; an address of "1.1.1.1:5353"
		// in `server` is refused.
		host, port := splitHostPort(address)
		srv := Obj{"type": "udp", "server": host}
		if port > 0 {
			srv["server_port"] = port
		}
		return srv, ""
	}

	host, path, _ := strings.Cut(rest, "/")
	host = strings.TrimSuffix(host, "/")
	server, port := splitHostPort(host)

	srv := Obj{"server": server}
	if port > 0 {
		srv["server_port"] = port
	}
	switch scheme {
	case "udp", "dns":
		srv["type"] = "udp"
	case "tcp":
		srv["type"] = "tcp"
	case "tls":
		srv["type"] = "tls"
	case "quic":
		srv["type"] = "quic"
	case "https", "h2c":
		srv["type"] = "https"
		if path != "" {
			srv["path"] = "/" + strings.TrimPrefix(path, "/")
		}
	case "h3":
		srv["type"] = "h3"
		if path != "" {
			srv["path"] = "/" + strings.TrimPrefix(path, "/")
		}
	case "localhost", "local":
		return Obj{"type": "local"}, ""
	case "dhcp":
		return Obj{"type": "dhcp"}, ""
	default:
		return nil, fmt.Sprintf("the DNS server %q was dropped — sing-box has no server of that kind", address)
	}
	return srv, ""
}

func splitHostPort(host string) (string, int) {
	// An IPv6 literal is bracketed; anything else with one colon has a port.
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end > 0 {
			addr := host[1:end]
			if rest := host[end+1:]; strings.HasPrefix(rest, ":") {
				if p, err := strconv.Atoi(rest[1:]); err == nil {
					return addr, p
				}
			}
			return addr, 0
		}
	}
	if strings.Count(host, ":") == 1 {
		h, portStr, _ := strings.Cut(host, ":")
		if p, err := strconv.Atoi(portStr); err == nil {
			return h, p
		}
	}
	return host, 0
}

// ---- small helpers -------------------------------------------------------

// firstNonEmpty returns the first value that is not empty. The classic x-ui
// line keeps a Hysteria client's secret under `auth` rather than `password`.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case json.Number:
		return s.String()
	case bool:
		return strconv.FormatBool(s)
	}
	return ""
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f
	}
	return 0
}

func objOf(parent Obj, key string) Obj {
	if parent == nil {
		return nil
	}
	o, _ := parent[key].(Obj)
	return o
}

func stringsOf(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := str(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}

func stringsIn(v any) []string {
	s, _ := v.([]string)
	return s
}

func appendUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

func setIf(m Obj, key string, v []string) {
	if len(v) > 0 {
		m[key] = v
	}
}

func headersOf(v any) map[string]string {
	o, ok := v.(Obj)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, val := range o {
		if s := str(val); s != "" {
			out[k] = s
		} else if arr := stringsOf(val); len(arr) > 0 {
			out[k] = arr[0]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// portList turns Xray's "80,443,1000-2000" (or a number) into sing-box's
// separate port / port_range spelling, flattened into one list of strings the
// caller stores under `port`. sing-box takes plain numbers in `port` and ranges
// in `port_range`, so ranges are reported rather than silently dropped.
func portList(v any) []int {
	var out []int
	for _, part := range strings.Split(str(v), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			continue // a range: handled by the caller's notes, not expressible here
		}
		if p, err := strconv.Atoi(part); err == nil && p > 0 {
			out = append(out, p)
		}
	}
	return out
}

func copyNum(dst Obj, key string, v any) {
	if n := num(v); n > 0 {
		dst[key] = int(n)
	}
}

// BuiltinDirect is the outbound tag Nexora puts on every node config itself
// (a plain `direct`). An imported outbound under the same tag makes the node
// refuse its whole configuration as a duplicate, so readers reserve the name
// and fold a plain direct outbound into the built-in one.
const BuiltinDirect = "direct"

// IsBuiltinDirect reports whether an outbound is exactly the one Nexora
// already provides: a direct outbound tagged "direct" with no options.
func IsBuiltinDirect(tag, typ string, cfg map[string]any) bool {
	if !strings.EqualFold(tag, BuiltinDirect) || typ != "direct" {
		return false
	}
	for k := range cfg {
		if k != "type" && k != "tag" {
			return false
		}
	}
	return true
}

// XrayOutboundExtras reports whether an Xray outbound carries settings beside
// its protocol ones — a bind address, mux, a dialer proxy — which a reader
// reports as dropped. Such an outbound is not folded into the built-in one,
// so what it had stays listed and explained.
func XrayOutboundExtras(o Obj) bool {
	for _, k := range []string{"sendThrough", "proxySettings"} {
		if str(o[k]) != "" || objOf(o, k) != nil {
			return true
		}
	}
	if m := objOf(o, "mux"); m != nil {
		if on, _ := m["enabled"].(bool); on {
			return true
		}
	}
	return objOf(objOf(o, "streamSettings"), "sockopt") != nil
}

// xhttpExtras copies the rest of an Xray xhttpSettings block onto the node's
// transport, renaming Xray's camelCase to the node's snake_case.
//
// Most of these are not tuning. The padding, session, sequence and uplink
// fields are read by the server out of every request and checked against what
// the client sent, and a client migrated from the old panel still has the old
// panel's values in its link: an inbound that arrived with the defaults would
// answer part of its traffic with errors. The server-only ones (buffers, the
// stream-up window, header size, trusted forwarding) carry over for the same
// reason anything else does. Fields only a client reads — xmux,
// scMinPostsIntervalMs, uplinkChunkSize, download — do nothing on an inbound
// and are not copied.
//
// Xray lets the block nest its fields under `extra`, which then stands in for
// everything but host, path and mode; the same is done here.
func xhttpExtras(t, x, sockopt Obj) {
	if x == nil {
		return
	}
	if extra := objOf(x, "extra"); extra != nil {
		x = extra
	}
	for from, to := range map[string]string{
		"xPaddingBytes":        "x_padding_bytes",
		"scMaxEachPostBytes":   "sc_max_each_post_bytes",
		"scStreamUpServerSecs": "sc_stream_up_server_secs",
	} {
		if r := xrayRange(x[from]); r != "" {
			t[to] = r
		}
	}
	copyNum(t, "sc_max_buffered_posts", x["scMaxBufferedPosts"])
	copyNum(t, "server_max_header_bytes", x["serverMaxHeaderBytes"])
	for from, to := range map[string]string{
		"noGRPCHeader":     "no_grpc_header",
		"noSSEHeader":      "no_sse_header",
		"xPaddingObfsMode": "x_padding_obfs_mode",
	} {
		if b, ok := x[from].(bool); ok && b {
			t[to] = true
		}
	}
	for from, to := range map[string]string{
		"xPaddingKey":         "x_padding_key",
		"xPaddingHeader":      "x_padding_header",
		"xPaddingPlacement":   "x_padding_placement",
		"xPaddingMethod":      "x_padding_method",
		"uplinkHTTPMethod":    "uplink_http_method",
		"sessionPlacement":    "session_placement",
		"sessionKey":          "session_key",
		"seqPlacement":        "seq_placement",
		"seqKey":              "seq_key",
		"uplinkDataPlacement": "uplink_data_placement",
		"uplinkDataKey":       "uplink_data_key",
	} {
		if v := strings.TrimSpace(str(x[from])); v != "" {
			t[to] = v
		}
	}
	// Xray renamed the session pair in v26.6.22; the newer spelling wins.
	if v := strings.TrimSpace(str(x["sessionIDPlacement"])); v != "" {
		t["session_placement"] = v
	}
	if v := strings.TrimSpace(str(x["sessionIDKey"])); v != "" {
		t["session_key"] = v
	}
	// Xray keeps the trusted forwarding headers in sockopt, for every
	// transport; the node reads them on xhttp only, so only here.
	if list := stringsOf(sockopt["trustedXForwardedFor"]); len(list) > 0 {
		t["trusted_x_forwarded_for"] = list
	}
}

// xrayRange renders one of Xray's range values — "100-1000", a bare number,
// or {"from":…,"to":…} — as the "from-to" string the node and every Xray
// client accept. Empty when the value is absent or none of those.
func xrayRange(v any) string {
	switch r := v.(type) {
	case string:
		return strings.TrimSpace(r)
	case float64, json.Number:
		return str(r)
	case Obj:
		from, to := str(r["from"]), str(r["to"])
		if from == "" || to == "" {
			return ""
		}
		if from == to {
			return from
		}
		return from + "-" + to
	}
	return ""
}

// DecodeObj parses a JSON document that may be stored as a string containing
// JSON (which is how Xray panels keep `settings` and `streamSettings`).
func DecodeObj(raw json.RawMessage) Obj {
	if len(raw) == 0 {
		return nil
	}
	var o Obj
	if json.Unmarshal(raw, &o) == nil {
		return o
	}
	// A JSON string holding JSON.
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		if json.Unmarshal([]byte(s), &o) == nil {
			return o
		}
	}
	return nil
}

// DecodeObjString is DecodeObj for a plain string column.
func DecodeObjString(s string) Obj {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return DecodeObj(json.RawMessage(s))
}

// numericOrString reads a field these panels spell either way — 3x-ui's tgId
// is a text box in the UI and a number in the JSON of some forks.
func numericOrString(v any) string {
	if s := str(v); s != "" {
		return s
	}
	if n := num(v); n != 0 {
		return strconv.FormatInt(int64(n), 10)
	}
	return ""
}
