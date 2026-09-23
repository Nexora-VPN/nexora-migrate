package source_test

import (
	"strings"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/source"

	// Every reader, so this test sees the registry the wizard actually shows.
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/hiddify"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/marzban"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/marzneshin"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/remnawave"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/sui"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/xui"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/xuiclassic"
)

// The descriptor is the contract between a reader and the form the operator
// fills in. A field left off here does not fail anywhere — it just produces a
// form asking for the wrong thing.
func TestEveryDescriptorIsUsable(t *testing.T) {
	list := source.List()
	if len(list) < 8 {
		t.Fatalf("only %d sources registered", len(list))
	}
	for _, d := range list {
		if d.Label == "" || d.Hint == "" {
			t.Errorf("%s: a source with no label or no hint cannot be chosen sensibly", d.ID)
		}
		if len(d.Channels) == 0 {
			t.Errorf("%s: no channel at all", d.ID)
		}
		api := false
		for _, c := range d.Channels {
			switch c {
			case source.ChannelFile:
				if d.DefaultPath == "" {
					t.Errorf("%s reads a file but does not say where that file normally lives", d.ID)
				}
			case source.ChannelAPI:
				api = true
			default:
				t.Errorf("%s: unknown channel %q", d.ID, c)
			}
		}
		if api && len(d.Auth) == 0 {
			t.Errorf("%s is read over an API but declares no way of logging in, so the form offers none", d.ID)
		}
		for _, a := range d.Auth {
			if a != source.AuthLogin && a != source.AuthToken {
				t.Errorf("%s: unknown auth mode %q", d.ID, a)
			}
		}
	}
}

// A credential the panel was never going to accept is refused here, by name,
// rather than deep inside a reader.
func TestCredentialsThePanelDoesNotHaveAreRefused(t *testing.T) {
	_, err := source.Read(t.Context(), "x-ui", source.Options{
		Channel: source.ChannelAPI, BaseURL: "https://example.invalid", Token: "nope",
	})
	if err == nil || !strings.Contains(err.Error(), "no API tokens") {
		t.Fatalf("the classic x-ui line has no tokens; got %v", err)
	}

	_, err = source.Read(t.Context(), "hiddify", source.Options{
		Channel: source.ChannelAPI, BaseURL: "https://example.invalid", Username: "admin", Password: "pw",
	})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("Hiddify takes only an API key; got %v", err)
	}
}
