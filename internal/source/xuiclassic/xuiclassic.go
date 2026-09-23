// Package xuiclassic reads the classic x-ui line from its SQLite database:
// vaxilu's original and the forks that carried it on, alireza0's among them.
//
// It shares its accounts, inbounds and logins with 3x-ui — that is all in
// internal/source/xraysql — and differs in where the rest of the configuration
// lives. The later forks gave outbounds and routing rules **tables of their
// own**, one Xray rule per row as raw JSON ordered by `sort`, while 3x-ui keeps
// both inside one setting. Older installs have neither table, so this reader
// falls back to the `xrayTemplateConfig` setting and covers the whole family
// with one code path.
package xuiclassic

import (
	"context"
	"fmt"
	"strings"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/convert"
	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
	"github.com/nexora-vpn/nexora-migrate/internal/source"
	"github.com/nexora-vpn/nexora-migrate/internal/source/dbfetch"
	"github.com/nexora-vpn/nexora-migrate/internal/source/xraysql"
	"github.com/nexora-vpn/nexora-migrate/internal/sqlitex"
)

// WireGuardClientsArePeers: this line stores WireGuard peers under
// `settings.clients` and only renames them to `peers` when it writes the Xray
// config, so a reader looking for `peers` would find an endpoint with none.
var panel = xraysql.Panel{Name: "x-ui", WireGuardClientsArePeers: true}

func init() {
	source.Register(source.Descriptor{
		ID:               "x-ui",
		Label:            "x-ui (classic / alireza0)",
		Channels:         []source.Channel{source.ChannelFile, source.ChannelAPI},
		DefaultPath:      "/etc/x-ui/x-ui.db",
		Hint:             "The original x-ui and its forks, alireza0/x-ui included. Pick x-ui.db, or let the wizard fetch it from a live panel — forks that have a backup endpoint hand the database over, vaxilu's original has none and needs the file. If this is actually 3x-ui, pick 3x-ui instead: it keeps its routing somewhere else.",
		Auth:             []source.AuthMode{source.AuthLogin},
		APIFetchesBackup: true,
	}, reader{})
}

type reader struct{}

func (reader) Read(ctx context.Context, opt source.Options) (*bundle.Bundle, error) {
	src, err := dbfetch.Source(ctx, dbfetch.XUIClassic, opt)
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

	b := &bundle.Bundle{Source: bundle.SourceInfo{Panel: panel.Name, Origin: src.Origin}}
	if src.Note != "" {
		b.Note("%s", src.Note)
	}
	settings := xraysql.Settings(db)
	b.Source.Version = settings["version"]

	// Both panels call their file x-ui.db, so an operator who picked the wrong
	// one gets a plausible-looking half-import. 3x-ui's own tables are the tell.
	if db.HasTable("client_ips") || db.HasTable("inbound_client_ips") {
		if !db.HasTable("outbounds") && !db.HasTable("routing_rules") {
			b.Note("this database has neither an outbounds nor a routing_rules table. That is normal for the original x-ui, but it is also what 3x-ui looks like — if this is a 3x-ui install, go back and pick 3x-ui, or its routing and egress will not come across.")
		}
	}

	accounts := xraysql.Accounts{}
	tags := normalize.NewNamer()
	tags.Reserve(convert.BuiltinDirect)
	renamed := map[string]string{}

	inbounds := xraysql.Inbounds(b, db, panel, tags, accounts, renamed)
	template := readConfiguration(b, db, settings, tags, inbounds, renamed)
	xraysql.Admins(b, db, panel)
	xraysql.Clients(b, db, settings, accounts, template)

	return b, nil
}

// readConfiguration assembles the one template this panel reduces to. The
// egress list and the routing rules come from their own tables where the
// install has them, and from the Xray template setting where it does not.
func readConfiguration(b *bundle.Bundle, db *sqlitex.DB, settings map[string]string, tags *normalize.Namer, inbounds []string, renamed map[string]string) string {
	tpl := convert.DecodeObjString(settings["xrayTemplateConfig"])
	rc := convert.RouteContext{Blocked: map[string]bool{}, Renamed: renamed}

	outbounds := readOutbounds(b, db, tags, rc, tpl)
	routing := readRoutingRules(b, db, tpl)

	route, routeNotes, ruleSetTags := convert.Routing(routing, rc)
	dns, dnsNotes := convert.DNS(objOf(tpl, "dns"))
	ruleSets := xraysql.RuleSets(b, ruleSetTags)

	if len(route) == 0 && dns == nil && len(outbounds) == 0 && len(inbounds) == 0 {
		return ""
	}

	core := map[string]any{}
	if dns != nil {
		core["dns"] = dns
	}
	if log := objOf(tpl, "log"); log != nil {
		if level := log["loglevel"]; level != nil {
			core["log"] = convert.Obj{"level": level}
		}
	}

	id := xraysql.Template(b, panel, "template:x-ui", route, core, inbounds, outbounds, ruleSets)
	xraysql.NoteOn(b, id, append(routeNotes, dnsNotes...))
	if objOf(tpl, "policy") != nil {
		xraysql.NoteOn(b, id, []string{
			"the Xray `policy` block (per-level connection and speed limits) was dropped — Nexora puts a real speed cap and address cap on the account itself, so set them there instead",
		})
	}
	return id
}

