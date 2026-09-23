package convert

import (
	"encoding/json"
	"reflect"
	"testing"
)

func decode(t *testing.T, s string) Obj {
	t.Helper()
	var o Obj
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

// The padding, session and uplink fields are checked by the server against
// what each request carries, and a migrated client still has the old panel's
// values in its link — so they have to arrive, renamed to the node's spelling.
func TestXHTTPCarriesTheLockstepFields(t *testing.T) {
	stream := decode(t, `{
		"network": "xhttp",
		"xhttpSettings": {
			"path": "/x", "host": "cdn.example.com", "mode": "packet-up",
			"xPaddingBytes": "200-900",
			"scMaxEachPostBytes": {"from": 500000, "to": 1000000},
			"scMaxBufferedPosts": 40,
			"noSSEHeader": true,
			"xPaddingObfsMode": true, "xPaddingPlacement": "header", "xPaddingMethod": "tokenish",
			"sessionIDPlacement": "cookie", "sessionIDKey": "sid",
			"uplinkHTTPMethod": "GET", "uplinkDataPlacement": "cookie",
			"xmux": {"maxConcurrency": "16-32"}
		},
		"sockopt": {"trustedXForwardedFor": ["CF-Connecting-IP"]}
	}`)
	in := Inbound("vless", "", 443, decode(t, `{"clients":[],"decryption":"none"}`), stream)
	tr, _ := in.Config["transport"].(Obj)
	want := Obj{
		"type": "xhttp", "path": "/x", "host": "cdn.example.com", "mode": "packet-up",
		"x_padding_bytes":         "200-900",
		"sc_max_each_post_bytes":  "500000-1000000",
		"sc_max_buffered_posts":   40,
		"no_sse_header":           true,
		"x_padding_obfs_mode":     true,
		"x_padding_placement":     "header",
		"x_padding_method":        "tokenish",
		"session_placement":       "cookie",
		"session_key":             "sid",
		"uplink_http_method":      "GET",
		"uplink_data_placement":   "cookie",
		"trusted_x_forwarded_for": []string{"CF-Connecting-IP"},
	}
	if !reflect.DeepEqual(tr, want) {
		t.Errorf("transport =\n%#v\nwant\n%#v", tr, want)
	}
	if _, ok := in.Config["decryption"]; ok {
		t.Errorf(`"none" is no encryption and must not be written: %v`, in.Config)
	}
}

// Xray's `extra` stands in for every field but host, path and mode.
func TestXHTTPReadsExtra(t *testing.T) {
	stream := decode(t, `{"network":"xhttp","xhttpSettings":{"path":"/y","mode":"auto",
		"extra":{"xPaddingBytes":1000,"noGRPCHeader":true}}}`)
	tr, _ := Inbound("vless", "", 443, nil, stream).Config["transport"].(Obj)
	if tr["x_padding_bytes"] != "1000" || tr["no_grpc_header"] != true || tr["path"] != "/y" {
		t.Errorf("extra was not read: %v", tr)
	}
}

// The node reads Xray's VLESS Encryption string as it is, so it moves verbatim
// and the clients' `encryption=` keeps matching.
func TestVLESSEncryptionIsCarried(t *testing.T) {
	const dec = "mlkem768x25519plus.native.600s.cGFzc3dvcmQtcGFzc3dvcmQtcGFzc3dvcmQtcGFzc3c"
	in := Inbound("vless", "", 443, decode(t, `{"clients":[],"decryption":" `+dec+` "}`), nil)
	if in.Config["decryption"] != dec {
		t.Errorf("decryption = %v, want %q", in.Config["decryption"], dec)
	}
	if len(in.Notes) == 0 {
		t.Error("the dropped vision flow must be said")
	}
}

// 3x-ui and alireza0's x-ui write a VLESS outbound flat, without vnext; read
// as vnext-only it arrived with no server and no uuid.
func TestFlatVLESSOutbound(t *testing.T) {
	_, cfg, _, ok := Outbound(decode(t, `{"protocol":"vless","tag":"out",
		"settings":{"address":"1.2.3.4","port":443,"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","flow":"xtls-rprx-vision","encryption":"none"}}`))
	if !ok || cfg["server"] != "1.2.3.4" || cfg["server_port"] != 443 ||
		cfg["uuid"] != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" || cfg["flow"] != "xtls-rprx-vision" {
		t.Errorf("flat outbound = %v", cfg)
	}
}
