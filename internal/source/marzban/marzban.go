// Package marzban reads a Marzban installation — and PasarGuard, which is a
// fork of it — over the panel's own REST API.
//
// Marzban runs on MySQL or MariaDB on any real install, so there is no single
// file to copy: the API is the channel. It is also a good one here. Marzban
// computes two things the database does not store and this migration wants
// most: each user's finished `subscription_url`, which is what keeps a
// customer's existing link working, and the aggregated `used_traffic`.
//
// What the API does not expose is the JWT secret, which is what makes those
// subscription tokens derivable. That does not matter — the finished URL comes
// back in every user object, so the token is read rather than recomputed.
package marzban

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/convert"
	"github.com/nexora-vpn/nexora-migrate/internal/httpx"
	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
	"github.com/nexora-vpn/nexora-migrate/internal/source"
)

func init() {
	source.Register(source.Descriptor{
		ID:       "marzban",
		Label:    "Marzban",
		Channels: []source.Channel{source.ChannelAPI},
		Hint:     "Give the panel's address and a sudo admin's login. Marzban runs on MySQL, so there is no single file to copy — this reads the panel's own API.",
		Auth:     []source.AuthMode{source.AuthLogin, source.AuthToken},
	}, reader{flavour: "marzban"})

	source.Register(source.Descriptor{
		ID:       "pasarguard",
		Label:    "PasarGuard",
		Channels: []source.Channel{source.ChannelAPI},
		Hint:     "A Marzban fork. Give the address and either an admin login or an API key.",
		Auth:     []source.AuthMode{source.AuthLogin, source.AuthToken},
	}, reader{flavour: "pasarguard"})
}

type reader struct{ flavour string }

func (r reader) Read(ctx context.Context, opt source.Options) (*bundle.Bundle, error) {
	c, err := httpx.New(opt.BaseURL, opt.Insecure)
	if err != nil {
		return nil, err
	}
	if err := login(ctx, c, opt); err != nil {
		return nil, err
	}

	label := "Marzban"
	if r.flavour == "pasarguard" {
		label = "PasarGuard"
	}
	b := &bundle.Bundle{Source: bundle.SourceInfo{Panel: label, Origin: c.Base}}
	readVersion(ctx, c, b)

	tags := normalize.NewNamer()
	tags.Reserve(convert.BuiltinDirect)
	templates := readCores(ctx, c, b, tags)
	admins := readAdmins(ctx, c, b)
	readUsers(ctx, c, b, admins, templates)
	return b, nil
}

// login obtains a bearer token. An API key is used directly when one is given
// (PasarGuard has them); otherwise the OAuth2 password form Marzban uses.
func login(ctx context.Context, c *httpx.Client, opt source.Options) error {
	if opt.Token != "" {
		c.SetBearer(opt.Token)
		return nil
	}
	if opt.Username == "" || opt.Password == "" {
		return fmt.Errorf("this panel needs an admin username and password, or an API key")
	}
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", opt.Username)
	form.Set("password", opt.Password)

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.Post(ctx, "/api/admin/token", form, &out); err != nil {
		if httpx.Status(err) == 401 {
			return fmt.Errorf("the panel refused that username and password")
		}
		return fmt.Errorf("could not log in: %w", err)
	}
	if out.AccessToken == "" {
		return fmt.Errorf("the panel accepted the login but returned no token")
	}
	c.SetBearer(out.AccessToken)
	return nil
}

func readVersion(ctx context.Context, c *httpx.Client, b *bundle.Bundle) {
	var sys struct {
		Version string `json:"version"`
	}
	if c.Get(ctx, "/api/system", &sys) == nil {
		b.Source.Version = sys.Version
	}
}

