// Package marzneshin reads a Marzneshin installation over its REST API.
//
// Marzneshin is the one panel here that stores no credentials at all. It
// derives every uuid and password from the account's `key` with a hash whose
// algorithm is an install-wide setting (plain, or XXH128), so reading the
// database or the API tells you the key but not what the customer's client is
// actually configured with.
//
// Rather than reimplement somebody's key derivation and hope the install uses
// the default, this reader asks the panel: it fetches each account's own
// subscription and reads the credentials out of the links the panel published.
// That is exact by construction, and it stays exact when upstream changes the
// algorithm.
package marzneshin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/convert"
	"github.com/nexora-vpn/nexora-migrate/internal/httpx"
	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
	"github.com/nexora-vpn/nexora-migrate/internal/source"
)

func init() {
	source.Register(source.Descriptor{
		ID:       "marzneshin",
		Label:    "Marzneshin",
		Channels: []source.Channel{source.ChannelAPI},
		Hint:     "Give the address and a sudo admin's login. Credentials are read from each account's own subscription, because Marzneshin derives rather than stores them.",
		Auth:     []source.AuthMode{source.AuthLogin, source.AuthToken},
	}, reader{})
}

type reader struct{}

type user struct {
	ID              int        `json:"id"`
	Username        string     `json:"username"`
	Key             string     `json:"key"`
	Enabled         bool       `json:"enabled"`
	Activated       bool       `json:"activated"`
	ExpireStrategy  string     `json:"expire_strategy"`
	ExpireDate      *time.Time `json:"expire_date"`
	UsageDuration   *int64     `json:"usage_duration"`
	DataLimit       *int64     `json:"data_limit"`
	ResetStrategy   string     `json:"data_limit_reset_strategy"`
	UsedTraffic     int64      `json:"used_traffic"`
	IPLimit         int        `json:"ip_limit"`
	Note            string     `json:"note"`
	OnlineAt        *time.Time `json:"online_at"`
	SubscriptionURL string     `json:"subscription_url"`
	OwnerUsername   string     `json:"owner_username"`
	ServiceIDs      []int      `json:"service_ids"`
}

func (reader) Read(ctx context.Context, opt source.Options) (*bundle.Bundle, error) {
	c, err := httpx.New(opt.BaseURL, opt.Insecure)
	if err != nil {
		return nil, err
	}
	if opt.Token != "" {
		c.SetBearer(opt.Token)
	} else {
		if opt.Username == "" || opt.Password == "" {
			return nil, fmt.Errorf("this panel needs an admin username and password")
		}
		form := url.Values{}
		form.Set("grant_type", "password")
		form.Set("username", opt.Username)
		form.Set("password", opt.Password)
		var tok struct {
			AccessToken string `json:"access_token"`
		}
		if err := c.Post(ctx, "/api/admins/token", form, &tok); err != nil {
			if httpx.Status(err) == 401 {
				return nil, fmt.Errorf("the panel refused that username and password")
			}
			return nil, fmt.Errorf("could not log in: %w", err)
		}
		c.SetBearer(tok.AccessToken)
	}

	b := &bundle.Bundle{Source: bundle.SourceInfo{Panel: "Marzneshin", Origin: c.Base}}
	b.Note("Marzneshin's subscription link is /sub/<username>/<key> — two path segments, where Nexora resolves one. These accounts get new links; their credentials and quota are kept.")

	admins := readAdmins(ctx, c, b)
	readUsers(ctx, c, b, admins)
	return b, nil
}

func readAdmins(ctx context.Context, c *httpx.Client, b *bundle.Bundle) map[string]string {
	out := map[string]string{}
	var page struct {
		Items []struct {
			Username string `json:"username"`
			IsSudo   bool   `json:"is_sudo"`
			Enabled  bool   `json:"enabled"`
		} `json:"items"`
	}
	if err := c.Get(ctx, "/api/admins?page=1&size=100", &page); err != nil {
		b.Note("the admin list could not be read (%v); accounts arrive without an owner", err)
		return out
	}
	for _, a := range page.Items {
		if a.Username == "" {
			continue
		}
		role := "resale"
		if a.IsSudo {
			role = "admin"
		}
		pw := convert.RandomPassword()
		body, _ := json.Marshal(map[string]any{"username": a.Username, "password": pw, "role": role})
		it := bundle.Item{
			ID: "admin:" + a.Username, Kind: bundle.KindAdmin,
			Name: a.Username, SourceName: a.Username, Payload: body,
		}
		it.Set("role", role)
		it.Set("new password", pw)
		it.AddNote("password hashes cannot move between panels: this admin arrives with the generated password shown here — write it down, it is not stored")
		b.Add(it)
		out[a.Username] = it.ID
	}
	return out
}

