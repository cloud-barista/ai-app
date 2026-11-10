package redact

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// secretURL returns base with user information and a query token added, the
// way a URL with credentials may be configured.
func secretURL(t *testing.T, base, user, password, query string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %q: %v", base, err)
	}
	u.User = url.UserPassword(user, password)
	u.RawQuery = query
	u.Fragment = "frag"
	return u.String()
}

func TestURL(t *testing.T) {
	cases := map[string]string{
		secretURL(t, "https://host.example:8443/a/b", "u", "p", "t=1"): "https://host.example:8443/a/b",
		"http://host.example":    "http://host.example",
		"":                       InvalidURL,
		"host.example/no-scheme": InvalidURL,
		"http://[::1:bad":        InvalidURL,
	}
	for in, want := range cases {
		if got := URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorFromHTTPClient(t *testing.T) {
	raw := secretURL(t, "http://127.0.0.1:1/registry", "ru", "rpw", "key=rtok")
	target, _ := url.Parse(raw)
	req, _ := http.NewRequest(http.MethodPost, target.JoinPath("agents").String(), nil)
	_, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err == nil {
		t.Fatal("request to a closed port succeeded")
	}
	got := Error(err, raw)
	for _, secret := range []string{"ru:", "rpw", "rtok", "frag"} {
		if strings.Contains(got, secret) {
			t.Errorf("Error kept %q: %s (from %v)", secret, got, err)
		}
	}
	if !strings.Contains(got, "127.0.0.1:1/registry/agents") {
		t.Errorf("Error dropped the endpoint: %s", got)
	}
}
