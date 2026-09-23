package dbfetch

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/nexora-vpn/nexora-migrate/internal/httpx"
	"github.com/nexora-vpn/nexora-migrate/internal/source"
)

// loginSUI authenticates against s-ui and returns the endpoints to try.
//
// s-ui has two doors. The v2 API takes a token in a `Token` header and is the
// one to prefer: it is a stable, documented surface with no session and no
// same-origin check. The v1 API is the panel's own browser session — a form
// login at /api/login — and is there for operators who have not minted a token.
func loginSUI(ctx context.Context, c *httpx.Client, opt source.Options) ([]attempt, error) {
	if tok := strings.TrimSpace(opt.Token); tok != "" {
		return []attempt{{path: "/apiv2/getdb", headers: map[string]string{"Token": tok}}}, nil
	}
	if opt.Username == "" {
		return nil, fmt.Errorf("s-ui needs either an API token or an admin username and password")
	}
	// The v1 API refuses state-changing requests that did not come from the
	// panel's own pages. A cross-site form post cannot set a custom header, so
	// this is the marker it accepts in place of a matching Origin.
	c.Headers["X-Requested-With"] = "XMLHttpRequest"

	form := url.Values{"user": {opt.Username}, "pass": {opt.Password}}
	var msg jsonMsg
	if err := c.Post(ctx, "/api/login", form, &msg); err != nil {
		return nil, loginError("s-ui", err)
	}
	if !msg.Success {
		return nil, msg.err("s-ui refused the login")
	}
	return []attempt{{path: "/api/getdb"}}, nil
}

// loginXUI authenticates against 3x-ui or the classic x-ui line.
//
// The two differ in three places and are otherwise the same panel: 3x-ui has
// API tokens and a CSRF token on its login, the classic line has neither, and
// they hang the backup endpoint off different prefixes. Which prefixes exist
// depends on the release rather than the fork — 3x-ui moved /server/getDb under
// /panel/api in v3, alireza0's line serves both — so every known path is tried
// in turn and the first database wins.
func loginXUI(ctx context.Context, c *httpx.Client, opt source.Options, threeX bool) ([]attempt, error) {
	paths := []attempt{
		{path: "/panel/api/server/getDb"},
		{path: "/server/getDb"},
		{path: "/xui/API/server/getDb"},
		// A 3x-ui on PostgreSQL (v3.8+) answers getDb with a pg_dump archive,
		// which is not a database this tool reads; its migration export is a
		// SQLite file on that engine. Last, so a SQLite install never gets
		// here: there the same route answers with a text dump.
		{path: "/panel/api/server/getMigration"},
	}
	if !threeX {
		// The classic line serves /server/getDb directly and only mirrors it
		// under its own API prefix; /panel/api is 3x-ui's shape.
		paths = []attempt{
			{path: "/server/getDb"},
			{path: "/xui/API/server/getDb"},
			{path: "/panel/api/server/getDb"},
		}
	}

	if tok := strings.TrimSpace(opt.Token); tok != "" {
		if !threeX {
			return nil, fmt.Errorf("the classic x-ui line has no API tokens — give an admin username and password instead")
		}
		c.SetBearer(tok)
		return paths, nil
	}
	if opt.Username == "" {
		name := "x-ui"
		if threeX {
			name = "3x-ui"
		}
		return nil, fmt.Errorf("%s needs an admin username and password", name)
	}

	c.Headers["X-Requested-With"] = "XMLHttpRequest"

	// 3x-ui v3 guards its login with a session CSRF token. Older builds and the
	// classic line have no such route, so a miss here is not a failure — it is
	// how we find out which generation we are talking to.
	if threeX {
		var tok jsonMsg
		if err := c.Get(ctx, "/csrf-token", &tok); err == nil && tok.Success {
			var value string
			if len(tok.Obj) > 0 {
				_ = jsonUnquote(tok.Obj, &value)
			}
			if value != "" {
				c.Headers["X-CSRF-Token"] = value
			}
		}
	}

	var body any = url.Values{"username": {opt.Username}, "password": {opt.Password}}
	if threeX {
		body = map[string]string{
			"username":      opt.Username,
			"password":      opt.Password,
			"twoFactorCode": opt.TwoFactor,
		}
	}
	var msg jsonMsg
	if err := c.Post(ctx, "/login", body, &msg); err != nil {
		return nil, loginError(panelName(threeX), err)
	}
	if !msg.Success {
		return nil, msg.err(panelName(threeX) + " refused the login")
	}
	return paths, nil
}

func panelName(threeX bool) string {
	if threeX {
		return "3x-ui"
	}
	return "x-ui"
}

// loginError says which of the two usual causes it was, because "404" on a
// login route means the address is missing the panel's secret path far more
// often than it means the panel is broken.
func loginError(panel string, err error) error {
	if httpx.Status(err) == 404 {
		// The body of a 404 is whatever the web server felt like serving — an
		// error page, usually — and pasting it here buries the one thing the
		// operator has to do.
		return fmt.Errorf(
			"%s has no login at this address: it answered 404. These panels are usually installed on a secret base path, so paste the address exactly as you open it in your browser, including that path",
			panel)
	}
	return fmt.Errorf("could not log into %s: %w", panel, err)
}
