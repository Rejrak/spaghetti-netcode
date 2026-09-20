package authorization

import "crypto/sha256"

const keycloakPolicyDescriptorDomain = "alpha.keycloak.attribute-policy.v1"

func KeycloakPolicyHash(policyID, policyVersion, permission string) [sha256.Size]byte {
	return sha256.Sum256([]byte(keycloakPolicyDescriptor(policyID, policyVersion, permission)))
}

func keycloakPolicyDescriptor(policyID, policyVersion, permission string) string {
	return keycloakPolicyDescriptorDomain +
		"|policy_id=" + policyID +
		"|policy_version=" + policyVersion +
		"|operation=" + MsgSendTypeURL +
		"|permission=" + permission +
		"|denom=token" +
		"|max_amount=5000"
}