// readCores converts the Xray configuration the panel runs into Nexora
// templates and returns their item ids. Marzban has one, at /api/core/config.
// PasarGuard keeps any number of named cores behind /api/cores and has no
// /api/core/config at all; each of its Xray cores becomes a template of its own.
// Requires a sudo admin; a plain admin gets a 403 and the migration continues
// with users only.
func readCores(ctx context.Context, c *httpx.Client, b *bundle.Bundle, tags *normalize.Namer) (templates []string) {
	var raw json.RawMessage
	err := c.Get(ctx, "/api/core/config", &raw)
	if err == nil {
		cfg := convert.DecodeObj(raw)
		if cfg == nil {
			b.Note("the panel returned an Xray configuration this tool could not parse")
			return nil
		}
		return appendIf(nil, convertCore(b, tags, cfg, b.Source.Panel, "template:core"))
	}
	if st := httpx.Status(err); st == http.StatusNotFound || st == http.StatusMethodNotAllowed {
		var list struct {
			Cores []struct {
				ID     int64           `json:"id"`
				Name   string          `json:"name"`
				Type   string          `json:"type"`
				Config json.RawMessage `json:"config"`
			} `json:"cores"`
		}
		if err = c.Get(ctx, "/api/cores", &list); err == nil {
			for _, core := range list.Cores {
				if core.Type != "" && !strings.EqualFold(core.Type, "xray") {
					b.Note("core %q is a %s core, which this tool does not convert", core.Name, core.Type)
					continue
				}
				cfg := convert.DecodeObj(core.Config)
				if cfg == nil {
					b.Note("core %q has a configuration this tool could not parse", core.Name)
					continue
				}
				name := core.Name
				if name == "" {
					name = fmt.Sprintf("%s core %d", b.Source.Panel, core.ID)
				}
				templates = appendIf(templates, convertCore(b, tags, cfg, name, fmt.Sprintf("template:core-%d", core.ID)))
			}
			return templates
		}
	}
	if httpx.Status(err) == http.StatusForbidden {
		b.Note("this login is not a sudo admin, so the Xray configuration could not be read — inbounds, outbounds, routing and DNS are not in this list; accounts still are")
	} else {
		b.Note("the Xray configuration could not be read (%v) — accounts are still listed", err)
	}
	return nil
}

// convertCore turns one Xray configuration into its inbounds, outbounds and
// rule sets plus the template that joins them, and returns the template's id.
func convertCore(b *bundle.Bundle, tags *normalize.Namer, cfg convert.Obj, name, itemID string) string {
	var inbounds, outbounds []string

	rc := convert.RouteContext{Blocked: map[string]bool{}, Renamed: map[string]string{}}
	if arr, ok := cfg["inbounds"].([]any); ok {
		for i, e := range arr {
			o, ok := e.(convert.Obj)
			if !ok {
				continue
			}
			inbounds = appendIf(inbounds, convertInbound(b, tags, i, o, rc))
		}
	}
	if arr, ok := cfg["outbounds"].([]any); ok {
		for i, e := range arr {
			o, ok := e.(convert.Obj)
			if !ok {
				continue
			}
			outbounds = appendIf(outbounds, convertOutbound(b, tags, i, o, rc))
		}
	}

	route, routeNotes, ruleSetTags := convert.Routing(objOf(cfg, "routing"), rc)
	ruleSets := ruleSetItems(b, ruleSetTags)
	dns, dnsNotes := convert.DNS(objOf(cfg, "dns"))

	payload := map[string]any{
		"name":       name,
		"remark":     "routing, DNS and egress carried over from " + b.Source.Panel,
		"inboundIds": []uint{}, "outboundIds": []uint{}, "ruleSetIds": []uint{},
	}
	if len(route) > 0 {
		payload["route"] = route
	}
	if dns != nil {
		payload["core"] = map[string]any{"dns": dns}
	}
	body, _ := json.Marshal(payload)
	it := bundle.Item{
		ID: itemID, Kind: bundle.KindTemplate, Name: name,
		SourceName: "xray core config", Payload: body,
		IDRefs: map[string][]string{
			"inboundIds": inbounds, "outboundIds": outbounds, "ruleSetIds": ruleSets,
		},
	}
	for _, n := range append(routeNotes, dnsNotes...) {
		it.AddNote("%s", n)
	}
	it.Set("inbounds", strconv.Itoa(len(inbounds)))
	it.Set("outbounds", strconv.Itoa(len(outbounds)))
	it.Set("rule sets", strconv.Itoa(len(ruleSets)))
	it.AddNote("check the rules before assigning this template to a node: Xray and sing-box do not name every condition the same way, and anything with no equivalent was dropped")
	b.Add(it)
	return it.ID
}

