package remnawave

import (
	"encoding/json"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
)

// Remnawave 3.0 dropped the account's uuid for a numeric id and nested the
// counters under userTraffic. Read as 2.x, every account came out with the
// same bundle id and no usage.
func TestReadsThe3xUserShape(t *testing.T) {
	var u user
	if err := json.Unmarshal([]byte(`{
		"id": 42, "shortUuid": "abc", "username": "ali", "status": "ACTIVE",
		"trafficLimitBytes": 1000, "trafficLimitStrategy": "NO_RESET",
		"expireAt": "2027-01-01T00:00:00.000Z",
		"vlessUuid": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "trojanPassword": "p", "ssPassword": "p",
		"userTraffic": {"usedTrafficBytes": 700, "lifetimeUsedTrafficBytes": 900,
			"onlineAt": "2026-09-20T10:00:00.000Z"}
	}`), &u); err != nil {
		t.Fatal(err)
	}
	it := userItem(u, normalize.NewNamer())
	if it.ID != "client:42" {
		t.Errorf("item id = %q, want client:42", it.ID)
	}
	var body struct {
		Down     int64 `json:"down"`
		OnlineAt int64 `json:"onlineAt"`
	}
	_ = json.Unmarshal(it.Payload, &body)
	if body.Down != 700 || body.OnlineAt == 0 {
		t.Errorf("usage not read from userTraffic: %s", it.Payload)
	}
}
