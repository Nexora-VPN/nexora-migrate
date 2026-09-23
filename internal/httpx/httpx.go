// Package httpx is the small HTTP client the API-backed readers and the Nexora
// target client share: bearer or header auth, JSON in and out, and errors that
// quote the server rather than saying "unexpected status".
//
// An operator debugging a migration at 2am needs the panel's own words. Every
// error here carries the method, the path, the status and the first part of the
// body.
package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a JSON HTTP client pinned to one base URL.
type Client struct {
	Base    string
	HTTP    *http.Client
	Headers map[string]string
	// Cookies keeps a session for panels that authenticate that way.
	jar http.CookieJar
}

// New builds a client. insecure accepts a self-signed certificate, which many
// of these panels are deployed with.
func New(base string, insecure bool) (*Client, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, fmt.Errorf("no address given")
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("%q is not a valid address: %w", base, err)
	}
	jar := newJar()
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 15 * time.Second,
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the operator asked for it
	}
	return &Client{
		Base:    base,
		Headers: map[string]string{},
		jar:     jar,
		HTTP:    &http.Client{Timeout: 120 * time.Second, Transport: tr, Jar: jar},
	}, nil
}

// SetBearer sets an Authorization header.
func (c *Client) SetBearer(token string) {
	if token != "" {
		c.Headers["Authorization"] = "Bearer " + token
	}
}

// Do performs a request. body may be nil, a []byte, or anything JSON-encodable.
// out may be nil, a *json.RawMessage, or a pointer to decode into.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	contentType := ""
	switch v := body.(type) {
	case nil:
	case []byte:
		rdr, contentType = bytes.NewReader(v), "application/json"
	case url.Values:
		rdr, contentType = strings.NewReader(v.Encode()), "application/x-www-form-urlencoded"
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		rdr, contentType = bytes.NewReader(raw), "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))

	if resp.StatusCode >= 300 {
		return &StatusError{Method: method, Path: path, Status: resp.StatusCode, Body: string(raw)}
	}
	if out == nil {
		return nil
	}
	if p, ok := out.(*json.RawMessage); ok {
		*p = json.RawMessage(raw)
		return nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: the response was not the JSON this reader expected: %w", method, path, err)
	}
	return nil
}

// Download streams a response body into dst instead of decoding JSON. It is
// what the backup-over-API readers use: a panel database is tens of megabytes,
// far past the limit Do reads into memory, and it is not JSON at all.
//
// headers are merged over the client's own for this one request, which is how
// a panel that wants its token in a bespoke header (s-ui's `Token`) is served
// without that header leaking into every other call.
func (c *Client) Download(ctx context.Context, path string, headers map[string]string, dst io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "*/*")
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return 0, &StatusError{Method: "GET", Path: path, Status: resp.StatusCode, Body: string(raw)}
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, maxDownload))
	if err != nil {
		return n, fmt.Errorf("GET %s: the download stopped after %d bytes: %w", path, n, err)
	}
	if n == maxDownload {
		return n, fmt.Errorf("GET %s: the response is larger than %d MB, which is not a panel database", path, maxDownload>>20)
	}
	return n, nil
}

// maxDownload is a sanity bound, not a quota: the largest panel databases seen
// in the wild are a few hundred megabytes.
const maxDownload = 2 << 30

// Get is Do with no body.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// Post is Do with a body.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) url(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.Base + path
}

// StatusError is a non-2xx response with the server's own message kept.
type StatusError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	msg := strings.TrimSpace(e.Body)
	// Most of these panels answer {"detail": "..."} or {"error": "..."}; show
	// that sentence rather than the whole document.
	var probe map[string]any
	if json.Unmarshal([]byte(msg), &probe) == nil {
		for _, k := range []string{"detail", "error", "message", "msg"} {
			if s, ok := probe[k].(string); ok && s != "" {
				msg = s
				break
			}
		}
	}
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("%s %s → %d: %s", e.Method, e.Path, e.Status, msg)
}

// Status returns the HTTP status of err, or 0 if it is not a StatusError.
func Status(err error) int {
	var se *StatusError
	if ok := asStatus(err, &se); ok {
		return se.Status
	}
	return 0
}

func asStatus(err error, target **StatusError) bool {
	for err != nil {
		if se, ok := err.(*StatusError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// newJar returns a cookie jar that keeps a session across requests.
func newJar() http.CookieJar {
	j, err := newSimpleJar()
	if err != nil {
		return nil
	}
	return j
}