func convertInbound(b *bundle.Bundle, tags *normalize.Namer, i int, o convert.Obj, rc convert.RouteContext) string {
	tag, _ := o["tag"].(string)
	if tag == "" {
		tag = fmt.Sprintf("inbound-%d", i+1)
	}
	protocol, _ := o["protocol"].(string)
	res := tags.Tag(tag, fmt.Sprintf("inbound-%d", i+1))
	rc.Renamed[tag] = res.Name

	listen, _ := o["listen"].(string)
	port := 0
	if p, ok := o["port"].(float64); ok {
		port = int(p)
	}
	conv := convert.Inbound(protocol, listen, port,
		objOf(o, "settings"), objOf(o, "streamSettings"))

	it := bundle.Item{
		ID: "inbound:" + res.Name, Kind: bundle.KindInbound,
		Name: res.Name, SourceName: tag, Group: []string{groupOf(protocol)},
	}
	it.Set("protocol", protocol)
	it.Set("port", strconv.Itoa(port))
	if conv.Blocked != "" {
		it.Block("%s", conv.Blocked)
		b.Add(it)
		return ""
	}
	conv.Config["tag"] = res.Name
	payload, _ := json.Marshal(map[string]any{
		"tag": res.Name, "type": conv.Type, "config": conv.Config, "remark": tag,
	})
	it.Payload = payload
	it.Set("type", conv.Type)
	for _, n := range conv.Notes {
		it.AddNote("%s", n)
	}
	if res.Changed {
		it.AddNote("%s", res.Note)
	}
	it.AddNote("Marzban advertises this inbound through its own Hosts list (addresses, SNI, fragment); Nexora advertises the node's address instead, so check the generated links")
	b.Add(it)
	return it.ID
}

func convertOutbound(b *bundle.Bundle, tags *normalize.Namer, i int, o convert.Obj, rc convert.RouteContext) string {
	tag, _ := o["tag"].(string)
	if tag == "" {
		tag = fmt.Sprintf("outbound-%d", i+1)
	}
	typ, cfg, notes, ok := convert.Outbound(o)
	if ok && len(notes) == 0 && convert.IsBuiltinDirect(tag, typ, cfg) && !convert.XrayOutboundExtras(o) {
		rc.Renamed[tag] = convert.BuiltinDirect
		b.Note("the old panel's plain %q outbound is the one Nexora already puts on every node, so it was not created again; rules that named it use Nexora's", tag)
		return ""
	}
	res := tags.Tag(tag, fmt.Sprintf("outbound-%d", i+1))
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
		rc.Blocked[tag] = true
	}
	cfg["tag"] = res.Name
	payload, _ := json.Marshal(map[string]any{"tag": res.Name, "type": typ, "config": cfg})
	it.Payload, it.Group = payload, []string{typ}
	it.Set("type", typ)
	for _, n := range notes {
		it.AddNote("%s", n)
	}
	b.Add(it)
	return it.ID
}

type marzbanAdmin struct {
	Username   string `json:"username"`
	IsSudo     bool   `json:"is_sudo"`
	TelegramID *int64 `json:"telegram_id"`
	UsersUsage int64  `json:"users_usage"`
	// PasarGuard has roles instead of is_sudo; its owner and sudo roles are
	// what Marzban called sudo.
	Role *struct {
		Name    string `json:"name"`
		IsOwner bool   `json:"is_owner"`
	} `json:"role"`
}

func (a marzbanAdmin) sudo() bool {
	if a.IsSudo {
		return true
	}
	return a.Role != nil && (a.Role.IsOwner ||
		strings.EqualFold(a.Role.Name, "sudo") || strings.EqualFold(a.Role.Name, "owner"))
}

