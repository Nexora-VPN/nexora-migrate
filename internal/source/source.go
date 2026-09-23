// Package source holds the readers: one per panel we can migrate from.
//
// Two channels, one interface. A reader is either file-backed (an SQLite
// database the operator copied off the old server) or API-backed (base URL plus
// credentials). Which one a panel supports is a fact about that panel, not a
// choice — 3x-ui and s-ui keep everything in one SQLite file, while Marzban,
// Marzneshin, Hiddify, PasarGuard and Remnawave run on MySQL or Postgres and
// are read over their own REST API instead.
package source

import (
	"context"
	"fmt"
	"sort"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
)

// Channel is how a reader gets at the data.
type Channel string

const (
	ChannelFile Channel = "file" // an SQLite database file
	ChannelAPI  Channel = "api"  // the source panel's own REST API
)

// AuthMode is one way of proving who you are to a source panel. Which ones a
// panel offers is a fact about that panel: the classic x-ui line never had API
// tokens, and Hiddify has nothing but.
type AuthMode string

const (
	AuthLogin AuthMode = "login" // an admin username and password
	AuthToken AuthMode = "token" // an API key or token the panel issued
)

// Options is everything a reader can be given. Which fields matter depends on
// the channel the operator picked.
type Options struct {
	Channel Channel `json:"channel"`

	// File channel.
	Path string `json:"path"`

	// API channel.
	BaseURL  string `json:"baseUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`     // an API key, where the panel has them
	ProxyKey string `json:"proxyPath"` // Hiddify's per-install path segment
	Insecure bool   `json:"insecure"`  // accept a self-signed TLS certificate
	// TwoFactor is the six-digit code from an authenticator app, for panels
	// whose admin login can be set to require one (3x-ui).
	TwoFactor string `json:"twoFactor"`
}

// Reader turns one source panel into a bundle.
type Reader interface {
	Read(ctx context.Context, opt Options) (*bundle.Bundle, error)
}

// Descriptor is what the wizard shows on its first step.
type Descriptor struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Channels []Channel `json:"channels"`
	// DefaultPath is where this panel's database normally lives, offered as a
	// placeholder so an operator does not have to look it up.
	DefaultPath string `json:"defaultPath,omitempty"`
	// Hint is one sentence shown under the form.
	Hint string `json:"hint"`
	// NeedsProxyPath marks Hiddify, whose API lives under a secret path.
	NeedsProxyPath bool `json:"needsProxyPath,omitempty"`
	// Auth is the ways this panel will let the reader in, in the order the
	// wizard should offer them. One entry means no choice to make.
	Auth []AuthMode `json:"auth,omitempty"`
	// NeedsTwoFactor marks a panel that can be set to ask for a one-time code
	// at login, so the wizard offers a field for it.
	NeedsTwoFactor bool `json:"needsTwoFactor,omitempty"`
	// APIFetchesBackup marks a source whose API channel does not re-read the
	// panel through its REST API but downloads the panel's own database backup
	// and reads that — the same bytes the file channel takes, fetched rather
	// than copied by hand. The wizard says so, because it is the reason the
	// two channels cannot disagree.
	APIFetchesBackup bool `json:"apiFetchesBackup,omitempty"`

	reader Reader
}

func (d *Descriptor) allows(m AuthMode) bool {
	for _, a := range d.Auth {
		if a == m {
			return true
		}
	}
	return false
}

var registry = map[string]*Descriptor{}

// Register adds a reader. Called from each reader package's init.
func Register(d Descriptor, r Reader) {
	d.reader = r
	registry[d.ID] = &d
}

// List returns every registered source, in display order.
func List() []Descriptor {
	out := make([]Descriptor, 0, len(registry))
	for _, d := range registry {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return order(out[i].ID) < order(out[j].ID) })
	return out
}

// order is the build order from docs/approach.md, which is also the order of
// confidence: the readers at the top were written first and are the ones a
// fixture test covers most thoroughly.
func order(id string) int {
	for i, s := range []string{"s-ui", "3x-ui", "x-ui", "marzban", "pasarguard", "hiddify", "marzneshin", "remnawave"} {
		if s == id {
			return i
		}
	}
	return 99
}

// Read runs one source's reader.
func Read(ctx context.Context, id string, opt Options) (*bundle.Bundle, error) {
	d, ok := registry[id]
	if !ok {
		return nil, fmt.Errorf("unknown source panel %q", id)
	}
	if len(d.Channels) == 1 {
		opt.Channel = d.Channels[0]
	}
	if opt.Channel == "" {
		// A caller that did not say which channel it meant told us anyway, by
		// which field it filled in.
		if opt.Path != "" {
			opt.Channel = ChannelFile
		} else if opt.BaseURL != "" {
			opt.Channel = ChannelAPI
		}
	}
	supported := false
	for _, c := range d.Channels {
		if c == opt.Channel {
			supported = true
		}
	}
	if opt.Channel == ChannelAPI && len(d.Auth) > 0 {
		// Saying which door this panel has beats letting the reader fail on a
		// credential the panel was never going to accept.
		if opt.Token != "" && !d.allows(AuthToken) {
			return nil, fmt.Errorf("%s has no API tokens — give an admin username and password instead", d.Label)
		}
		if opt.Token == "" && !d.allows(AuthLogin) {
			return nil, fmt.Errorf("%s is read with an API key, not a username and password", d.Label)
		}
	}
	if !supported {
		if opt.Channel == "" {
			return nil, fmt.Errorf("%s needs either a database file or a panel address", d.Label)
		}
		return nil, fmt.Errorf("%s cannot be read over %q", d.Label, opt.Channel)
	}
	b, err := d.reader.Read(ctx, opt)
	if err != nil {
		return nil, err
	}
	b.Validate()
	return b, nil
}
