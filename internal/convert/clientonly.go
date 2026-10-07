package convert

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Client-only sing-box options: they read state only a client has — the
// process or app behind a connection, the Wi-Fi network, the platform's
// network monitor, a LAN neighbour, a Clash mode. Nexora offers none of them
// (the Panel's docs/product.md decision 29): the Panel refuses to save a
// template carrying one and the node refuses a config naming one. A panel
// whose routing was also run on clients (s-ui) may carry them, so they are
// dropped on import and the import says so.
var (
	clientOnlyRuleKeys = []string{
		"process_name", "process_path", "process_path_regex",
		"package_name", "package_name_regex", "user", "user_id",
		"wifi_ssid", "wifi_bssid", "network_type", "network_is_expensive",
		"network_is_constrained", "default_interface_address",
		"source_mac_address", "source_hostname", "clash_mode",
	}
	clientOnlyRouteKeys = []string{
		"find_process", "find_neighbor", "dhcp_lease_files", "override_android_vpn",
		"default_network_strategy", "default_network_type",
		"default_fallback_network_type", "default_fallback_delay",
	}
)

// DropClientOnlyRoute removes the client-only options from a sing-box route
// block and reports what went. A rule naming a client-only condition anywhere
// in it is dropped whole: stripping only the condition would widen it, and on a
// server such a rule never matched.
func DropClientOnlyRoute(route json.RawMessage) (json.RawMessage, []string) {
	return dropClientOnly(route, "route", clientOnlyRouteKeys, false)
}

// DropClientOnlyDNS does the same for a dns block: its rules, and a local
// server's neighbour domains.
func DropClientOnlyDNS(dns json.RawMessage) (json.RawMessage, []string) {
	return dropClientOnly(dns, "dns", nil, true)
}

func dropClientOnly(raw json.RawMessage, name string, blockKeys []string, servers bool) (json.RawMessage, []string) {
	var block map[string]any
	if json.Unmarshal(raw, &block) != nil {
		return raw, nil
	}
	var dropped []string
	for _, key := range blockKeys {
		if _, ok := block[key]; ok {
			delete(block, key)
			dropped = append(dropped, name+"."+key)
		}
	}
	if rules, ok := block["rules"].([]any); ok {
		kept := make([]any, 0, len(rules))
		for i, rule := range rules {
			if keys := clientOnlyIn(rule); len(keys) > 0 {
				dropped = append(dropped, fmt.Sprintf("%s.rules[%d] (%s)", name, i, strings.Join(keys, ", ")))
				continue
			}
			kept = append(kept, rule)
		}
		block["rules"] = kept
	}
	if servers {
		if list, ok := block["servers"].([]any); ok {
			for i, srv := range list {
				if obj, ok := srv.(map[string]any); ok {
					if _, has := obj["neighbor_domain"]; has {
						delete(obj, "neighbor_domain")
						dropped = append(dropped, fmt.Sprintf("%s.servers[%d].neighbor_domain", name, i))
					}
				}
			}
		}
	}
	if len(dropped) == 0 {
		return raw, nil
	}
	out, err := json.Marshal(block)
	if err != nil {
		return raw, nil
	}
	return out, dropped
}

// clientOnlyIn lists the client-only conditions a rule sets, nested logical
// rules included.
func clientOnlyIn(rule any) []string {
	obj, ok := rule.(map[string]any)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for _, key := range clientOnlyRuleKeys {
		if v, ok := obj[key]; ok && !emptyValue(v) {
			seen[key] = true
		}
	}
	if nested, ok := obj["rules"].([]any); ok {
		for _, inner := range nested {
			for _, key := range clientOnlyIn(inner) {
				seen[key] = true
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func emptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
