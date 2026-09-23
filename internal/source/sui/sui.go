// Package sui reads an s-ui installation.
//
// s-ui is the closest thing to a sibling Nexora has: it already runs sing-box,
// its `clients` table carries the same columns under the same names, and its
// inbounds, outbounds and endpoints are sing-box documents rather than a
// different engine's. Almost nothing here is a conversion — it is a copy with
// the tags and names checked.
//
// The two places it is not a copy: s-ui keeps credentials per protocol inside
// one client (Nexora keeps one set), and s-ui `services` have no Nexora
// equivalent at all.
package sui

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/convert"
	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
	"github.com/nexora-vpn/nexora-migrate/internal/source"
	"github.com/nexora-vpn/nexora-migrate/internal/source/dbfetch"
	"github.com/nexora-vpn/nexora-migrate/internal/sqlitex"
)

func init() {
	source.Register(source.Descriptor{
		ID:               "s-ui",
		Label:            "s-ui",
		Channels:         []source.Channel{source.ChannelFile, source.ChannelAPI},
		DefaultPath:      "/usr/local/s-ui/db/s-ui.db",
		Hint:             "Copy s-ui.db off the server and pick it here, or let the wizard fetch it: s-ui can hand over its own database over its API. s-ui already runs sing-box, so almost everything transfers unchanged.",
		Auth:             []source.AuthMode{source.AuthLogin, source.AuthToken},
		APIFetchesBackup: true,
	}, reader{})
}

type reader struct{}

func (reader) Read(ctx context.Context, opt source.Options) (*bundle.Bundle, error) {
	src, err := dbfetch.Source(ctx, dbfetch.SUI, opt)
	if err != nil {
		return nil, err
	}
	// A downloaded panel database is a complete copy of somebody's install,
	// every credential in it. It goes as soon as it has been read.
	defer src.Remove()

	db, err := sqlitex.Open(src.Path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	b := &bundle.Bundle{Source: bundle.SourceInfo{Panel: "s-ui", Origin: src.Origin}}
	if src.Note != "" {
		b.Note("%s", src.Note)
	}
	settings := readSettings(db)
	b.Source.Version = settings["version"]

	tls := readTLS(db)
	tags := normalize.NewNamer()
	tags.Reserve(convert.BuiltinDirect)

	inboundItem := readInbounds(b, db, tls, tags)
	readOutbounds(b, db, tags)
	readEndpoints(b, db, tags)
	readServices(b, db)
	tplItem := readBaseConfig(b, settings, inboundItem)
	readAdmins(b, db)
	readClients(b, db, settings, inboundItem, tplItem)

	return b, nil
}

// readSettings loads the key/value table s-ui keeps everything global in.
func readSettings(db *sqlitex.DB) map[string]string {
	out := map[string]string{}
	rows, err := db.Select("settings", []string{"key", "value"}, "id")
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[r.Str("key")] = r.Str("value")
	}
	return out
}

// readTLS maps tls.id → the server-side TLS object, which an inbound points at
// by foreign key rather than carrying inline.
func readTLS(db *sqlitex.DB) map[int64]json.RawMessage {
	out := map[int64]json.RawMessage{}
	rows, err := db.Select("tls", []string{"id", "name", "server"}, "id")
	if err != nil {
		return out
	}
	for _, r := range rows {
		if raw := r.JSON("server"); raw != nil {
			out[r.Int("id")] = raw
		}
	}
	return out
}

