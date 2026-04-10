package cli

import (
	"net/http"
	"time"
)

// CFAccessTransport is an http.RoundTripper that injects Cloudflare Access
// service-token headers on every outbound request. It is the single place
// where CF Access credentials are applied — both the CLI APIClient and the
// daemon Client call NewHTTPClient to build their http.Client, so credentials
// propagate everywhere automatically without individual call-site changes.
type CFAccessTransport struct {
	Base         http.RoundTripper // nil → uses http.DefaultTransport
	ClientID     string
	ClientSecret string
}

func (t *CFAccessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.ClientID != "" || t.ClientSecret != "" {
		// Clone the request so we don't mutate the caller's copy.
		r := req.Clone(req.Context())
		if t.ClientID != "" {
			r.Header.Set("CF-Access-Client-Id", t.ClientID)
		}
		if t.ClientSecret != "" {
			r.Header.Set("CF-Access-Client-Secret", t.ClientSecret)
		}
		req = r
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// NewHTTPClient returns an http.Client configured with timeout and an optional
// Cloudflare Access transport layer. When clientID and clientSecret are both
// empty the returned client behaves identically to a plain http.Client.
func NewHTTPClient(timeout time.Duration, clientID, clientSecret string) *http.Client {
	var transport http.RoundTripper
	if clientID != "" || clientSecret != "" {
		transport = &CFAccessTransport{
			ClientID:     clientID,
			ClientSecret: clientSecret,
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport, // nil → http.DefaultTransport (Go default)
	}
}
