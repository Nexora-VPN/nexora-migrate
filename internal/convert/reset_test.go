package convert

import (
	"encoding/json"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
)

func newNamer() *normalize.Namer { return normalize.NewNamer() }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Every source panel spells the cycle as a strategy and this tool used to write
// 30 days for "month" — a silent change to what the customer bought, and one
// that keeps moving. These are the spellings the five importers actually pass.
func TestApplyResetStrategy(t *testing.T) {
	cases := []struct {
		in     string
		period string
		days   int
		day    int
		on     bool
	}{
		{"", "", 0, 0, false},
		{"no_reset", "", 0, 0, false},
		{"day", "", 1, 0, true},
		{"DAY", "", 1, 0, true},
		{"daily", "", 1, 0, true},
		{"week", "weekly", 0, 0, true},
		{"WEEK", "weekly", 0, 0, true},
		{"weekly", "weekly", 0, 0, true},
		{"month", "monthly", 0, 1, true},
		{"MONTH", "monthly", 0, 1, true},
		{"monthly", "monthly", 0, 1, true},
		{"month_rolling", "monthly", 0, 1, true},
		{"year", "yearly", 0, 1, true},
		{"  Monthly  ", "monthly", 0, 1, true},
	}
	for _, tc := range cases {
		var c Client
		ApplyResetStrategy(&c, tc.in, 0)
		if c.AutoReset != tc.on || c.ResetPeriod != tc.period || c.ResetDays != tc.days || c.ResetStartDay != tc.day {
			t.Errorf("%q → on=%v period=%q days=%d day=%d, want on=%v period=%q days=%d day=%d",
				tc.in, c.AutoReset, c.ResetPeriod, c.ResetDays, c.ResetStartDay,
				tc.on, tc.period, tc.days, tc.day)
		}
	}
	// A source that knows the anchor passes it, and a nonsense one is clamped.
	var c Client
	ApplyResetStrategy(&c, "month", 15)
	if c.ResetStartDay != 15 {
		t.Errorf("anchor 15 → %d", c.ResetStartDay)
	}
	ApplyResetStrategy(&c, "month", 99)
	if c.ResetStartDay != 1 {
		t.Errorf("anchor 99 → %d, want the 1st", c.ResetStartDay)
	}
}

// TestCalendarCycleClearsTheRollingLength: the two are alternatives, and a
// rolling length left beside a period is only there to be misread.
func TestCalendarCycleClearsTheRollingLength(t *testing.T) {
	c := Client{Name: "u", ResetDays: 30}
	ApplyResetStrategy(&c, "month", 0)
	it := ClientItem(c, newNamer())
	var got map[string]any
	if err := jsonUnmarshal(it.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got["resetPeriod"] != "monthly" {
		t.Errorf("resetPeriod = %v", got["resetPeriod"])
	}
	if got["resetDays"].(float64) != 0 {
		t.Errorf("resetDays = %v, want 0 beside a period", got["resetDays"])
	}
}