// readInbounds converts every inbound and returns source id → bundle item id,
// which is how clients and the template find them again.
func readInbounds(b *bundle.Bundle, db *sqlitex.DB, tls map[int64]json.RawMessage, tags *normalize.Namer) map[int64]string {
	index := map[int64]string{}
	rows, err := db.Select("inbounds", []string{"id", "type", "tag", "tls_id", "addrs", "out_json", "options"}, "id")
	if err != nil {
		b.Note("no inbounds were read: %v", err)
		return index
	}
	for _, r := range rows {
		id := r.Int("id")
		typ := r.Str("type")
		res := tags.Tag(r.Str("tag"), fmt.Sprintf("inbound-%d", id))

		cfg := map[string]any{}
		if raw := r.JSON("options"); raw != nil {
			_ = json.Unmarshal(raw, &cfg)
		}
		cfg["type"] = typ
		cfg["tag"] = res.Name

		it := bundle.Item{
			ID: "inbound:" + strconv.FormatInt(id, 10), Kind: bundle.KindInbound,
			Name: res.Name, SourceName: r.Str("tag"), Group: []string{typ},
		}
		if tlsID := r.Int("tls_id"); tlsID != 0 {
			if server, ok := tls[tlsID]; ok {
				var obj map[string]any
				if json.Unmarshal(server, &obj) == nil && len(obj) > 0 {
					cfg["tls"] = obj
				}
			} else {
				it.AddNote("the TLS profile this inbound pointed at (id %d) is not in the database; it arrives without TLS", tlsID)
			}
		}
		// s-ui stores the credentials on the client, never on the inbound —
		// same as Nexora, which injects them per node.
		delete(cfg, "users")

		payload := map[string]any{
			"tag": res.Name, "type": typ, "config": cfg,
		}
		if out := r.JSON("out_json"); out != nil && !isEmptyJSON(out) {
			// s-ui's out_json is a client-side outbound override, which is
			// exactly what Nexora calls clientConfig.
			payload["clientConfig"] = out
		}
		raw, _ := json.Marshal(payload)
		it.Payload = raw

		it.Set("type", typ)
		if p, ok := cfg["listen_port"]; ok {
			it.Set("port", fmt.Sprint(p))
		}
		if _, ok := cfg["tls"]; ok {
			it.Set("tls", "yes")
		}
		if addrs := r.JSON("addrs"); addrs != nil && !isEmptyJSON(addrs) {
			it.AddNote("this inbound advertised its own address list; Nexora advertises a node's address instead, so check the generated links")
		}
		if res.Changed {
			it.AddNote("%s", res.Note)
		}
		b.Add(it)
		index[id] = it.ID
	}
	return index
}

func readOutbounds(b *bundle.Bundle, db *sqlitex.DB, tags *normalize.Namer) {
	rows, err := db.Select("outbounds", []string{"id", "type", "tag", "options"}, "id")
	if err != nil {
		return
	}
	for _, r := range rows {
		id, typ := r.Int("id"), r.Str("type")
		cfg := map[string]any{}
		if raw := r.JSON("options"); raw != nil {
			_ = json.Unmarshal(raw, &cfg)
		}
		if convert.IsBuiltinDirect(r.Str("tag"), typ, cfg) {
			// Routes name it by tag, and the tag is the same one Nexora's.
			b.Note("the old panel's plain %q outbound is the one Nexora already puts on every node, so it was not created again; rules that named it use Nexora's", r.Str("tag"))
			continue
		}
		res := tags.Tag(r.Str("tag"), fmt.Sprintf("outbound-%d", id))
		cfg["type"], cfg["tag"] = typ, res.Name

		payload, _ := json.Marshal(map[string]any{"tag": res.Name, "type": typ, "config": cfg})
		it := bundle.Item{
			ID: "outbound:" + strconv.FormatInt(id, 10), Kind: bundle.KindOutbound,
			Name: res.Name, SourceName: r.Str("tag"), Group: []string{typ}, Payload: payload,
		}
		it.Set("type", typ)
		if res.Changed {
			it.AddNote("%s", res.Note)
		}
		b.Add(it)
	}
}

func readEndpoints(b *bundle.Bundle, db *sqlitex.DB, tags *normalize.Namer) {
	rows, err := db.Select("endpoints", []string{"id", "type", "tag", "options", "ext"}, "id")
	if err != nil {
		return
	}
	for _, r := range rows {
		id, typ := r.Int("id"), r.Str("type")
		// s-ui calls a WARP endpoint "warp"; sing-box only knows wireguard.
		outType := typ
		if typ == "warp" {
			outType = "wireguard"
		}
		res := tags.Tag(r.Str("tag"), fmt.Sprintf("endpoint-%d", id))
		cfg := map[string]any{}
		if raw := r.JSON("options"); raw != nil {
			_ = json.Unmarshal(raw, &cfg)
		}
		cfg["type"], cfg["tag"] = outType, res.Name

		payload, _ := json.Marshal(map[string]any{"tag": res.Name, "type": outType, "config": cfg})
		it := bundle.Item{
			ID: "endpoint:" + strconv.FormatInt(id, 10), Kind: bundle.KindEndpoint,
			Name: res.Name, SourceName: r.Str("tag"), Group: []string{outType}, Payload: payload,
		}
		it.Set("type", outType)
		if typ == "warp" {
			it.AddNote("s-ui's WARP endpoint becomes a plain wireguard endpoint; Nexora creates WARP profiles itself, so consider rebuilding it there")
		}
		if res.Changed {
			it.AddNote("%s", res.Note)
		}
		b.Add(it)
	}
}

