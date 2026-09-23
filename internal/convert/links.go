package convert

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
)

// LinkCredentials pulls the credentials out of a share link.
//
// This exists for the panels that do not store credentials at all: Marzneshin
// derives them from the account key with a hash whose algorithm is an install
// setting, so the only honest way to learn what a customer's client is actually
// configured with is to read the link the panel itself hands that customer.
// Parsing what the source panel published is exact by construction, and it
// needs no reimplementation of anybody's key derivation.
type LinkCredentials struct {
	UUID     string
	Password string
	Flow     string
	Method   string
	Protocol string
}

// ParseShareLink reads one vless://, vmess://, trojan:// or ss:// link.
func ParseShareLink(link string) (LinkCredentials, bool) {
	link = strings.TrimSpace(link)
	scheme, rest, found := strings.Cut(link, "://")
	if !found {
		return LinkCredentials{}, false
	}
	switch strings.ToLower(scheme) {
	case "vless":
		return parseUserInfoLink("vless", rest)
	case "trojan":
		return parseUserInfoLink("trojan", rest)
	case "vmess":
		return parseVMess(rest)
	case "ss", "shadowsocks":
		return parseShadowsocks(rest)
	}
	return LinkCredentials{}, false
}

// parseUserInfoLink handles the scheme://secret@host:port?params#name shape that
// VLESS and Trojan share.
func parseUserInfoLink(protocol, rest string) (LinkCredentials, bool) {
	at := strings.LastIndex(rest, "@")
	if at <= 0 {
		return LinkCredentials{}, false
	}
	secret, _ := url.PathUnescape(rest[:at])
	if secret == "" {
		return LinkCredentials{}, false
	}
	out := LinkCredentials{Protocol: protocol}
	if protocol == "vless" {
		out.UUID = secret
		if q := queryOf(rest[at:]); q != nil {
			out.Flow = q.Get("flow")
		}
	} else {
		out.Password = secret
	}
	return out, true
}

// parseVMess handles vmess://<base64 json>.
func parseVMess(rest string) (LinkCredentials, bool) {
	raw, err := decodeBase64(strings.SplitN(rest, "#", 2)[0])
	if err != nil {
		return LinkCredentials{}, false
	}
	var doc struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.ID == "" {
		return LinkCredentials{}, false
	}
	return LinkCredentials{UUID: doc.ID, Protocol: "vmess"}, true
}

// parseShadowsocks handles both ss://base64(method:password)@host and the
// SIP002 ss://base64(method:password)@host?… spelling.
func parseShadowsocks(rest string) (LinkCredentials, bool) {
	body := strings.SplitN(rest, "#", 2)[0]
	body = strings.SplitN(body, "?", 2)[0]

	if at := strings.LastIndex(body, "@"); at > 0 {
		if raw, err := decodeBase64(body[:at]); err == nil {
			if method, password, ok := strings.Cut(string(raw), ":"); ok {
				return LinkCredentials{Method: method, Password: password, Protocol: "shadowsocks"}, true
			}
		}
		// Some panels leave the userinfo in the clear.
		if method, password, ok := strings.Cut(body[:at], ":"); ok {
			m, _ := url.PathUnescape(method)
			p, _ := url.PathUnescape(password)
			return LinkCredentials{Method: m, Password: p, Protocol: "shadowsocks"}, true
		}
		return LinkCredentials{}, false
	}
	// Whole-link base64: method:password@host:port.
	raw, err := decodeBase64(body)
	if err != nil {
		return LinkCredentials{}, false
	}
	creds, _, ok := strings.Cut(string(raw), "@")
	if !ok {
		return LinkCredentials{}, false
	}
	method, password, ok := strings.Cut(creds, ":")
	if !ok {
		return LinkCredentials{}, false
	}
	return LinkCredentials{Method: method, Password: password, Protocol: "shadowsocks"}, true
}

// MergeLinks folds the credentials out of a whole subscription into one set,
// preferring VLESS/VMess for the uuid and Trojan/Shadowsocks for the password —
// the same precedence the panel readers use — and naming any protocol whose
// secret had to be discarded.
func MergeLinks(links []string) (uuid, password, flow, method string, conflicts []string) {
	for _, l := range links {
		c, ok := ParseShareLink(l)
		if !ok {
			continue
		}
		if c.UUID != "" {
			switch {
			case uuid == "":
				uuid = c.UUID
				if c.Flow != "" {
					flow = c.Flow
				}
			case uuid != c.UUID:
				conflicts = appendUnique(conflicts, c.Protocol)
			}
		}
		if c.Password != "" {
			switch {
			case password == "":
				password = c.Password
				if c.Method != "" {
					method = c.Method
				}
			case password != c.Password:
				conflicts = appendUnique(conflicts, c.Protocol)
			}
		}
	}
	return
}

// SplitSubscription turns a subscription body into its links, accepting both
// the plain list and the base64-wrapped form every panel in this space serves.
func SplitSubscription(body []byte) []string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil
	}
	if !strings.Contains(text, "://") {
		if raw, err := decodeBase64(text); err == nil {
			text = string(raw)
		}
	}
	var out []string
	for _, line := range strings.Fields(text) {
		if strings.Contains(line, "://") {
			out = append(out, line)
		}
	}
	return out
}

// decodeBase64 accepts every padding and alphabet variant these links use.
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(s); err == nil {
			return raw, nil
		}
	}
	return nil, base64.CorruptInputError(0)
}

func queryOf(rest string) url.Values {
	i := strings.Index(rest, "?")
	if i < 0 {
		return nil
	}
	q := rest[i+1:]
	if h := strings.Index(q, "#"); h >= 0 {
		q = q[:h]
	}
	v, err := url.ParseQuery(q)
	if err != nil {
		return nil
	}
	return v
}
