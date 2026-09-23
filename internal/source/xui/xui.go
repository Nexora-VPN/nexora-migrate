// Package xui reads a 3x-ui installation from its SQLite database.
//
// 3x-ui runs Xray, so everything here is a translation rather than a copy. The
// parts it shares with the classic x-ui line — clients living inside each
// inbound's `settings` JSON, counters in `client_traffics`, logins in `users` —
// live in internal/source/xraysql. What is specific to 3x-ui, and all this file
// is about, is where the rest of the configuration sits: routing, DNS and the
// egress list are not tables at all. They are one Xray document inside the
// `xrayTemplateConfig` setting.
package xui

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

// WireGuardClientsArePeers: since v3.7 3x-ui keeps WireGuard peers under
// `settings.clients` too, and its migration deleted `peers`.
var panel = xraysql.Panel{Name: "3x-ui", WireGuardClientsArePeers: true}

func init() {
	source.Register(source.Descriptor{
		ID:               "3x-ui",
		Label:            "3x-ui",
		Channels:         []source.Channel{source.ChannelFile, source.ChannelAPI},
		DefaultPath:      "/etc/x-ui/x-ui.db",
		Hint:             "Pick x-ui.db, or let the wizard fetch it: 3x-ui can hand over its own database over its API, which is the same file its backup button sends.",
		Auth:             []source.AuthMode{source.AuthLogin, source.AuthToken},
		NeedsTwoFactor:   true,
		APIFetchesBackup: true,
	}, reader{})
}

type reader struct{}

func (reader) Read(ctx context.Context, opt source.Options) (*bundle.Bundle, error) {
	src, err := dbfetch.Source(ctx, dbfetch.XUI3, opt)
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

	accounts := xraysql.Accounts{}
	tags := normalize.NewNamer()
	tags.Reserve(convert.BuiltinDirect)
	renamed := map[string]string{}

	inbounds := xraysql.Inbounds(b, db, panel, tags, accounts, renamed)
	template := readXrayTemplate(b, settings, tags, inbounds, renamed)
	xraysql.Admins(b, db, panel)
	xraysql.Clients(b, db, settings, accounts, template)

	return b, nil
}

// readXrayTemplate turns the `xrayTemplateConfig` setting into the outbounds,
// the rule sets and the routing/DNS template. This is the only place 3x-ui
// keeps any of them.
func readXrayTemplate(b *bundle.Bundle, settings map[string]string, tags *normalize.Namer, inbounds []string, renamed map[string]string) string {
	raw := settings["xrayTemplateConfig"]
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	tpl := convert.DecodeObjString(raw)
	if tpl == nil {
		b.Note("the 3x-ui Xray template could not be parsed, so no outbounds, routing or DNS were built")
		return ""
	}

	rc := convert.RouteContext{Blocked: map[string]bool{}, Renamed: renamed}
	var outbounds []string
	if arr, ok := tpl["outbounds"].([]any); ok {
		for i, e := range arr {
			o, ok := e.(convert.Obj)
			if !ok {
				continue
			}
			if id := xraysql.Outbound(b, tags, rc, fmt.Sprintf("outbound-%d", i+1), o); id != "" {
				outbounds = append(outbounds, id)
			}
		}
	}

	route, routeNotes, ruleSetTags := convert.Routing(objOf(tpl, "routing"), rc)
	dns, dnsNotes := convert.DNS(objOf(tpl, "dns"))
	ruleSets := xraysql.RuleSets(b, ruleSetTags)

	core := map[string]any{}
	if dns != nil {
		core["dns"] = dns
	}
	if log := objOf(tpl, "log"); log != nil {
		if level := log["loglevel"]; level != nil {
			core["log"] = convert.Obj{"level": level}
		}
	}

	id := xraysql.Template(b, panel, "template:3x-ui", route, core, inbounds, outbounds, ruleSets)
	xraysql.NoteOn(b, id, append(routeNotes, dnsNotes...))
	return id
}

func objOf(parent convert.Obj, key string) convert.Obj {
	if parent == nil {
		return nil
	}
	o, _ := parent[key].(convert.Obj)
	return o
}
