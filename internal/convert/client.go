package convert

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
)

// Client is the shape every source reader reduces one account to. It is
// deliberately Nexora's vocabulary rather than any source panel's, so the
// awkward parts of each panel — a single traffic counter, unlimited spelled as
// -1 or null, a lifetime that has not started — are resolved inside the reader
// where the panel's own semantics are known, not later where they are not.
type Client struct {
	// SourceID makes the bundle item id stable across re-runs.
	SourceID string
	Name     string
	Enable   bool

	// One credential set, which is all Nexora keeps. A source that stores
	// credentials per protocol resolves the conflict before filling these in
	// and records what it discarded in Conflicts.
	UUID     string
	Password string
	Flow     string
	Method   string
	// Conflicts names the protocols whose credentials could not be carried.
	Conflicts []string

	// Quota and lifetime. Zero means unlimited / never, exactly as in Nexora,
	// so a reader must normalise -1 and null before setting these.
	Volume int64
	Expiry int64 // unix seconds
	// Duration turns Expiry into a length that starts at first use. Set it only
	// when the source really has that shape; ActivatedAt carries the start over
	// for an account that has already begun.
	Duration    int64
	ActivatedAt int64

	Up   int64
	Down int64
	// SingleCounter marks a source that only kept one total. Everything lands
	// in Down and the item says so.
	SingleCounter bool

	SpeedLimit int64
	IPLimit    int

	// The periodic reset, in Nexora's own shape: an empty ResetPeriod is the
	// rolling cycle measured in ResetDays, and a calendar cycle
	// ("weekly"/"monthly"/"yearly") is anchored to ResetStartDay.
	//
	// Every source panel spells "monthly" as a strategy rather than a day
	// count, and this tool used to write 30 days for it — which moves every
	// migrated customer's reset day and keeps moving it, about five days a
	// year. Nexora has calendar periods now, so the mapping is exact.
	AutoReset     bool
	ResetDays     int
	ResetPeriod   string
	ResetStartDay int

	// SubID carries the old panel's subscription token. Nexora resolves
	// /sub/{id} against sub_id as well as sub_token, so an old link keeps
	// working once the hostname points here.
	SubID string
	// SubURL is the full old link, shown in the review table so an operator can
	// see what continuity they are getting.
	SubURL string

	Group    string
	Desc     string
	Remark   string
	OnlineAt int64

	// Contact is the customer's own details on the Nexora side: telegram_id,
	// email and phone, plus whatever else a source keeps about a person. Every
	// panel this tool reads has *somewhere* to write a customer's email or
	// Telegram id, and until Nexora had a column for it the importer read the
	// field, showed it in the review table and then dropped it — the operator's
	// contact list, lost in the one step that was supposed to preserve
	// everything. Written with SetContact so an empty value is never a key.
	Contact map[string]string

	// OwnerItem is the bundle id of the admin that owns this account, if the
	// source has resellers and that admin is in the bundle.
	OwnerItem string
	// TemplateItems are the bundle ids of the templates the account should be
	// provisioned on. Empty means every template.
	TemplateItems []string
}

// ClientItem turns a Client into the bundle item that will be POSTed to
// /api/users. namer is the shared namespace for this bundle's client names.
func ClientItem(c Client, namer *normalize.Namer) bundle.Item {
	res := namer.Name(c.Name, "client-"+c.SourceID)

	cred := map[string]string{}
	setStr(cred, "uuid", c.UUID)
	setStr(cred, "password", c.Password)
	setStr(cred, "flow", c.Flow)
	setStr(cred, "method", c.Method)
	credRaw, _ := json.Marshal(cred)

	payload := map[string]any{
		"name":       res.Name,
		"enable":     c.Enable,
		"config":     json.RawMessage(credRaw),
		"desc":       c.Desc,
		"remark":     c.Remark,
		"group":      c.Group,
		"volume":     c.Volume,
		"expiry":     c.Expiry,
		"duration":   c.Duration,
		"up":         c.Up,
		"down":       c.Down,
		"speedLimit": c.SpeedLimit,
		"ipLimit":    c.IPLimit,
		"autoReset":  c.AutoReset,
		"resetDays":  c.ResetDays,
		"onlineAt":   c.OnlineAt,
	}
	if len(c.Contact) > 0 {
		payload["contact"] = c.Contact
	}
	if c.ResetPeriod != "" {
		payload["resetPeriod"] = c.ResetPeriod
		payload["resetStartDay"] = c.ResetStartDay
		// The rolling length means nothing beside a period, and a stored one
		// would only be there to be misread later.
		payload["resetDays"] = 0
	}
	if c.ActivatedAt > 0 {
		payload["activatedAt"] = c.ActivatedAt
	}
	if c.SubID != "" {
		payload["subId"] = c.SubID
	}
	if len(c.TemplateItems) == 0 {
		// No template selected means the account goes everywhere, which is what
		// an operator migrating a whole panel wants and what Nexora's create
		// route does by default.
		payload["allTemplates"] = true
	} else {
		payload["allTemplates"] = false
		payload["templateIds"] = []uint{}
	}
	raw, _ := json.Marshal(payload)

	it := bundle.Item{
		ID:         "client:" + c.SourceID,
		Kind:       bundle.KindClient,
		Name:       res.Name,
		SourceName: c.Name,
		Payload:    raw,
	}
	if c.Group != "" {
		it.Group = []string{c.Group}
	}
	if len(c.TemplateItems) > 0 {
		it.IDRefs = map[string][]string{"templateIds": c.TemplateItems}
	}
	if c.OwnerItem != "" {
		it.IDRef = map[string]string{"adminId": c.OwnerItem}
	}

	// The review table.
	it.Set("quota", bytesLabel(c.Volume))
	it.Set("used", bytesLabel(c.Up+c.Down))
	it.Set("expires", expiryLabel(c))
	it.Set("enabled", yesNo(c.Enable))
	if c.UUID != "" {
		it.Set("uuid", c.UUID)
	}
	if c.SubURL != "" {
		it.Set("old link", c.SubURL)
	}
	if c.SubID != "" {
		it.Set("sub id", c.SubID)
	}

	if res.Changed {
		it.AddNote("%s", res.Note)
	}
	if len(c.Conflicts) > 0 {
		it.AddNote("Nexora keeps one credential set per account; these protocols had their own and will change for this user: %s",
			strings.Join(c.Conflicts, ", "))
	}
	if c.SingleCounter && (c.Down > 0) {
		it.AddNote("the old panel kept one traffic total, not upload and download separately — all %s is recorded as download",
			bytesLabel(c.Down))
	}
	if c.SubID == "" {
		it.AddNote("no subscription token came across, so this user gets a new link")
	}
	return it
}