// readServices records s-ui's services as blocked items rather than hiding
// them: an operator who runs ssm-api or derp needs to know it did not come.
func readServices(b *bundle.Bundle, db *sqlitex.DB) {
	if !db.HasTable("services") {
		return
	}
	rows, err := db.Select("services", []string{"id", "type", "tag"}, "id")
	if err != nil {
		return
	}
	for _, r := range rows {
		it := bundle.Item{
			ID: "service:" + r.Str("id"), Kind: bundle.KindEndpoint,
			Name: r.Str("tag"), SourceName: r.Str("tag"), Group: []string{"services (not supported)"},
		}
		it.Set("type", r.Str("type"))
		it.Block("Nexora has no services pool; a %q service has to be rebuilt by hand if it is needed", r.Str("type"))
		b.Add(it)
	}
}

// readBaseConfig turns s-ui's single `config` setting — log, dns, route and
// experimental in one document — into a Nexora template, which is where those
// blocks live. The template also carries the inbound selection, so applying it
// reproduces what the s-ui node was running.
func readBaseConfig(b *bundle.Bundle, settings map[string]string, inbounds map[int64]string) string {
	raw := settings["config"]
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var cfg map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		b.Note("the s-ui base config could not be parsed, so no routing/DNS template was built")
		return ""
	}

	core := map[string]json.RawMessage{}
	for _, key := range []string{"dns", "log", "ntp"} {
		if v, ok := cfg[key]; ok && !isEmptyJSON(v) {
			core[key] = v
		}
	}
	payload := map[string]any{
		"name":   "s-ui",
		"remark": "routing, DNS and core settings carried over from s-ui",
	}
	if v, ok := cfg["route"]; ok && !isEmptyJSON(v) {
		payload["route"] = v
	}
	if len(core) > 0 {
		payload["core"] = core
	}
	payload["inboundIds"] = []uint{}

	body, _ := json.Marshal(payload)
	it := bundle.Item{
		ID: "template:s-ui", Kind: bundle.KindTemplate, Name: "s-ui", SourceName: "s-ui base config",
		Payload: body,
	}
	if len(inbounds) > 0 {
		ids := make([]string, 0, len(inbounds))
		for _, v := range inbounds {
			ids = append(ids, v)
		}
		it.IDRefs = map[string][]string{"inboundIds": ids}
		it.Set("inbounds", strconv.Itoa(len(ids)))
	}
	if _, ok := cfg["experimental"]; ok {
		it.AddNote("s-ui's `experimental` block was dropped — Nexora manages Clash API and cache itself")
	}
	it.Set("has routing", yesNo(payload["route"] != nil))
	it.Set("has dns", yesNo(core["dns"] != nil))
	b.Add(it)
	return it.ID
}

// readAdmins carries s-ui's panel logins across as Nexora admins. Password
// hashes cannot travel between panels, so each arrives with a generated one.
func readAdmins(b *bundle.Bundle, db *sqlitex.DB) {
	rows, err := db.Select("users", []string{"id", "username"}, "id")
	if err != nil {
		return
	}
	for _, r := range rows {
		username := r.Str("username")
		if username == "" {
			continue
		}
		pw := convert.RandomPassword()
		payload, _ := json.Marshal(map[string]any{
			"username": username, "password": pw, "role": "admin",
		})
		it := bundle.Item{
			ID: "admin:" + r.Str("id"), Kind: bundle.KindAdmin,
			Name: username, SourceName: username, Payload: payload,
		}
		it.Set("role", "admin")
		it.Set("new password", pw)
		it.AddNote("password hashes cannot move between panels: this admin arrives with the generated password shown here — write it down, it is not stored")
		b.Add(it)
	}
}

