package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestClient_IdentityHeaders_PostJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Platform"); got != "daemon" {
			t.Errorf("expected X-Client-Platform daemon, got %q", got)
		}
		if got := r.Header.Get("X-Client-Version"); got != "9.9.9" {
			t.Errorf("expected X-Client-Version 9.9.9, got %q", got)
		}
		if got := r.Header.Get("X-Client-OS"); got != normalizeGOOS(runtime.GOOS) {
			t.Errorf("expected X-Client-OS %q, got %q", normalizeGOOS(runtime.GOOS), got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("expected Authorization Bearer tok, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")
	c.SetVersion("9.9.9")

	if err := c.postJSON(context.Background(), "/api/daemon/test", map[string]any{}, nil); err != nil {
		t.Fatalf("postJSON: %v", err)
	}
}

func TestClient_CFAccessHeaders_PostJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("CF-Access-Client-Id"); got != "fake-client-id" {
			t.Errorf("expected CF-Access-Client-Id fake-client-id, got %q", got)
		}
		if got := r.Header.Get("CF-Access-Client-Secret"); got != "fake-client-secret" {
			t.Errorf("expected CF-Access-Client-Secret fake-client-secret, got %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClientWithCF(srv.URL, "fake-client-id", "fake-client-secret")
	if err := c.postJSON(context.Background(), "/api/daemon/test", map[string]any{}, nil); err != nil {
		t.Fatalf("postJSON: %v", err)
	}
}

func TestClient_WebSocketHeaders(t *testing.T) {
	c := NewClientWithCF("https://api.example.com", "fake-client-id", "fake-client-secret")
	c.SetToken("tok")
	c.SetVersion("9.9.9")

	headers := c.websocketHeaders()
	if got := headers.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("expected Authorization Bearer tok, got %q", got)
	}
	if got := headers.Get("X-Client-Platform"); got != "daemon" {
		t.Errorf("expected X-Client-Platform daemon, got %q", got)
	}
	if got := headers.Get("X-Client-Version"); got != "9.9.9" {
		t.Errorf("expected X-Client-Version 9.9.9, got %q", got)
	}
	if got := headers.Get("X-Client-OS"); got != normalizeGOOS(runtime.GOOS) {
		t.Errorf("expected X-Client-OS %q, got %q", normalizeGOOS(runtime.GOOS), got)
	}
	if got := headers.Get("CF-Access-Client-Id"); got != "fake-client-id" {
		t.Errorf("expected CF-Access-Client-Id fake-client-id, got %q", got)
	}
	if got := headers.Get("CF-Access-Client-Secret"); got != "fake-client-secret" {
		t.Errorf("expected CF-Access-Client-Secret fake-client-secret, got %q", got)
	}
}

func TestClient_WebSocketHeadersOmitCloudflareAccessWhenUnset(t *testing.T) {
	c := NewClient("https://api.example.com")
	headers := c.websocketHeaders()
	if vals := headers.Values("CF-Access-Client-Id"); len(vals) != 0 {
		t.Errorf("expected CF-Access-Client-Id absent, got %v", vals)
	}
	if vals := headers.Values("CF-Access-Client-Secret"); len(vals) != 0 {
		t.Errorf("expected CF-Access-Client-Secret absent, got %v", vals)
	}
}

func TestClient_IdentityHeaders_GetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Platform"); got != "daemon" {
			t.Errorf("expected X-Client-Platform daemon, got %q", got)
		}
		if got := r.Header.Get("X-Client-Version"); got != "1.2.3" {
			t.Errorf("expected X-Client-Version 1.2.3, got %q", got)
		}
		if got := r.Header.Get("X-Client-OS"); got == "" {
			t.Errorf("expected X-Client-OS to be set")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")
	c.SetVersion("1.2.3")

	var out map[string]any
	if err := c.getJSON(context.Background(), "/api/daemon/test", &out); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
}

func TestClient_VersionOmittedWhenUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Platform"); got != "daemon" {
			t.Errorf("expected X-Client-Platform daemon, got %q", got)
		}
		// SetVersion not called → header must be omitted (not "").
		if vals := r.Header.Values("X-Client-Version"); len(vals) != 0 {
			t.Errorf("expected X-Client-Version absent, got %v", vals)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.postJSON(context.Background(), "/api/daemon/test", nil, nil); err != nil {
		t.Fatalf("postJSON: %v", err)
	}
}

func TestNormalizeGOOS(t *testing.T) {
	cases := map[string]string{
		"darwin":  "macos",
		"windows": "windows",
		"linux":   "linux",
		"freebsd": "freebsd",
	}
	for in, want := range cases {
		if got := normalizeGOOS(in); got != want {
			t.Errorf("normalizeGOOS(%q) = %q, want %q", in, got, want)
		}
	}
}
