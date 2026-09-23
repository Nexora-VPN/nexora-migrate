// Package xraysql is the machinery shared by the Xray panels that keep
// everything in one SQLite file — 3x-ui and the classic x-ui line (vaxilu's
// original and the forks of it, alireza0's among them).
//
// They descend from the same code, so they agree on the parts that matter here:
// an `inbounds` table whose `settings` JSON carries the clients, a
// `client_traffics` table keyed by the client "email" that is unique
// panel-wide, a `users` table of panel logins, and a `settings` key/value table.
// One person therefore appears once per inbound they are on, and their counters
// live somewhere else entirely — so a Nexora user is built by merging every
// appearance of an email and then joining the counters on.
//
// Where they disagree is what surrounds that: 3x-ui keeps routing, DNS and the
// egress list inside one `xrayTemplateConfig` setting, while the classic line
// gained `outbounds` and `routing_rules` tables of its own. That difference
// stays in the two readers; everything above it lives here.
package xraysql

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/convert"
	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
	"github.com/nexora-vpn/nexora-migrate/internal/sqlitex"
)

// Panel is what one reader tells the shared code about its dialect.
type Panel struct {
	// Name is the panel's display name, used in the notes an operator reads.
	Name string
	// WireGuardClientsArePeers marks a panel that stores WireGuard peers under
	// `settings.clients` and renames them to `peers` when it writes the Xray
	// config. The classic x-ui line always did; 3x-ui moved to it in v3.7 and
	// its migration deleted `peers`. `peers` is still read first, so an older
	// database of either line reads the same.
	WireGuardClientsArePeers bool
}

// Account is one client, merged across every inbound it appears on.
type Account struct {
	Email    string
	UUID     string
	Password string
	Flow     string
	Method   string
	SubID    string
	LimitIP  int
	TotalGB  int64
	ExpiryMS int64
	Reset    int
	ResetDay int
	// TrafficReset/TrafficResetDay are 3x-ui's per-client traffic cycle.
	TrafficReset    string
	TrafficResetDay int
	Enable          *bool
	Conflicts       []string
	Inbounds        []string // bundle item ids
	// The person behind the account: the Telegram id 3x-ui's bot messages, and
	// the operator's note. First seen wins, like the credential merge — the
	// same client on two inbounds is one customer.
	TelegramID string
	Comment    string
	// Peer marks an email that names a WireGuard peer, which arrives on its
	// endpoint. Once WireGuard peers became clients, each got a traffic row
	// too, and without this they would read as accounts with no credentials.
	Peer bool

	firstSeen int
}

// Accounts is the panel-wide merge, keyed by the client email.
type Accounts map[string]*Account

// Merge folds one inbound's clients in, recording a conflict whenever the same
// person holds a different secret on a different inbound — which Nexora, with
// one credential set per user, cannot reproduce.
func (a Accounts) Merge(clients []convert.XrayClient, protocol, inboundItem string) {
	for _, c := range clients {
		key := c.Email
		if key == "" {
			key = c.UUID
		}
		if key == "" {
			continue
		}
		acc, ok := a[key]
		if !ok {
			acc = &Account{Email: key, firstSeen: len(a)}
			a[key] = acc
		}
		acc.Inbounds = append(acc.Inbounds, inboundItem)

		if c.UUID != "" {
			switch {
			case acc.UUID == "":
				acc.UUID = c.UUID
				if c.Flow != "" {
					acc.Flow = c.Flow
				}
			case acc.UUID != c.UUID:
				acc.Conflicts = appendUnique(acc.Conflicts, protocol)
			}
		}
		if c.Password != "" {
			switch {
			case acc.Password == "":
				acc.Password = c.Password
				if c.Method != "" {
					acc.Method = c.Method
				}
			case acc.Password != c.Password:
				acc.Conflicts = appendUnique(acc.Conflicts, protocol)
			}
		}
		if acc.SubID == "" {
			acc.SubID = c.SubID
		}
		if acc.TelegramID == "" {
			acc.TelegramID = c.TelegramID
		}
		if acc.Comment == "" {
			acc.Comment = c.Comment
		}
		if c.LimitIP > acc.LimitIP {
			acc.LimitIP = c.LimitIP
		}
		if c.TotalBytes > acc.TotalGB {
			acc.TotalGB = c.TotalBytes
		}
		if c.ExpiryMS != 0 && acc.ExpiryMS == 0 {
			acc.ExpiryMS = c.ExpiryMS
		}
		if c.Reset > acc.Reset {
			acc.Reset = c.Reset
		}
		if c.ResetDay > 0 && acc.ResetDay == 0 {
			acc.ResetDay = c.ResetDay
		}
		if t := strings.ToLower(strings.TrimSpace(c.TrafficReset)); t != "" && t != "never" && acc.TrafficReset == "" {
			acc.TrafficReset, acc.TrafficResetDay = t, c.TrafficResetDay
		}
		if c.Enable != nil && acc.Enable == nil {
			acc.Enable = c.Enable
		}
	}
}

