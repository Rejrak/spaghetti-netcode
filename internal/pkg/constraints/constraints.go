package constraints

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// CanonicalConstraints matches the chain's struct definitions and order.
// We must use omitempty where applicable to ensure canonical JSON bytes.
type CanonicalConstraints struct {
	MsgType       string `json:"msg_type,omitempty"`
	Denom         string `json:"denom,omitempty"`
	Receiver      string `json:"receiver,omitempty"`
	SourceChannel string `json:"source_channel,omitempty"`
	MaxAmount     string `json:"max_amount,omitempty"`
}

// ComputeHash returns the SHA-256 hash of the canonical JSON representation of constraints.
func ComputeHash(c CanonicalConstraints) []byte {
	// Marshal the struct into JSON. json.Marshal handles the fields in the order
	// they are defined in the struct and drops empty fields if omitempty is set.
	b, err := json.Marshal(c)
	if err != nil {
		panic(fmt.Errorf("failed to marshal canonical constraints: %w", err))
	}

	h := sha256.Sum256(b)
	return h[:]
}
