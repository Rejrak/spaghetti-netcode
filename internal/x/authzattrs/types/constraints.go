// Package types — canonical constraints hashing for x/authzattrs.
//
// The hash algorithm must be byte-for-byte identical between:
//   - this chain-side implementation
//   - the off-chain auth service (Go)
//
// Design:
//  1. Build a ConstraintsDoc from the parsed tx message fields.
//  2. Serialize to canonical JSON (sorted keys, no whitespace).
//  3. SHA-256 the UTF-8 bytes.
//
// The canonical JSON field order is enforced by the fixed struct field order
// and `json:",omitempty"` to suppress zero values consistently.
package types

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// ConstraintsDoc is the canonical representation of per-message constraints
// that is hashed and stored in AuthzRecord.ConstraintsHash.
//
// IMPORTANT: field order in the JSON output is determined by the struct field
// declaration order (Go's encoding/json marshals fields in declaration order).
// Do NOT reorder fields without a policy version bump.
type ConstraintsDoc struct {
	// MsgType is the full protobuf type URL, e.g. "/cosmos.bank.v1beta1.MsgSend".
	MsgType string `json:"msg_type"`

	// Denom is the token denomination (set for bank/IBC messages).
	Denom string `json:"denom,omitempty"`

	// Receiver is the destination address (optional).
	Receiver string `json:"receiver,omitempty"`

	// SourceChannel is the IBC source channel identifier (optional).
	SourceChannel string `json:"source_channel,omitempty"`

	// SourcePort is the IBC source port (optional).
	SourcePort string `json:"source_port,omitempty"`

	// MaxAmount is the human-readable maximum transfer amount in base units (optional).
	// Use an empty string to mean "unrestricted".
	MaxAmount string `json:"max_amount,omitempty"`

	// Memo is the tx memo constraint, if applicable (optional).
	Memo string `json:"memo,omitempty"`
}

// HashConstraints serializes doc to canonical JSON and returns its SHA-256 digest.
// This is the authoritative implementation for the chain side.
func HashConstraints(doc ConstraintsDoc) ([]byte, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("authzattrs: marshal constraints: %w", err)
	}
	h := sha256.Sum256(b)
	return h[:], nil
}

// MustHashConstraints is like HashConstraints but panics on error.
// Only use in tests or genesis where a panic is acceptable.
func MustHashConstraints(doc ConstraintsDoc) []byte {
	h, err := HashConstraints(doc)
	if err != nil {
		panic(err)
	}
	return h
}

// ConstraintsJSON returns the canonical JSON string for a ConstraintsDoc.
// Useful for debugging and test vectors.
func ConstraintsJSON(doc ConstraintsDoc) (string, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