// Ensure adds an account that has counters but no inbound left — the inbound it
// belonged to was deleted under it. It is still somebody's account.
func (a Accounts) Ensure(email string) {
	if email == "" {
		return
	}
	if _, ok := a[email]; !ok {
		a[email] = &Account{Email: email, firstSeen: len(a)}
	}
}

// MarkPeer records an email as a WireGuard peer's.
func (a Accounts) MarkPeer(email string) {
	if email == "" {
		return
	}
	a.Ensure(email)
	a[email].Peer = true
}

// Ordered returns the accounts in the order they were first seen, so a re-read
// of the same database produces the same bundle.
func (a Accounts) Ordered() []*Account {
	out := make([]*Account, 0, len(a))
	for _, acc := range a {
		out = append(out, acc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].firstSeen < out[j].firstSeen })
	return out
}

// Settings reads the key/value table both panels keep their globals in.
func Settings(db *sqlitex.DB) map[string]string {
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

// Inbounds converts every inbound and collects the clients found inside them,
// returning the bundle ids of the inbounds it produced. A WireGuard inbound
// becomes a Nexora endpoint instead, which is where sing-box keeps it, and its
// id is not in the returned list — a template selects inbounds, not endpoints.
//
// renamed collects source tag → Nexora tag so the routing conversion can follow
// a tag that had to be normalised. A rule pointing at a tag that no longer
// exists is a config a node refuses in full.
func Inbounds(b *bundle.Bundle, db *sqlitex.DB, p Panel, tags *normalize.Namer, accounts Accounts, renamed map[string]string) []string {
	rows, err := db.Select("inbounds", []string{
		"id", "up", "down", "total", "remark", "enable", "expiry_time", "listen",
		"port", "protocol", "settings", "stream_settings", "tag", "sniffing",
	}, "id")
	if err != nil {
		b.Note("no inbounds were read: %v", err)
		return nil
	}

	var items []string
	for _, r := range rows {
		id := r.Int("id")
		protocol := strings.ToLower(r.Str("protocol"))
		remark := r.Str("remark")
		sourceTag := r.Str("tag")
		if sourceTag == "" {
			sourceTag = remark
		}
		res := tags.Tag(sourceTag, fmt.Sprintf("inbound-%d", id))
		renamed[sourceTag] = res.Name

		settings := convert.DecodeObjString(r.Str("settings"))
		stream := convert.DecodeObjString(r.Str("stream_settings"))
		port := int(r.Int("port"))

		if protocol == "wireguard" || protocol == "amneziawg" {
			for _, email := range WireGuardEndpoint(b, p, id, res, protocol, port, settings) {
				accounts.MarkPeer(email)
			}
			continue
		}

		conv := convert.Inbound(protocol, r.Str("listen"), port, settings, stream)
		it := bundle.Item{
			ID: "inbound:" + strconv.FormatInt(id, 10), Kind: bundle.KindInbound,
			Name: res.Name, SourceName: sourceTag, Group: []string{group(protocol)},
		}
		it.Set("protocol", protocol)
		it.Set("port", strconv.Itoa(port))
		it.Set("remark", remark)
		it.Set("clients", strconv.Itoa(len(conv.Clients)))
		if !r.Bool("enable") {
			it.Set("enabled in "+p.Name, "no")
		}

		if conv.Blocked != "" {
			it.Block("%s", conv.Blocked)
			b.Add(it)
			continue
		}
		conv.Config["tag"] = res.Name
		payload, _ := json.Marshal(map[string]any{
			"tag": res.Name, "type": conv.Type, "config": conv.Config, "remark": remark,
		})
		it.Payload = payload
		it.Set("type", conv.Type)
		for _, n := range conv.Notes {
			it.AddNote("%s", n)
		}
		if res.Changed {
			it.AddNote("%s", res.Note)
		}
		if r.Str("sniffing") != "" {
			it.AddNote("sniffing was dropped — sing-box 1.12 and later express it as a route action, which Nexora sets on the template")
		}
		b.Add(it)
		items = append(items, it.ID)

		accounts.Merge(conv.Clients, protocol, it.ID)
	}
	return items
}

// WireGuardEndpoint converts a WireGuard inbound into a sing-box endpoint. The
// peers carry across; the panel-side key management does not.
//
// It returns the emails the peers carry, which a panel that stores peers as
// clients also keeps traffic rows under.
func WireGuardEndpoint(b *bundle.Bundle, p Panel, id int64, res normalize.Result, protocol string, port int, settings convert.Obj) (emails []string) {
	it := bundle.Item{
		ID: "endpoint:" + strconv.FormatInt(id, 10), Kind: bundle.KindEndpoint,
		Name: res.Name, SourceName: res.Source, Group: []string{"wireguard"},
	}
	if protocol == "amneziawg" {
		it.Block("AmneziaWG is not a sing-box endpoint type; rebuild it in Nexora if the node's overlay supports it")
		b.Add(it)
		return
	}
	cfg := convert.Obj{"type": "wireguard", "tag": res.Name}
	if port > 0 {
		cfg["listen_port"] = port
	}
	if settings != nil {
		if k, ok := settings["secretKey"].(string); ok && k != "" {
			cfg["private_key"] = k
		}
		if mtu, ok := settings["mtu"].(float64); ok && mtu > 0 {
			cfg["mtu"] = int(mtu)
		}
		// The classic x-ui line keeps its peers under `clients` and renames them
		// only when it writes the Xray config; 3x-ui stores `peers` directly.
		raw, _ := settings["peers"].([]any)
		if len(raw) == 0 && p.WireGuardClientsArePeers {
			raw, _ = settings["clients"].([]any)
		}
		var peers []convert.Obj
		for _, e := range raw {
			peer, ok := e.(convert.Obj)
			if !ok {
				continue
			}
			if e, ok := peer["email"].(string); ok && e != "" {
				emails = append(emails, e)
			}
			np := convert.Obj{}
			for _, key := range []string{"publicKey", "public_key"} {
				if v, ok := peer[key].(string); ok && v != "" {
					np["public_key"] = v
				}
			}
			for _, key := range []string{"psk", "preSharedKey", "pre_shared_key"} {
				if v, ok := peer[key].(string); ok && v != "" {
					np["pre_shared_key"] = v
				}
			}
			for _, key := range []string{"allowedIPs", "allowed_ips"} {
				if v, ok := peer[key].([]any); ok && len(v) > 0 {
					np["allowed_ips"] = v
				}
			}
			if v, ok := peer["keepAlive"].(float64); ok && v > 0 {
				np["persistent_keepalive_interval"] = int(v)
			}
			if len(np) > 0 {
				peers = append(peers, np)
			}
		}
		if len(peers) > 0 {
			cfg["peers"] = peers
			it.Set("peers", strconv.Itoa(len(peers)))
		}
	}
	payload, _ := json.Marshal(map[string]any{"tag": res.Name, "type": "wireguard", "config": cfg})
	it.Payload = payload
	it.Set("type", "wireguard")
	it.Set("port", strconv.Itoa(port))
	it.AddNote("Nexora mints WireGuard keys per user and derives addresses itself; these peers arrive as written and are not managed accounts")
	b.Add(it)
	return emails
}

// Admins carries the panel logins across. Password hashes cannot travel between
// panels, so each arrives with a generated one.
func Admins(b *bundle.Bundle, db *sqlitex.DB, p Panel) {
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
		payload, _ := json.Marshal(map[string]any{"username": username, "password": pw, "role": "admin"})
		it := bundle.Item{
			ID: "admin:" + r.Str("id"), Kind: bundle.KindAdmin,
			Name: username, SourceName: username, Payload: payload,
		}
		it.Set("role", "admin")
		it.Set("new password", pw)
		it.AddNote("%s hashes its logins differently: this admin arrives with the generated password shown here — write it down, it is not stored", p.Name)
		b.Add(it)
	}
}

