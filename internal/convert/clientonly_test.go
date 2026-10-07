package convert

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDropClientOnly(t *testing.T) {
	route, dropped := DropClientOnlyRoute(json.RawMessage(`{"final":"direct","find_process":true,"rules":[` +
		`{"process_name":["curl"],"outbound":"block"},` +
		`{"type":"logical","mode":"and","rules":[{"domain":["a"]},{"wifi_ssid":["x"]}],"outbound":"block"},` +
		`{"domain":["b"],"outbound":"direct"}]}`))
	for _, gone := range []string{"find_process", "process_name", "wifi_ssid", `"block"`} {
		if strings.Contains(string(route), gone) {
			t.Errorf("%s survived: %s", gone, route)
		}
	}
	if !strings.Contains(string(route), `"domain":["b"]`) {
		t.Errorf("a clean rule went: %s", route)
	}
	if len(dropped) != 3 {
		t.Errorf("dropped = %v, want three entries", dropped)
	}

	dns, dropped := DropClientOnlyDNS(json.RawMessage(`{"servers":[{"type":"local","tag":"l","neighbor_domain":["lan"]}],"rules":[{"clash_mode":"global","server":"l"}]}`))
	if strings.Contains(string(dns), "neighbor_domain") || strings.Contains(string(dns), "clash_mode") || len(dropped) != 2 {
		t.Errorf("dns = %s, dropped = %v", dns, dropped)
	}

	clean := json.RawMessage(`{"rules":[{"domain":["b"],"outbound":"direct"}]}`)
	if got, dropped := DropClientOnlyRoute(clean); string(got) != string(clean) || dropped != nil {
		t.Errorf("a clean route was rewritten: %s %v", got, dropped)
	}
}
