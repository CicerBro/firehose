package server

import (
	"net/http/httptest"
	"testing"
)

func TestAuthenticate(t *testing.T) {
	const tok = "s3cret-token"
	cases := []struct {
		name     string
		apiToken string
		dev      bool
		headers  map[string]string
		wantOK   bool
		wantUser string
	}{
		{"valid bearer", tok, false, map[string]string{"Authorization": "Bearer " + tok}, true, "token"},
		{"lowercase scheme", tok, false, map[string]string{"Authorization": "bearer " + tok}, true, "token"},
		{"invalid bearer", tok, false, map[string]string{"Authorization": "Bearer wrong"}, false, ""},
		{"prefix of token", tok, false, map[string]string{"Authorization": "Bearer s3cret"}, false, ""},
		{"empty API token rejects bearer", "", false, map[string]string{"Authorization": "Bearer "}, false, ""},
		{"empty API token rejects any bearer", "", false, map[string]string{"Authorization": "Bearer x"}, false, ""},
		{"remote-user present", tok, false, map[string]string{"Remote-User": "alice"}, true, "alice"},
		{"remote-user absent", tok, false, nil, false, ""},
		{"bearer with spoofed remote-user still needs token", tok, false,
			map[string]string{"Authorization": "Bearer wrong", "Remote-User": "alice"}, false, ""},
		{"basic auth falls back to remote-user", tok, false,
			map[string]string{"Authorization": "Basic Zm9vOmJhcg==", "Remote-User": "bob"}, true, "bob"},
		{"dev bypass", "", true, nil, true, "dev"},
		{"dev with wrong bearer", tok, true, map[string]string{"Authorization": "Bearer wrong"}, true, "dev"},
		{"dev keeps remote-user", "", true, map[string]string{"Remote-User": "carol"}, true, "carol"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/config", nil)
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			user, ok := authenticate(r, c.apiToken, "Remote-User", c.dev)
			if ok != c.wantOK || user != c.wantUser {
				t.Fatalf("got (%q,%v) want (%q,%v)", user, ok, c.wantUser, c.wantOK)
			}
		})
	}
}

func TestCustomAuthHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Remote-User", "alice")
	if _, ok := authenticate(r, "", "X-Auth-User", false); ok {
		t.Fatal("wrong header accepted")
	}
	r.Header.Set("X-Auth-User", "alice")
	if u, ok := authenticate(r, "", "X-Auth-User", false); !ok || u != "alice" {
		t.Fatal("custom header rejected")
	}
}