// Clients joins the accounts gathered out of the inbounds with the
// `client_traffics` counters and emits one Nexora user each.
func Clients(b *bundle.Bundle, db *sqlitex.DB, settings map[string]string, accounts Accounts, templateItem string) {
	traffic := map[string]sqlitex.Row{}
	rows, err := db.Select("client_traffics", []string{
		"id", "inbound_id", "enable", "email", "up", "down", "expiry_time",
		"total", "reset", "reset_day", "last_online",
	}, "id")
	if err == nil {
		for _, r := range rows {
			traffic[r.Str("email")] = r
		}
	}
	for email := range traffic {
		accounts.Ensure(email)
	}

	names := normalize.NewNamer()
	subBase := SubscriptionBase(settings)

	peers := 0
	for _, a := range accounts.Ordered() {
		if a.Peer && a.UUID == "" && a.Password == "" {
			peers++
			continue
		}
		t := traffic[a.Email]
		c := convert.Client{
			SourceID:  a.Email,
			Name:      a.Email,
			Enable:    true,
			UUID:      a.UUID,
			Password:  a.Password,
			Flow:      a.Flow,
			Method:    a.Method,
			Conflicts: a.Conflicts,
			IPLimit:   a.LimitIP,
			SubID:     a.SubID,
			Up:        t.Int("up"),
			Down:      t.Int("down"),
			OnlineAt:  convert.MillisToUnix(t.Int("last_online")),
		}
		if a.Enable != nil {
			c.Enable = *a.Enable
		}
		if t.Has("enable") {
			c.Enable = c.Enable && t.Bool("enable")
		}

		c.Volume = t.Int("total")
		if c.Volume == 0 {
			c.Volume = a.TotalGB
		}

		expiry := t.Int("expiry_time")
		if expiry == 0 {
			expiry = a.ExpiryMS
		}
		// These panels store milliseconds, and a *negative* value is their
		// start-on-first-use shape: that much lifetime, not yet begun. Nexora
		// spells the same thing as Duration.
		switch {
		case expiry < 0:
			c.Duration = -expiry / 1000
		case expiry > 0:
			c.Expiry = expiry / 1000
		}

		reset, resetDay := a.Reset, a.ResetDay
		if n := int(t.Int("reset")); n > 0 {
			reset = n
		}
		if n := int(t.Int("reset_day")); n > 0 {
			resetDay = n
		}
		var resetNote string
		switch {
		case a.TrafficReset == "hourly":
			// Nexora's shortest cycle is a day; an hourly reset is
			// effectively no quota at all, so it is said rather than faked.
			resetNote = "the old panel reset this account's traffic every hour; Nexora's shortest cycle is a day, so no reset was set"
		case a.TrafficReset != "":
			// 3x-ui's own traffic cycle, which is exactly Nexora's.
			convert.ApplyResetStrategy(&c, a.TrafficReset, a.TrafficResetDay)
		case resetDay > 0:
			// Since 3x-ui v3.7 a renewal day turns the interval into a
			// calendar renewal on that day of the month.
			convert.ApplyResetStrategy(&c, "monthly", resetDay)
		case reset > 0:
			c.AutoReset, c.ResetDays = true, reset
		}

		// The two things this panel keeps about the person. The Telegram id
		// goes on the customer's card; the note is a note, and Nexora already
		// has one field for that (docs/product.md decision 12) rather than a
		// fourth free-text column for every importer to choose between.
		convert.SetContact(&c, convert.ContactTelegramID, a.TelegramID)
		if a.Comment != "" {
			c.Desc = a.Comment
		}

		if c.SubID != "" && subBase != "" {
			c.SubURL = subBase + c.SubID
		}
		if templateItem != "" && len(a.Inbounds) > 0 {
			c.TemplateItems = []string{templateItem}
		}

		it := convert.ClientItem(c, names)
		it.Set("inbounds", strconv.Itoa(len(a.Inbounds)))
		if a.TelegramID != "" {
			it.Set("telegram", a.TelegramID)
		}
		if a.Comment != "" {
			it.AddNote("the client's comment was carried over as the account's description")
		}
		if resetNote != "" {
			it.AddNote("%s", resetNote)
		}
		if a.UUID == "" && a.Password == "" {
			it.Block("this account has no credentials in the database — its inbound was probably deleted; recreate it in Nexora instead")
		}
		b.Add(it)
	}
	if peers > 0 {
		b.Note("%d WireGuard peer(s) arrive on their endpoint, as written, rather than as accounts", peers)
	}
}