func readUsers(ctx context.Context, c *httpx.Client, b *bundle.Bundle, admins map[string]string) {
	names := normalize.NewNamer()
	page, size := 1, 200
	var all []user

	for {
		var resp struct {
			Items []user `json:"items"`
			Total int    `json:"total"`
		}
		path := fmt.Sprintf("/api/users?page=%d&size=%d", page, size)
		if err := c.Get(ctx, path, &resp); err != nil {
			b.Note("reading accounts stopped after %d: %v", len(all), err)
			break
		}
		if len(resp.Items) == 0 {
			break
		}
		all = append(all, resp.Items...)
		if resp.Total > 0 && len(all) >= resp.Total {
			break
		}
		page++
		if ctx.Err() != nil {
			break
		}
	}

	creds := fetchCredentials(ctx, c, all)
	missing := 0
	for _, u := range all {
		it, ok := userItem(b, u, names, admins, creds[u.Username])
		if !ok {
			missing++
		}
		b.Add(it)
	}
	if missing > 0 {
		b.Note("%d account(s) could not have their credentials read from the panel's own subscription output; those are listed as blocked rather than imported with a new uuid, because a new uuid silently breaks a paying customer", missing)
	}
}

// fetchCredentials reads each account's published subscription and parses the
// credentials out of the links. Bounded concurrency: a migration must not
// behave like a load test against a panel that is still serving customers.
func fetchCredentials(ctx context.Context, c *httpx.Client, users []user) map[string]convert.LinkCredentials {
	type result struct {
		username string
		creds    convert.LinkCredentials
		ok       bool
	}
	out := make(map[string]convert.LinkCredentials, len(users))
	if len(users) == 0 {
		return out
	}

	const workers = 4
	jobs := make(chan user)
	results := make(chan result)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range jobs {
				uuid, password, flow, method, _ := subscriptionCredentials(ctx, c, u)
				results <- result{
					username: u.Username,
					creds:    convert.LinkCredentials{UUID: uuid, Password: password, Flow: flow, Method: method},
					ok:       uuid != "" || password != "",
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, u := range users {
			select {
			case jobs <- u:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	for r := range results {
		if r.ok {
			out[r.username] = r.creds
		}
	}
	return out
}

func subscriptionCredentials(ctx context.Context, c *httpx.Client, u user) (uuid, password, flow, method string, ok bool) {
	path := u.SubscriptionURL
	if path == "" {
		path = fmt.Sprintf("/sub/%s/%s", url.PathEscape(u.Username), url.PathEscape(u.Key))
	}
	var raw json.RawMessage
	if err := c.Get(ctx, strings.TrimSuffix(path, "/")+"/v2ray", &raw); err != nil {
		// Older builds serve the links straight off the base path.
		if err2 := c.Get(ctx, path, &raw); err2 != nil {
			return "", "", "", "", false
		}
	}
	links := convert.SplitSubscription(unquote(raw))
	uuid, password, flow, method, _ = convert.MergeLinks(links)
	return uuid, password, flow, method, uuid != "" || password != ""
}

// unquote unwraps a subscription body that came back as a JSON string.
func unquote(raw json.RawMessage) []byte {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []byte(s)
	}
	return raw
}

func userItem(b *bundle.Bundle, u user, names *normalize.Namer, admins map[string]string, creds convert.LinkCredentials) (bundle.Item, bool) {
	c := convert.Client{
		SourceID:      fmt.Sprint(u.ID),
		Name:          u.Username,
		Enable:        u.Enabled,
		Desc:          u.Note,
		Down:          u.UsedTraffic,
		SingleCounter: true,
		UUID:          creds.UUID,
		Password:      creds.Password,
		Flow:          creds.Flow,
		Method:        creds.Method,
	}
	if u.DataLimit != nil {
		c.Volume = *u.DataLimit
	}
	// Marzneshin spells unlimited as -1 for the address cap; Nexora spells it 0.
	if u.IPLimit > 0 {
		c.IPLimit = u.IPLimit
	}
	switch strings.ToLower(u.ExpireStrategy) {
	case "fixed_date":
		if u.ExpireDate != nil {
			c.Expiry = u.ExpireDate.Unix()
		}
	case "start_on_first_use":
		if u.UsageDuration != nil {
			c.Duration = *u.UsageDuration
		}
		if u.Activated && u.ExpireDate != nil {
			// Already running: keep the real date and the start.
			c.Expiry = u.ExpireDate.Unix()
			c.ActivatedAt = u.ExpireDate.Add(-time.Duration(c.Duration) * time.Second).Unix()
			c.Duration = 0
		}
	}
	convert.ApplyResetStrategy(&c, u.ResetStrategy, 0)
	if u.OnlineAt != nil {
		c.OnlineAt = u.OnlineAt.Unix()
	}
	if u.OwnerUsername != "" {
		c.Group = u.OwnerUsername
		if id, ok := admins[u.OwnerUsername]; ok {
			c.OwnerItem = id
		}
	}

	it := convert.ClientItem(c, names)
	it.Set("services", fmt.Sprint(len(u.ServiceIDs)))
	it.Set("old link", u.SubscriptionURL)
	if c.UUID == "" && c.Password == "" {
		it.Block("the panel's own subscription for this account could not be read, so its real credentials are unknown — importing it would hand the customer a new uuid without telling them")
		return it, false
	}
	return it, true
}
