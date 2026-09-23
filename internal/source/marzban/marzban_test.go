package marzban

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/source"
)

const coreConfig = `{"inbounds":[{"tag":"VLESS TCP","protocol":"vless","port":443,
	"settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"tcp"}}],
	"outbounds":[{"protocol":"freedom","tag":"DIRECT"}]}`

// PasarGuard, as v5.4 answers: named cores behind /api/cores and no
// /api/core/config, admins in an envelope with roles, proxy_settings for
// proxies, and an ISO datetime for expire. Read as Marzban, the users page
// failed to decode and the whole import came back empty.
func TestPasarGuardShape(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"access_token": "tok"})
	})
	mux.HandleFunc("GET /api/cores", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"count": 1, "cores": []map[string]any{
			{"id": 1, "name": "main", "type": "xray", "config": json.RawMessage(coreConfig)},
		}})
	})
	mux.HandleFunc("GET /api/admins", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"total": 2, "admins": []map[string]any{
			{"username": "boss", "role": map[string]any{"name": "owner", "is_owner": true}},
			{"username": "seller", "role": map[string]any{"name": "reseller"}},
		}})
	})
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("load_sub") != "true" {
			t.Error("users must be listed with load_sub=true, or subscription_url is empty")
		}
		if r.URL.Query().Get("offset") != "0" {
			writeJSON(w, map[string]any{"users": []any{}, "total": 1})
			return
		}
		writeJSON(w, map[string]any{"total": 1, "users": []map[string]any{{
			"username": "ali", "status": "active", "used_traffic": 10, "data_limit": 1000,
			"expire":           "2027-01-02T03:04:05Z",
			"proxy_settings":   map[string]any{"vless": map[string]any{"id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}},
			"subscription_url": "https://sub.example.com/sub/tok123",
			"admin":            map[string]any{"id": 2, "username": "seller"},
		}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	b, err := reader{flavour: "pasarguard"}.Read(context.Background(), source.Options{
		Channel: source.ChannelAPI, BaseURL: srv.URL, Username: "u", Password: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]bundle.Item{}
	for _, it := range b.Items {
		byID[it.ID] = it
	}
	if _, ok := byID["template:core-1"]; !ok {
		t.Errorf("core 1 did not become a template; items: %v", keys(byID))
	}
	var boss struct{ Role string }
	_ = json.Unmarshal(byID["admin:boss"].Payload, &boss)
	if boss.Role != "admin" {
		t.Errorf("the owner role should arrive as a Nexora admin, got %q", boss.Role)
	}
	ali, ok := byID["client:ali"]
	if !ok {
		t.Fatalf("user not read; items: %v", keys(byID))
	}
	var body struct {
		Expiry int64             `json:"expiry"`
		SubID  string            `json:"subId"`
		Config map[string]string `json:"config"`
	}
	_ = json.Unmarshal(ali.Payload, &body)
	if body.Expiry != 1798859045 || body.SubID != "tok123" ||
		body.Config["uuid"] != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Errorf("user payload = %s", ali.Payload)
	}
	if ali.IDRef["adminId"] != "admin:seller" || len(ali.IDRefs["templateIds"]) != 1 {
		t.Errorf("owner/template refs = %v / %v", ali.IDRef, ali.IDRefs)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func keys(m map[string]bundle.Item) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