// SubscriptionBase reconstructs the old subscription address so the review
// table can show the link a customer already holds. Both panels spell these
// settings the same way.
func SubscriptionBase(settings map[string]string) string {
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
	host := domain
	if port := settings["subPort"]; port != "" && port != "443" {
		host += ":" + port
	}
	return "https://" + host + path
}

// RuleSets creates a mirrored rule set for every geosite:/geoip: reference the
// routing conversion produced.
//
// Without them a converted rule names a tag nothing defines, and the panel drops
// that rule when it builds a node's config — the routing would look imported and
// quietly not apply.
func RuleSets(b *bundle.Bundle, tags []string) []string {
	var ids []string
	for _, tag := range tags {
		url := convert.RuleSetURL(tag)
		if url == "" {
			b.Note("the routing referenced a rule set %q this tool has no source for; create it by hand or the rules naming it will not apply", tag)
			continue
		}
		payload, _ := json.Marshal(map[string]any{
			"tag": tag, "url": url, "updateIntervalHours": 24,
		})
		it := bundle.Item{
			ID: "ruleset:" + tag, Kind: bundle.KindRuleSet,
			Name: tag, SourceName: tag, Payload: payload,
		}
		it.Set("url", url)
		it.AddNote("this rule set replaces a bundled geosite/geoip list, which sing-box dropped in 1.12. The panel downloads it when it is created, so an unreachable URL fails this one item and nothing else")
		b.Add(it)
		ids = append(ids, it.ID)
	}
	return ids
}

