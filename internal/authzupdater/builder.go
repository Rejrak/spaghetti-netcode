package authzupdater

import (
	"encoding/hex"
	"spaghetti/internal/pkg/constraints"
	"spaghetti/internal/user"
	"time"
)

type AuthzRecord struct {
	Address         string `json:"address"`
	MsgType         string `json:"msg_type"`
	ConstraintsHash string `json:"constraints_hash"`
	ValidUntil      int64  `json:"valid_until"`
	PolicyVersion   uint64 `json:"policy_version"`
	IssuerSetId     uint64 `json:"issuer_set_id"`
	Revoked         bool   `json:"revoked"`
}

// BuildRecord creates an AuthzRecord from a wallet address, operation type, user attributes, and policy parameters.
func BuildRecord(
	walletAddress string,
	msgType string,
	attrs *user.Attributes,
	policyVersion uint64,
	issuerSetId uint64,
	validFor time.Duration,
) *AuthzRecord {
	// Mock logic: use user attributes to build constraints
	// e.g. if they have a role, allow up to certain amount.
	maxAmount := ""
	for _, role := range attrs.Roles {
		if role == "office_manager" {
			maxAmount = "1000000" // Mock amount based on role
		}
	}

	c := constraints.CanonicalConstraints{
		MsgType:   msgType,
		MaxAmount: maxAmount,
	}

	hashBytes := constraints.ComputeHash(c)
	hashStr := hex.EncodeToString(hashBytes)

	return &AuthzRecord{
		Address:         walletAddress,
		MsgType:         msgType,
		ConstraintsHash: hashStr,
		ValidUntil:      time.Now().Add(validFor).Unix(),
		PolicyVersion:   policyVersion,
		IssuerSetId:     issuerSetId,
		Revoked:         false,
	}
}