// readOutbounds prefers the panel's own `outbounds` table, which the later
// forks added, and falls back to the Xray template for an install predating it.
func readOutbounds(b *bundle.Bundle, db *sqlitex.DB, tags *normalize.Namer, rc convert.RouteContext, tpl convert.Obj) []string {
	var out []string
	if db.HasTable("outbounds") {
		rows, err := db.Select("outbounds", []string{
			"id", "tag", "protocol", "settings", "stream_settings",
			"send_through", "mux", "sort",
		}, "sort")
		if err == nil {
			for i, r := range rows {
				o := convert.Obj{"tag": r.Str("tag"), "protocol": r.Str("protocol")}
				if s := convert.DecodeObjString(r.Str("settings")); s != nil {
					o["settings"] = s
				}
				if s := convert.DecodeObjString(r.Str("stream_settings")); s != nil {
					o["streamSettings"] = s
				}
				// The two columns this line keeps beside the JSON, on the
				// object too, so a direct outbound that uses them is not
				// folded into Nexora's own.
				if v := strings.TrimSpace(r.Str("send_through")); v != "" {
					o["sendThrough"] = v
				}
				if mux := convert.DecodeObjString(r.Str("mux")); mux != nil {
					o["mux"] = mux
				}
				id := xraysql.Outbound(b, tags, rc, fmt.Sprintf("outbound-%d", i+1), o)
				if id == "" {
					continue
				}
				out = append(out, id)
				if v := strings.TrimSpace(r.Str("send_through")); v != "" {
					xraysql.NoteOn(b, id, []string{fmt.Sprintf(
						"sendThrough %q was dropped — sing-box binds an outbound with bind_interface or inet4_bind_address, which is a different setting and has to be written by hand", v)})
				}
				if mux := convert.DecodeObjString(r.Str("mux")); mux != nil {
					if enabled, _ := mux["enabled"].(bool); enabled {
						xraysql.NoteOn(b, id, []string{
							"this outbound had mux.cool enabled; sing-box multiplexes with smux/yamux/h2mux and cannot speak mux.cool, so it was dropped",
						})
					}
				}
			}
			return out
		}
	}
	arr, _ := tpl["outbounds"].([]any)
	for i, e := range arr {
		o, ok := e.(convert.Obj)
		if !ok {
			continue
		}
		if id := xraysql.Outbound(b, tags, rc, fmt.Sprintf("outbound-%d", i+1), o); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// readRoutingRules assembles an Xray routing object out of the `routing_rules`
// table — one rule per row, as raw JSON, in the operator's own order — or out
// of the template for an install without that table.
func readRoutingRules(b *bundle.Bundle, db *sqlitex.DB, tpl convert.Obj) convert.Obj {
	if !db.HasTable("routing_rules") {
		return objOf(tpl, "routing")
	}
	rows, err := db.Select("routing_rules", []string{"id", "tag", "sort", "raw_json"}, "sort")
	if err != nil {
		return objOf(tpl, "routing")
	}
	var rules []any
	unreadable := 0
	for _, r := range rows {
		rule := convert.DecodeObjString(r.Str("raw_json"))
		if rule == nil {
			unreadable++
			continue
		}
		rules = append(rules, rule)
	}
	if unreadable > 0 {
		b.Note("%d routing rule(s) could not be parsed and were left out", unreadable)
	}
	if len(rules) == 0 {
		return objOf(tpl, "routing")
	}
	// The table does not carry the routing *meta* — domainStrategy and any
	// balancers still live in the template.
	routing := convert.Obj{"rules": rules}
	if meta := objOf(tpl, "routing"); meta != nil {
		if bal, ok := meta["balancers"]; ok {
			routing["balancers"] = bal
		}
	}
	return routing
}

func objOf(parent convert.Obj, key string) convert.Obj {
	if parent == nil {
		return nil
	}
	o, _ := parent[key].(convert.Obj)
	return o
}
