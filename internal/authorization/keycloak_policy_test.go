package authorization

import (
	"crypto/sha256"
	"testing"
)

func TestKeycloakPolicyHash(t *testing.T) {
	wantDescriptor := "alpha.keycloak.attribute-policy.v1" +
		"|policy_id=policy-bank-send" +
		"|policy_version=7" +
		"|operation=/cosmos.bank.v1beta1.MsgSend" +
		"|permission=supply.send" +
		"|denom=token" +
		"|max_amount=5000"
	if got := keycloakPolicyDescriptor("policy-bank-send", "7", "supply.send"); got != wantDescriptor {
		t.Fatalf("descriptor = %q, want %q", got, wantDescriptor)
	}
	want := sha256.Sum256([]byte(wantDescriptor))
	got := KeycloakPolicyHash("policy-bank-send", "7", "supply.send")
	if got != want {
		t.Fatalf("hash = %x, want %x", got, want)
	}
	for name, changed := range map[string][sha256.Size]byte{
		"policy ID":      KeycloakPolicyHash("other-policy", "7", "supply.send"),
		"policy version": KeycloakPolicyHash("policy-bank-send", "8", "supply.send"),
		"permission":     KeycloakPolicyHash("policy-bank-send", "7", "other.permission"),
	} {
		if changed == got {
			t.Fatalf("changing %s did not change hash", name)
		}
	}
}
