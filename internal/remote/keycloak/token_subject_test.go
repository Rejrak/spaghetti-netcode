package remote

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type tokenRoundTripper func(*http.Request) (*http.Response, error)

func (f tokenRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestValidateTokenSubject(t *testing.T) {
	var base, audience, wallet string
	audience, wallet = "issuer-api", "cosmos1wallet"
	base = "https://keycloak.example"
	client := NewKeycloakClient(KeycloakConfig{BaseURL: base, Realm: "alpha", ClientID: "issuer", ClientSecret: "service-secret", AccessTokenAudience: "issuer-api", EnableWalletAttributeLookup: true})
	client.http.Transport = tokenRoundTripper(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/realms/alpha/protocol/openid-connect/token/introspect":
			if r.PostFormValue("client_secret") != "service-secret" {
				t.Error("missing server credential")
			}
			active := r.PostFormValue("token") == "valid"
			body = fmt.Sprintf(`{"active":%t,"iss":%q,"aud":[%q],"sub":"user-1"}`, active, base+"/realms/alpha", audience)
		case "/realms/alpha/protocol/openid-connect/token":
			body = `{"access_token":"service-token","expires_in":300}`
		case "/admin/realms/alpha/users/user-1":
			if r.Header.Get("Authorization") != "Bearer service-token" {
				t.Error("missing service token")
			}
			body = fmt.Sprintf(`{"id":"user-1","username":"other","attributes":{"walletAddress":[%q]}}`, wallet)
		case "/admin/realms/alpha/users":
			body = `[]`
			if r.URL.Query().Get("q") != "" {
				body = fmt.Sprintf(`[{"id":"user-1","username":"other","attributes":{"walletAddress":[%q]}}]`, wallet)
			}
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	authenticated, bound, err := client.ValidateTokenSubject(context.Background(), "valid", "cosmos1wallet")
	if err != nil || !authenticated || !bound {
		t.Fatalf("valid binding: %t %t %v", authenticated, bound, err)
	}
	authenticated, bound, err = client.ValidateTokenSubject(context.Background(), "invalid", "cosmos1wallet")
	if err != nil || authenticated || bound {
		t.Fatalf("inactive token accepted: %t %t %v", authenticated, bound, err)
	}
	authenticated, bound, err = client.ValidateTokenSubject(context.Background(), "valid", "cosmos1other")
	if err != nil || !authenticated || bound {
		t.Fatalf("mismatched wallet accepted: %t %t %v", authenticated, bound, err)
	}
	audience = "wrong-api"
	authenticated, bound, err = client.ValidateTokenSubject(context.Background(), "valid", "cosmos1wallet")
	if err != nil || authenticated || bound {
		t.Fatalf("wrong audience accepted: %t %t %v", authenticated, bound, err)
	}
}
