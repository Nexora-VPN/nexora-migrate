// Package target is the Nexora side: the client that logs in, checks what the
// panel can accept, and creates the items an operator picked.
//
// Everything is written through the panel's public REST API and nothing else.
// That is a deliberate constraint, not a convenience: the panel owns the
// subscription token it generates, template membership, the live node
// reconciliation, the audit trail, the licence cap and reseller ownership.
// Writing rows into its database would reproduce none of those and would break
// on the next schema change.
package target

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/nexora-vpn/nexora-migrate/internal/httpx"
)

// Options is how to reach the Nexora panel.
type Options struct {
	BaseURL  string `json:"baseUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
	// Code is the current authenticator code, for an account with two-factor
	// sign-in on. The password alone earns such an account no session.
	Code     string `json:"code"`
	Insecure bool   `json:"insecure"`
}

// Client is a connected Nexora panel.
type Client struct {
	http *httpx.Client
	Me   Identity
}

// Identity is who we are connected as.
type Identity struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Token    bool   `json:"token"`
	// MFA is set when the session was opened with a second factor. The panel
	// asks such a session for a fresh code before a sensitive write (creating
	// an admin is one) once ten minutes have passed since the last one.
	MFA bool `json:"mfa"`
}

// Connect logs in (or uses the given API token) and reads back who we are.
func Connect(ctx context.Context, opt Options) (*Client, error) {
	c, err := httpx.New(opt.BaseURL, opt.Insecure)
	if err != nil {
		return nil, err
	}
	mfa := false
	if opt.Token != "" {
		c.SetBearer(opt.Token)
	} else {
		if opt.Username == "" || opt.Password == "" {
			return nil, fmt.Errorf("give the panel a username and password, or an API token")
		}
		var out struct {
			Token       string `json:"token"`
			MFARequired bool   `json:"mfaRequired"`
			Pending     string `json:"pending"`
		}
		body := map[string]string{"username": opt.Username, "password": opt.Password}
		if err := c.Post(ctx, "/api/login", body, &out); err != nil {
			if httpx.Status(err) == http.StatusUnauthorized {
				return nil, fmt.Errorf("the panel refused that username and password")
			}
			return nil, fmt.Errorf("could not log in to Nexora: %w", err)
		}
		// An account with two-factor sign-in answers the password with a
		// short-lived pending token, and the session comes from the second step.
		if out.MFARequired {
			code := strings.Join(strings.Fields(opt.Code), "")
			if code == "" {
				return nil, ErrCodeRequired
			}
			second := map[string]string{"pending": out.Pending, "code": code}
			if err := c.Post(ctx, "/api/login/mfa", second, &out); err != nil {
				if httpx.Status(err) == http.StatusUnauthorized {
					return nil, fmt.Errorf("the panel refused that authenticator code: %w", err)
				}
				return nil, fmt.Errorf("could not finish the two-factor login: %w", err)
			}
			mfa = true
		}
		if out.Token == "" {
			return nil, fmt.Errorf("the panel accepted the login but returned no token")
		}
		c.SetBearer(out.Token)
	}

	cl := &Client{http: c}
	if err := c.Get(ctx, "/api/me", &cl.Me); err != nil {
		return nil, fmt.Errorf("connected, but the panel would not say who we are: %w", err)
	}
	cl.Me.MFA = mfa
	return cl, nil
}

// ErrCodeRequired is the login of an account with two-factor sign-in on, made
// without a code. The wizard answers it by asking for one.
var ErrCodeRequired = errors.New("this account has two-factor sign-in on: enter the current code from its authenticator app, or connect with an API token")

// Confirm presents a fresh authenticator code, which renews the session's
// confirmation for the sensitive writes that ask for one.
func (c *Client) Confirm(ctx context.Context, code string) error {
	code = strings.Join(strings.Fields(code), "")
	if err := c.http.Post(ctx, "/api/me/confirm", map[string]string{"code": code}, nil); err != nil {
		if httpx.Status(err) == http.StatusUnauthorized {
			return fmt.Errorf("the panel refused that authenticator code: %w", err)
		}
		return fmt.Errorf("the panel would not take the authenticator code: %w", err)
	}
	return nil
}

// Base is the panel address, for display.
func (c *Client) Base() string { return c.http.Base }

// ResourceUsage is one row of the licence's used/allowed table.
type ResourceUsage struct {
	Resource string `json:"resource"`
	Used     int64  `json:"used"`
	Max      int    `json:"max"`
}

// Preflight is everything worth knowing before writing anything.
type Preflight struct {
	Identity Identity        `json:"identity"`
	Licence  []ResourceUsage `json:"licence"`
	// LicenceReadable is false for a non-sudo login, which may not read it.
	LicenceReadable bool `json:"licenceReadable"`

	// Existing holds the names and tags the panel already has, per kind, so the
	// wizard can warn about a collision before it is a 409.
	Existing map[string][]string `json:"existing"`

	Nodes []Node `json:"nodes"`
}

// Node is one node of the fleet, with the facts the import cares about.
type Node struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// LimitDisabled is a node its periodic usage limit switched off. It is
	// left alone: switching it back on clears that state, and it would come
	// back up over its limit until the next check.
	LimitDisabled bool  `json:"limitDisabled"`
	Template      *uint `json:"templateId"`
}

// Pausable reports whether the import should switch this node off for the
// run: it is on, and on by the operator's choice rather than despite a limit.
func (n Node) Pausable() bool { return n.Enabled && !n.LimitDisabled }

// Headroom reports how many more of a resource the licence allows. ok is false
// when the licence could not be read, and -1 means unlimited.
func (p Preflight) Headroom(resource string) (n int64, ok bool) {
	for _, u := range p.Licence {
		if u.Resource == resource {
			if u.Max <= 0 {
				return -1, true
			}
			return int64(u.Max) - u.Used, true
		}
	}
	return 0, false
}

// Check reads the panel's state without changing any of it.
func (c *Client) Check(ctx context.Context) (Preflight, error) {
	p := Preflight{Identity: c.Me, Existing: map[string][]string{}}

	var lic struct {
		Usage []ResourceUsage `json:"usage"`
	}
	if err := c.http.Get(ctx, "/api/license", &lic); err == nil {
		p.Licence, p.LicenceReadable = lic.Usage, true
	}

	for kind, path := range map[string]string{
		"client":   "/api/users?limit=100000&fields=name",
		"inbound":  "/api/inbounds",
		"outbound": "/api/outbounds",
		"endpoint": "/api/endpoints",
		"ruleset":  "/api/rule-sets",
		"template": "/api/templates",
		"admin":    "/api/admins",
	} {
		names, err := c.names(ctx, path)
		if err != nil {
			// A reseller cannot read the pools; that is not fatal, it only
			// means we cannot warn about collisions there.
			continue
		}
		p.Existing[kind] = names
	}

	var nodes []Node
	if err := c.http.Get(ctx, "/api/nodes", &nodes); err == nil {
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
		p.Nodes = nodes
	}
	return p, nil
}

// names lists the name (or tag) of every row at a list endpoint, tolerating
// both the bare array the unversioned routes return and the {items,total}
// envelope the versioned ones do.
func (c *Client) names(ctx context.Context, path string) ([]string, error) {
	var raw json.RawMessage
	if err := c.http.Get(ctx, path, &raw); err != nil {
		return nil, err
	}
	rows, err := decodeList(raw)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		for _, key := range []string{"name", "tag", "username"} {
			if s, ok := r[key].(string); ok && s != "" {
				out = append(out, s)
				break
			}
		}
	}
	return out, nil
}

func decodeList(raw json.RawMessage) ([]map[string]any, error) {
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) == nil {
		return arr, nil
	}
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if json.Unmarshal(raw, &env) == nil {
		return env.Items, nil
	}
	return nil, fmt.Errorf("the panel returned a list in a shape this tool does not recognise")
}

// Create posts one item and returns the id the panel assigned it.
func (c *Client) Create(ctx context.Context, path string, payload any) (uint, error) {
	var out struct {
		ID uint `json:"id"`
	}
	if err := c.http.Post(ctx, path, payload, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// SetNodeEnabled turns one node on or off.
//
// This is what makes an import of thousands of accounts survivable without any
// change to the panel. Nexora has no bulk create: every POST /api/users ends in
// a reconcile of every node the account lands on, so twenty thousand accounts
// across three nodes would be sixty thousand full user-list pushes. With the
// nodes disabled for the duration it is zero, and one sync each at the end
// brings the whole fleet up to date.
func (c *Client) SetNodeEnabled(ctx context.Context, id uint, enabled bool) error {
	return c.http.Post(ctx, fmt.Sprintf("/api/nodes/%d/enabled", id),
		map[string]bool{"enabled": enabled}, nil)
}

// SyncNode pushes the panel's current state to one node.
func (c *Client) SyncNode(ctx context.Context, id uint) error {
	return c.http.Post(ctx, fmt.Sprintf("/api/nodes/%d/sync", id), nil, nil)
}

// Delete removes one created row, used by the rollback of a failed run.
func (c *Client) Delete(ctx context.Context, path string, id uint) error {
	return c.http.Do(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", path, id), nil, nil)
}

// IsConflict reports whether an error is the panel refusing a duplicate name or
// tag, which an import hits often enough to deserve its own message.
func IsConflict(err error) bool {
	if httpx.Status(err) == http.StatusConflict {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// IsLicenceRefusal reports whether the panel refused because the licence is
// full — a 402, which is worth stopping the whole run for rather than
// repeating a few thousand times.
func IsLicenceRefusal(err error) bool {
	return httpx.Status(err) == http.StatusPaymentRequired
}

// IsConfirmRequired reports whether the panel refused a sensitive write
// because a two-factor session's last code is too old — a 428.
func IsConfirmRequired(err error) bool {
	return httpx.Status(err) == http.StatusPreconditionRequired
}
