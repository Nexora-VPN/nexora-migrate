package httpx

import (
	"net/http"
	"net/http/cookiejar"
)

// newSimpleJar returns the standard library's jar. It exists as its own file so
// the panels that authenticate with a session cookie — 3x-ui's web login, and
// Marzneshin's browser flow — keep their session across requests without each
// reader carrying its own cookie handling.
func newSimpleJar() (http.CookieJar, error) {
	return cookiejar.New(nil)
}