// Outbound converts one Xray outbound object into a bundle item, or records why
// it could not be. It returns the item id, or "" when nothing was produced.
func Outbound(b *bundle.Bundle, tags *normalize.Namer, rc convert.RouteContext, fallbackName string, o convert.Obj) string {
	tag, _ := o["tag"].(string)
	if tag == "" {
		tag = fallbackName
	}
	typ, cfg, notes, ok := convert.Outbound(o)
	if ok && len(notes) == 0 && convert.IsBuiltinDirect(tag, typ, cfg) && !convert.XrayOutboundExtras(o) {
		rc.Renamed[tag] = convert.BuiltinDirect
		b.Note("the old panel's plain %q outbound is the one Nexora already puts on every node, so it was not created again; rules that named it use Nexora's", tag)
		return ""
	}
	res := tags.Tag(tag, fallbackName)
	rc.Renamed[tag] = res.Name

	it := bundle.Item{
		ID: "outbound:" + res.Name, Kind: bundle.KindOutbound,
		Name: res.Name, SourceName: tag,
	}
	if !ok {
		it.Block("%s", strings.Join(notes, "; "))
		b.Add(it)
		return ""
	}
	if typ == "block" {
		// A rule pointing at this tag becomes sing-box's reject action.
		rc.Blocked[tag] = true
	}
	cfg["tag"] = res.Name
	payload, _ := json.Marshal(map[string]any{"tag": res.Name, "type": typ, "config": cfg})
	it.Payload, it.Group = payload, []string{typ}
	it.Set("type", typ)
	for _, n := range notes {
		it.AddNote("%s", n)
	}
	if res.Changed {
		it.AddNote("%s", res.Note)
	}
	b.Add(it)
	return it.ID
}

// Template builds the one Nexora template these panels reduce to: the routing
// and DNS they ran, plus the selection of everything else that was converted.
func Template(b *bundle.Bundle, p Panel, id string, route convert.Obj, core map[string]any, inbounds, outbounds, ruleSets []string) string {
	payload := map[string]any{
		"name":       p.Name,
		"remark":     "routing, DNS and egress carried over from " + p.Name,
		"inboundIds": []uint{}, "outboundIds": []uint{}, "ruleSetIds": []uint{},
	}
	if len(route) > 0 {
		payload["route"] = route
	}
	if len(core) > 0 {
		payload["core"] = core
	}
	body, _ := json.Marshal(payload)

	it := bundle.Item{
		ID: id, Kind: bundle.KindTemplate, Name: p.Name, SourceName: p.Name + " configuration",
		Payload: body,
		IDRefs: map[string][]string{
			"inboundIds": inbounds, "outboundIds": outbounds, "ruleSetIds": ruleSets,
		},
	}
	if rules, ok := route["rules"].([]convert.Obj); ok {
		it.Set("routing rules", strconv.Itoa(len(rules)))
	}
	it.Set("inbounds", strconv.Itoa(len(inbounds)))
	it.Set("outbounds", strconv.Itoa(len(outbounds)))
	it.Set("rule sets", strconv.Itoa(len(ruleSets)))
	it.AddNote("check the rules before assigning this template to a node: Xray and sing-box do not name every condition the same way, and anything with no equivalent was dropped")
	b.Add(it)
	return it.ID
}

func group(p string) string {
	if p == "" {
		return "other"
	}
	return p
}

func appendUnique(list []string, v string) []string {
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// NoteOn adds conversion notes to an item that is already in the bundle. The
// template is built by a shared helper but its notes come from the reader's own
// conversion, which happens either side of it.
func NoteOn(b *bundle.Bundle, id string, notes []string) {
	if len(notes) == 0 {
		return
	}
	for i := range b.Items {
		if b.Items[i].ID != id {
			continue
		}
		for _, n := range notes {
			b.Items[i].AddNote("%s", n)
		}
		return
	}
}