func readClients(b *bundle.Bundle, db *sqlitex.DB, settings map[string]string, inbounds map[int64]string, tplItem string) {
	rows, err := db.Select("clients", []string{
		"id", "enable", "name", "config", "inbounds", "volume", "expiry", "down", "up",
		"desc", "group", "remark", "created_at", "online_at", "delay_start",
		"auto_reset", "reset_days", "next_reset", "total_up", "total_down",
	}, "id")
	if err != nil {
		b.Note("no clients were read: %v", err)
		return
	}
	names := normalize.NewNamer()
	subBase := subscriptionBase(settings)

	for _, r := range rows {
		id := r.Str("id")
		c := convert.Client{
			SourceID: id,
			Name:     r.Str("name"),
			Enable:   r.Bool("enable"),
			Volume:   r.Int("volume"),
			Expiry:   r.Int("expiry"),
			Up:       r.Int("up"),
			Down:     r.Int("down"),
			Desc:     r.Str("desc"),
			Group:    r.Str("group"),
			Remark:   r.Str("remark"),
			OnlineAt: r.Int("online_at"),

			AutoReset: r.Bool("auto_reset"),
			ResetDays: int(r.Int("reset_days")),
		}
		// In s-ui the client name is the subscription id, so carrying the name
		// carries the link.
		c.SubID = c.Name
		if subBase != "" {
			c.SubURL = subBase + c.Name
		}
		// delay_start is s-ui's start-on-first-use: expiry holds the length in
		// seconds until the first connection moves it.
		if r.Bool("delay_start") && c.Expiry > 0 {
			c.Duration = c.Expiry
			c.Expiry = 0
		}

		var conflicts []string
		c.UUID, c.Password, c.Flow, c.Method, conflicts = credentials(r.JSON("config"))
		c.Conflicts = conflicts

		if tplItem != "" {
			if ids := inboundIDs(r.JSON("inbounds")); len(ids) > 0 {
				// s-ui scopes a client to a set of inbounds; Nexora scopes to
				// templates, and this bundle has exactly one.
				c.TemplateItems = []string{tplItem}
			}
		}

		it := convert.ClientItem(c, names)
		if len(c.UUID+c.Password) == 0 {
			it.Block("this client has no readable credentials in the s-ui database")
		}
		b.Add(it)
	}
}

// credentials collapses s-ui's per-protocol credential map into the one set
// Nexora keeps, and names every protocol whose secret had to be discarded.
func credentials(raw json.RawMessage) (uuid, password, flow, method string, conflicts []string) {
	if raw == nil {
		return
	}
	var byProto map[string]map[string]any
	if json.Unmarshal(raw, &byProto) != nil {
		return
	}
	// Preference order: the protocols an operator is most likely to be selling
	// come first, so the account they actually use keeps working.
	for _, proto := range []string{"vless", "vmess", "tuic", "anytls", "hysteria2", "hysteria"} {
		cfg, ok := byProto[proto]
		if !ok {
			continue
		}
		if v := strOf(cfg["uuid"]); v != "" {
			if uuid == "" {
				uuid = v
				if f := strOf(cfg["flow"]); f != "" && flow == "" {
					flow = f
				}
			} else if v != uuid {
				conflicts = append(conflicts, proto)
			}
		}
	}
	for _, proto := range []string{"trojan", "shadowsocks", "naive", "socks", "mixed", "http", "shadowtls"} {
		cfg, ok := byProto[proto]
		if !ok {
			continue
		}
		v := strOf(cfg["password"])
		if v == "" {
			continue
		}
		if password == "" {
			password = v
			if m := strOf(cfg["method"]); m != "" {
				method = m
			}
		} else if v != password {
			conflicts = append(conflicts, proto)
		}
	}
	// A protocol keyed differently still has a secret worth keeping.
	if uuid == "" && password == "" {
		for _, cfg := range byProto {
			if v := strOf(cfg["uuid"]); v != "" {
				uuid = v
				break
			}
			if v := strOf(cfg["password"]); v != "" {
				password = v
				break
			}
		}
	}
	return
}

// subscriptionBase reconstructs the old subscription address so the review
// table can show an operator the link their customers already hold.
func subscriptionBase(settings map[string]string) string {
	if uri := strings.TrimSpace(settings["subURI"]); uri != "" {
		return strings.TrimRight(uri, "/") + "/"
	}
	domain := strings.TrimSpace(settings["subDomain"])
	if domain == "" {
		return ""
	}
	path := settings["subPath"]
	if path == "" {
		path = "/sub/"
	}
	port := settings["subPort"]
	host := domain
	if port != "" && port != "443" {
		host += ":" + port
	}
	return "https://" + host + path
}

func inboundIDs(raw json.RawMessage) []int64 {
	if raw == nil {
		return nil
	}
	var ids []int64
	_ = json.Unmarshal(raw, &ids)
	return ids
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == "{}" || s == "[]"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
