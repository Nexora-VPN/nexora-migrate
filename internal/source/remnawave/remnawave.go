// Package remnawave reads a Remnawave installation over its REST API.
//
// Remnawave runs on Postgres and wraps every response in a `response` envelope.
// It is the friendliest of these panels to read: it stores the three
// credentials an account uses — vlessUuid, trojanPassword, ssPassword — as
// plain columns, so nothing has to be derived or parsed out of a link.
//
// Its subscription URL is /api/sub/{shortUuid}: one segment, but behind a
// fixed prefix Nexora does not serve. The short uuid is carried into Nexora's
// sub id anyway, so the account is reachable at /sub/{shortUuid} — the token
// survives even though the full old URL does not.
package remnawave

import (
	"context"
	"encoding/json"
	"fmt"
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
		ID:       "remnawave",
		Label:    "Remnawave",
		Channels: []source.Channel{source.ChannelAPI},
		Hint:     "Give the panel address and either an admin login or an API token. Remnawave runs on Postgres, so the API is the channel.",
		Auth:     []source.AuthMode{source.AuthLogin, source.AuthToken},
	}, reader{})
}

type reader struct{}

// user is one account as /api/users lists it. Remnawave 3.0 dropped `uuid`
// for a numeric `id` and moved the traffic counters and onlineAt into a nested
// `userTraffic`; both spellings are read, so a 2.x panel reads the same.
type user struct {
	UUID                 string      `json:"uuid"`
	ID                   json.Number `json:"id"`
	ShortUUID            string      `json:"shortUuid"`
	Username             string      `json:"username"`
	Status               string      `json:"status"`
	TrafficLimitBytes    int64       `json:"trafficLimitBytes"`
	UsedTrafficBytes     int64       `json:"usedTrafficBytes"`
	LifetimeUsedTraffic  int64       `json:"lifetimeUsedTrafficBytes"`
	TrafficLimitStrategy string      `json:"trafficLimitStrategy"`
	ExpireAt             *time.Time  `json:"expireAt"`
	Description          string      `json:"description"`
	Tag                  string      `json:"tag"`
	Email                string      `json:"email"`
	// The customer's Telegram id, when the install fills it in. Read as a
	// number or a string, because the API has spelled it both ways.
	TelegramID      json.Number `json:"telegramId"`
	HwidDeviceLimit *int        `json:"hwidDeviceLimit"`
	TrojanPassword  string      `json:"trojanPassword"`
	VlessUUID       string      `json:"vlessUuid"`
	SsPassword      string      `json:"ssPassword"`
	OnlineAt        *time.Time  `json:"onlineAt"`
	SubscriptionURL string      `json:"subscriptionUrl"`
	UserTraffic     *struct {
		UsedTrafficBytes int64      `json:"usedTrafficBytes"`
		OnlineAt         *time.Time `json:"onlineAt"`
	} `json:"userTraffic"`
}

// sourceID is the account's stable id across re-reads: the uuid a 2.x panel
// gave, else the id a 3.x one does, else the short uuid both have.
func (u user) sourceID() string {
	for _, id := range []string{u.UUID, u.ID.String(), u.ShortUUID} {
		if id != "" {
			return id
		}
	}
	return u.Username
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
			return nil, fmt.Errorf("this panel needs an admin username and password, or an API token")
		}
		var resp struct {
			Response struct {
				AccessToken string `json:"accessToken"`
			} `json:"response"`
		}
		body := map[string]string{"username": opt.Username, "password": opt.Password}
		if err := c.Post(ctx, "/api/auth/login", body, &resp); err != nil {
			if httpx.Status(err) == 401 {
				return nil, fmt.Errorf("the panel refused that username and password")
			}
			return nil, fmt.Errorf("could not log in: %w", err)
		}
		if resp.Response.AccessToken == "" {
			return nil, fmt.Errorf("the panel accepted the login but returned no token")
		}
		c.SetBearer(resp.Response.AccessToken)
	}

	b := &bundle.Bundle{Source: bundle.SourceInfo{Panel: "Remnawave", Origin: c.Base}}
	b.Note("Remnawave serves subscriptions at /api/sub/<short uuid>. The short uuid is carried into Nexora's sub id, so each account keeps its token at /sub/<short uuid> — but the old full URL will not resolve, because its /api/sub prefix is Remnawave's.")
	b.Note("Remnawave's inbounds live in config profiles and internal squads rather than one engine document; those are not converted here, so build the inbounds in Nexora first and the accounts will attach to them.")

	if err := readUsers(ctx, c, b); err != nil {
		return nil, err
	}
	return b, nil
}

