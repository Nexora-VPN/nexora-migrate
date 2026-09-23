package xui

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/source"
)

// fixtureV37 adds what 3x-ui v3.7 changed to the base fixture: WireGuard peers
// stored as clients (the migration deletes `peers`) with a traffic row each,
// and the calendar renewal day and per-client traffic cycle.
func fixtureV37(t *testing.T) string {
	t.Helper()
	path := fixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
			VALUES (4,'wg2',1,'',51821,'wireguard',?,'{}','inbound-wg2','')`,
			[]any{`{"secretKey":"WGKEY2","peers":[],"clients":[{"email":"wg-peer","publicKey":"PUB2","allowedIPs":["10.0.0.3/32"],"enable":true}]}`}},
		{`INSERT INTO client_traffics (inbound_id, enable, email, up, down, expiry_time, total, reset, reset_day)
			VALUES (4,1,'wg-peer',1,1,0,0,0,0)`, nil},
		{`INSERT INTO inbounds (id, remark, enable, listen, port, protocol, settings, stream_settings, tag, sniffing)
			VALUES (5,'v37',1,'',9443,'vless',?,'{"network":"tcp"}','inbound-9443','')`,
			[]any{`{"clients":[
				{"id":"dddddddd-dddd-dddd-dddd-dddddddddddd","email":"renew","reset":30,"resetDay":15,"enable":true},
				{"id":"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee","email":"weekly","trafficReset":"weekly","enable":true}
			],"decryption":"none"}`}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	return path
}

func TestV37Shapes(t *testing.T) {
	b, err := source.Read(context.Background(), "3x-ui", source.Options{
		Channel: source.ChannelFile, Path: fixtureV37(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := payload(t, find(t, b, "endpoint:4"))["config"].(map[string]any)
	if peers, _ := cfg["peers"].([]any); len(peers) != 1 {
		t.Errorf("peers stored as clients were not read: %v", cfg)
	}
	for _, it := range b.Items {
		if it.ID == "client:wg-peer" {
			t.Error("a WireGuard peer must not also arrive as a credential-less account")
		}
	}

	renew := payload(t, find(t, b, "client:renew"))
	if renew["resetPeriod"] != "monthly" || renew["resetStartDay"] != float64(15) {
		t.Errorf("a renewal day is a monthly cycle on that day: %v", renew)
	}
	weekly := payload(t, find(t, b, "client:weekly"))
	if weekly["resetPeriod"] != "weekly" {
		t.Errorf("trafficReset weekly = %v", weekly)
	}
}