func setStr(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func expiryLabel(c Client) string {
	switch {
	case c.Duration > 0 && c.ActivatedAt == 0:
		return fmt.Sprintf("%d days, starts on first use", c.Duration/86400)
	case c.Expiry == 0:
		return "never"
	default:
		return time.Unix(c.Expiry, 0).UTC().Format("2006-01-02")
	}
}

// bytesLabel renders a byte count the way an operator reads it, and says
// "unlimited" for the zero that means exactly that everywhere in Nexora.
func bytesLabel(n int64) string {
	if n == 0 {
		return "unlimited"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// GBToBytes converts a gigabyte figure (Hiddify keeps quotas that way) into
// bytes, rounding to the nearest byte.
func GBToBytes(gb float64) int64 {
	if gb <= 0 {
		return 0
	}
	return int64(gb * 1024 * 1024 * 1024)
}

// MillisToUnix converts a millisecond timestamp (3x-ui's expiryTime) into unix
// seconds. A negative value is 3x-ui's "this many milliseconds after first
// use", which the caller handles instead.
func MillisToUnix(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms / 1000
}

// ApplyResetStrategy maps a source panel's reset strategy onto Nexora's cycle.
//
// Every panel this tool reads spells the cycle as a strategy — day / week /
// month / year, however each one capitalises it — and until Nexora had calendar
// periods the only thing to write was a day count, so "month" became 30 days.
// That is a silent change to what the customer bought: the reset day walks
// about five days a year, and by the second year it is in the middle of a
// different billing month. The mapping is now exact, and only "day" is still a
// rolling cycle, because a day is a day in every calendar.
//
// The anchor is the day the account's cycle should turn on. A source that does
// not say gets the 1st, which is what "monthly" means to the operator selling
// it; an importer that knows better (a stored anchor, or the day the last reset
// happened) passes it.
func ApplyResetStrategy(c *Client, strategy string, anchorDay int) {
	if anchorDay < 1 || anchorDay > 31 {
		anchorDay = 1
	}
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "day", "daily":
		c.AutoReset, c.ResetDays = true, 1
	case "week", "weekly":
		c.AutoReset, c.ResetPeriod, c.ResetStartDay = true, "weekly", 0
	case "month", "monthly", "month_rolling":
		c.AutoReset, c.ResetPeriod, c.ResetStartDay = true, "monthly", anchorDay
	case "year", "yearly", "annual":
		c.AutoReset, c.ResetPeriod, c.ResetStartDay = true, "yearly", anchorDay
	}
}

// SetContact records one of the customer's details, ignoring an empty value so
// a source that simply has no email does not write the key. The standard keys
// are model.ContactTelegramID / Email / Phone on the panel side; this package
// deliberately does not import the panel, so they are spelled here.
func SetContact(c *Client, key, value string) {
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	if c.Contact == nil {
		c.Contact = map[string]string{}
	}
	c.Contact[key] = value
}

// The keys the panel itself reads (filters, CSV columns, the user form).
const (
	ContactTelegramID = "telegram_id"
	ContactEmail      = "email"
	ContactPhone      = "phone"
)
