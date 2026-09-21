package remote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const keycloakTestAddress = "cosmos1fl48vsnmsdzcv85q5d2q4z5ajdha8yu34mf0eh"

func TestAuthorizationSubjectCandidate(t *testing.T) {
	tests := []struct {
		name   string
		wallet bool
		user   kcUser
		want   string
		ok     bool
	}{
		{name: "canonical username", user: kcUser{Username: keycloakTestAddress}, want: keycloakTestAddress, ok: true},
		{name: "service account", user: kcUser{Username: "service-account-authz-middleware"}},
		{name: "unrelated principal", user: kcUser{Username: "realm-admin"}},
		{name: "malformed cosmos username forwarded", user: kcUser{Username: "cosmos-malformed"}, want: "cosmos-malformed", ok: true},
		{name: "wallet attribute", wallet: true, user: kcUser{Username: "alice", Attributes: map[string][]string{"walletAddress": {keycloakTestAddress}}}, want: keycloakTestAddress, ok: true},
		{name: "missing wallet attribute", wallet: true, user: kcUser{Username: keycloakTestAddress}},
		{name: "malformed wallet forwarded", wallet: true, user: kcUser{Username: "alice", Attributes: map[string][]string{"walletAddress": {"cosmos-malformed"}}}, want: "cosmos-malformed", ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewKeycloakClient(KeycloakConfig{EnableWalletAttributeLookup: tt.wallet})
			got, ok := client.authorizationSubjectCandidate(tt.user)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("candidate = %q/%v, want %q/%v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestFetchAuthorizationSubjectCandidatesLocalRealmShape(t *testing.T) {
	client := testKeycloakClient(t, func(r *http.Request) *http.Response {
		switch r.URL.Path {
		case "/realms/alpha/protocol/openid-connect/token":
			return jsonResponse(t, map[string]any{"access_token": "test-token", "expires_in": 60})
		case "/admin/realms/alpha/users":
			return jsonResponse(t, []kcUser{{Username: "service-account-authz-middleware"}, {Username: keycloakTestAddress}, {Username: "other-client"}})
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}
		}
	})
	got, err := client.FetchAuthorizationSubjectCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{keycloakTestAddress}; !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestFetchAttributesUnchanged(t *testing.T) {
	client := testKeycloakClient(t, func(r *http.Request) *http.Response {
		switch {
		case r.URL.Path == "/realms/alpha/protocol/openid-connect/token":
			return jsonResponse(t, map[string]any{"access_token": "test-token", "expires_in": 60})
		case r.URL.Path == "/admin/realms/alpha/users" && r.URL.Query().Get("username") == keycloakTestAddress:
			return jsonResponse(t, []kcUser{{ID: "alice-id", Username: keycloakTestAddress}})
		case r.URL.Path == "/admin/realms/alpha/users/alice-id/role-mappings/realm":
			return jsonResponse(t, []roleRep{{ID: "role-id", Name: "authz-bank-send"}})
		case r.URL.Path == "/admin/realms/alpha/roles-by-id/role-id":
			return jsonResponse(t, roleRep{ID: "role-id", Name: "authz-bank-send", Attributes: map[string][]string{"supply.transaction.send": {"true"}}})
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}
		}
	})
	attributes, err := client.FetchAttributes(context.Background(), keycloakTestAddress)
	if err != nil {
		t.Fatal(err)
	}
	if !attributes.Perms["supply.transaction.send"] || !reflect.DeepEqual(attributes.Roles, []string{"authz-bank-send"}) {
		t.Fatalf("attributes = %+v", attributes)
	}
}

type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	response := f(request)
	response.Request = request
	return response, nil
}

func testKeycloakClient(t *testing.T, transport roundTripFunc) *KeycloakClient {
	t.Helper()
	client := NewKeycloakClient(KeycloakConfig{BaseURL: "http://keycloak.test", Realm: "alpha", ClientID: "client", ClientSecret: "secret"})
	client.http = &http.Client{Transport: transport}
	return client
}

func jsonResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	var body strings.Builder
	if err := json.NewEncoder(&body).Encode(value); err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body.String())),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}
}