// readAdmins carries the operator accounts across and returns username → item
// id so users can keep their owner.
func readAdmins(ctx context.Context, c *httpx.Client, b *bundle.Bundle) map[string]string {
	out := map[string]string{}
	// Marzban answers a bare list, PasarGuard {admins, total, …}.
	var raw json.RawMessage
	if err := c.Get(ctx, "/api/admins", &raw); err != nil {
		if httpx.Status(err) == 403 {
			b.Note("this login cannot list admins, so account ownership is not carried over")
		}
		return out
	}
	var admins []marzbanAdmin
	if json.Unmarshal(raw, &admins) != nil {
		var env struct {
			Admins []marzbanAdmin `json:"admins"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			b.Note("the admin list came back in a shape this tool does not recognise, so account ownership is not carried over")
			return out
		}
		admins = env.Admins
	}
	for _, a := range admins {
		if a.Username == "" {
			continue
		}
		pw := convert.RandomPassword()
		// A Marzban sudo becomes a Nexora admin, not a sudo: Nexora's sudo is
		// the install's own main account and is never created by an import.
		// Everyone else becomes a reseller, which is what they were.
		role := "resale"
		if a.sudo() {
			role = "admin"
		}
		payload, _ := json.Marshal(map[string]any{
			"username": a.Username, "password": pw, "role": role,
		})
		it := bundle.Item{
			ID: "admin:" + a.Username, Kind: bundle.KindAdmin,
			Name: a.Username, SourceName: a.Username, Payload: payload,
		}
		it.Set("role", role)
		it.Set("new password", pw)
		if a.sudo() {
			it.AddNote("this was a sudo admin in %s; it arrives as a Nexora admin, because Nexora's sudo account belongs to the install and is never created by an import", b.Source.Panel)
		}
		it.AddNote("password hashes cannot move between panels: this admin arrives with the generated password shown here — write it down, it is not stored")
		b.Add(it)
		out[a.Username] = it.ID
	}
	return out
}

type marzbanUser struct {
	Username    string                    `json:"username"`
	Status      string                    `json:"status"`
	UsedTraffic int64                     `json:"used_traffic"`
	DataLimit   *int64                    `json:"data_limit"`
	Expire      flexTime                  `json:"expire"`
	Proxies     map[string]map[string]any `json:"proxies"`
	// PasarGuard renamed proxies; same shape, one entry per protocol.
	ProxySettings   map[string]map[string]any    `json:"proxy_settings"`
	Inbounds        map[string][]string          `json:"inbounds"`
	Note            string                       `json:"note"`
	OnlineAt        *time.Time                   `json:"online_at"`
	CreatedAt       *time.Time                   `json:"created_at"`
	SubscriptionURL string                       `json:"subscription_url"`
	OnHoldDuration  *int64                       `json:"on_hold_expire_duration"`
	ResetStrategy   string                       `json:"data_limit_reset_strategy"`
	Admin           *struct{ Username string }   `json:"admin"`
	Groups          []map[string]json.RawMessage `json:"groups"` // PasarGuard
}

func readUsers(ctx context.Context, c *httpx.Client, b *bundle.Bundle, admins map[string]string, templates []string) {
	names := normalize.NewNamer()
	const page = 500
	offset := 0
	seen := 0

	for {
		var resp struct {
			Users []marzbanUser `json:"users"`
			Total int           `json:"total"`
		}
		// load_sub: PasarGuard leaves subscription_url empty without it, and
		// that URL is where the token every customer holds comes from.
		// Marzban ignores the parameter.
		path := fmt.Sprintf("/api/users?offset=%d&limit=%d&load_sub=true", offset, page)
		if err := c.Get(ctx, path, &resp); err != nil {
			b.Note("reading accounts stopped after %d: %v", seen, err)
			return
		}
		if len(resp.Users) == 0 {
			return
		}
		for _, u := range resp.Users {
			b.Add(userItem(b, u, names, admins, templates))
			seen++
		}
		offset += len(resp.Users)
		if resp.Total > 0 && offset >= resp.Total {
			return
		}
		if err := ctx.Err(); err != nil {
			b.Note("reading accounts was cancelled after %d", seen)
			return
		}
	}
}

func userItem(b *bundle.Bundle, u marzbanUser, names *normalize.Namer, admins map[string]string, templates []string) bundle.Item {
	c := convert.Client{
		SourceID:      u.Username,
		Name:          u.Username,
		Desc:          u.Note,
		SingleCounter: true,
		Down:          u.UsedTraffic,
	}
	// Marzban's five statuses collapse to Nexora's enable flag plus the limits
	// that produced them: limited and expired are states Nexora derives from
	// volume and expiry rather than stores, so only disabled turns the account
	// off.
	switch u.Status {
	case "disabled":
		c.Enable = false
	default:
		c.Enable = true
	}
	if u.DataLimit != nil {
		c.Volume = *u.DataLimit // null already means unlimited, which is 0 here
	}
	c.Expiry = int64(u.Expire)
	if u.Status == "on_hold" && u.OnHoldDuration != nil && *u.OnHoldDuration > 0 {
		// Not started yet: Nexora spells this as Duration with no ActivatedAt.
		c.Duration, c.Expiry = *u.OnHoldDuration, 0
	}
	if u.OnlineAt != nil {
		c.OnlineAt = u.OnlineAt.Unix()
	}
	convert.ApplyResetStrategy(&c, u.ResetStrategy, 0)

	proxies := u.Proxies
	if len(proxies) == 0 {
		proxies = u.ProxySettings
	}
	c.UUID, c.Password, c.Flow, c.Method, c.Conflicts = credentials(proxies)
	c.SubID, c.SubURL = subToken(u.SubscriptionURL)

	if u.Admin != nil && u.Admin.Username != "" {
		c.Group = u.Admin.Username
		if id, ok := admins[u.Admin.Username]; ok {
			c.OwnerItem = id
		}
	}
	if len(templates) > 0 {
		c.TemplateItems = templates
	}

	it := convert.ClientItem(c, names)
	it.Set("status", u.Status)
	if tags := inboundTags(u.Inbounds); tags != "" {
		it.Set("inbounds", tags)
	}
	if u.Admin != nil {
		it.Set("owner", u.Admin.Username)
	}
	if c.UUID == "" && c.Password == "" {
		it.Block("this account has no proxy credentials in the panel")
	}
	switch u.Status {
	case "limited":
		it.AddNote("this account was over its quota in %s; Nexora works that out from the volume and the counters, so it arrives enabled and still over", b.Source.Panel)
	case "expired":
		it.AddNote("this account was past its expiry in %s; it arrives enabled with the same expiry date, so it is still expired", b.Source.Panel)
	}
	return it
}

// credentials collapses Marzban's per-protocol proxies into the single set
// Nexora keeps, naming every protocol whose secret is discarded.
func credentials(proxies map[string]map[string]any) (uuid, password, flow, method string, conflicts []string) {
	get := func(proto, key string) string {
		if p, ok := proxies[proto]; ok {
			if s, ok := p[key].(string); ok {
				return s
			}
		}
		return ""
	}
	// VLESS first: it is what an operator is most likely selling, and it is the
	// one that carries a flow.
	for _, proto := range []string{"vless", "vmess"} {
		id := get(proto, "id")
		if id == "" {
			continue
		}
		if uuid == "" {
			uuid = id
			if f := get(proto, "flow"); f != "" {
				flow = f
			}
		} else if id != uuid {
			conflicts = append(conflicts, proto)
		}
	}
	for _, proto := range []string{"trojan", "shadowsocks"} {
		pw := get(proto, "password")
		if pw == "" {
			continue
		}
		if password == "" {
			password = pw
			if m := get(proto, "method"); m != "" {
				method = m
			}
		} else if pw != password {
			conflicts = append(conflicts, proto)
		}
	}
	return
}

// subToken pulls the token out of a finished subscription URL. Nexora resolves
// /sub/{token} against sub_id as well as its own sub_token, so the old token
// stored here keeps a customer's existing link working once the hostname
// points at Nexora.
func subToken(raw string) (token, full string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	full = raw
	trimmed := strings.TrimRight(raw, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 || idx+1 >= len(trimmed) {
		return "", full
	}
	token = trimmed[idx+1:]
	// A relative subscription_url ("/sub/xyz") is what Marzban returns when
	// XRAY_SUBSCRIPTION_URL_PREFIX is unset; keep the token, drop the illusion
	// of a full link.
	if !strings.Contains(raw, "://") {
		full = ""
	}
	return token, full
}

func inboundTags(m map[string][]string) string {
	var all []string
	for _, tags := range m {
		all = append(all, tags...)
	}
	if len(all) == 0 {
		return ""
	}
	if len(all) > 4 {
		return fmt.Sprintf("%s +%d more", strings.Join(all[:4], ", "), len(all)-4)
	}
	return strings.Join(all, ", ")
}

func objOf(parent convert.Obj, key string) convert.Obj {
	if parent == nil {
		return nil
	}
	o, _ := parent[key].(convert.Obj)
	return o
}

func groupOf(p string) string {
	if p == "" {
		return "other"
	}
	return strings.ToLower(p)
}

func appendIf(list []string, v string) []string {
	if v == "" {
		return list
	}
	return append(list, v)
}

// ruleSetItems creates a mirrored rule set for every geosite:/geoip: reference
// the routing conversion produced. Without them a converted rule names a tag
// nothing defines, and the panel simply drops that rule when it builds a node's
// config — the routing would look imported and quietly not apply.
func ruleSetItems(b *bundle.Bundle, tags []string) []string {
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

// flexTime reads an expiry as either spelling it has had: Marzban's unix
// seconds, or the ISO datetime PasarGuard answers with. Null and zero are
// both "never", which is 0 here as in Nexora.
type flexTime int64

func (f *flexTime) UnmarshalJSON(b []byte) error {
	*f = 0
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var n json.Number
	if json.Unmarshal(b, &n) == nil {
		v, err := n.Float64()
		if err != nil {
			return err
		}
		*f = flexTime(v)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() > 1980 {
				*f = flexTime(t.Unix())
			}
			return nil
		}
	}
	return fmt.Errorf("unrecognised time %q", s)
}