func readUsers(ctx context.Context, c *httpx.Client, b *bundle.Bundle) error {
	names := normalize.NewNamer()
	start, size, seen := 0, 250, 0

	for {
		var resp struct {
			Response struct {
				Users []user          `json:"users"`
				Total json.RawMessage `json:"total"`
			} `json:"response"`
		}
		path := fmt.Sprintf("/api/users?start=%d&size=%d", start, size)
		if err := c.Get(ctx, path, &resp); err != nil {
			if seen == 0 {
				return fmt.Errorf("could not list accounts: %w", err)
			}
			b.Note("reading accounts stopped after %d: %v", seen, err)
			return nil
		}
		users := resp.Response.Users
		if len(users) == 0 {
			return nil
		}
		for _, u := range users {
			b.Add(userItem(u, names))
			seen++
		}
		if len(users) < size {
			return nil
		}
		start += len(users)
		if ctx.Err() != nil {
			b.Note("reading accounts was cancelled after %d", seen)
			return nil
		}
	}
}

func userItem(u user, names *normalize.Namer) bundle.Item {
	c := convert.Client{
		SourceID:      u.sourceID(),
		Name:          u.Username,
		Desc:          strings.TrimSpace(u.Description),
		Enable:        !strings.EqualFold(u.Status, "DISABLED"),
		Volume:        u.TrafficLimitBytes,
		Down:          u.UsedTrafficBytes,
		SingleCounter: true,
		UUID:          u.VlessUUID,
		// Trojan and Shadowsocks hold different secrets here and Nexora keeps
		// one password, so Trojan wins and Shadowsocks is reported.
		Password: u.TrojanPassword,
		SubID:    u.ShortUUID,
		SubURL:   u.SubscriptionURL,
		Group:    u.Tag,
	}
	if u.SsPassword != "" && u.SsPassword != u.TrojanPassword {
		c.Conflicts = append(c.Conflicts, "shadowsocks")
	}
	if u.ExpireAt != nil && u.ExpireAt.Year() > 1980 {
		c.Expiry = u.ExpireAt.Unix()
	}
	if t := u.UserTraffic; t != nil {
		c.Down = t.UsedTrafficBytes
		if t.OnlineAt != nil {
			u.OnlineAt = t.OnlineAt
		}
	}
	if u.OnlineAt != nil {
		c.OnlineAt = u.OnlineAt.Unix()
	}
	convert.ApplyResetStrategy(&c, u.TrafficLimitStrategy, 0)
	// The customer's own details. This panel has had them all along; until
	// Nexora had a column for them the importer read the email, showed it in
	// the review table and then threw it away.
	convert.SetContact(&c, convert.ContactEmail, u.Email)
	convert.SetContact(&c, convert.ContactTelegramID, u.TelegramID.String())

	it := convert.ClientItem(c, names)
	it.Set("status", u.Status)
	if u.HwidDeviceLimit != nil && *u.HwidDeviceLimit > 0 {
		it.Set("device limit", fmt.Sprint(*u.HwidDeviceLimit))
		// Nexora has the same cap, and it is worth carrying.
		it.Payload = withField(it.Payload, "deviceLimit", *u.HwidDeviceLimit)
	}
	if u.Email != "" {
		it.Set("email", u.Email)
	}
	if u.TelegramID.String() != "" {
		it.Set("telegram", u.TelegramID.String())
	}
	if c.UUID == "" && c.Password == "" {
		it.Block("this account has no credentials in the panel")
	}
	return it
}

// withField adds one field to an already-encoded payload.
func withField(payload json.RawMessage, key string, value any) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return payload
	}
	m[key] = value
	out, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return out
}
