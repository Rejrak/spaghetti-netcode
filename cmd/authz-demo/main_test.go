package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"spaghetti/internal/authorization"
)

func TestDemoOutputContainsOnlyPublicResultFields(t *testing.T) {
	const (
		alphaSeedHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
		betaSeedHex  = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
		subject      = "cosmos1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnrql8a"
		receiver     = "cosmos1qyqszqgpqyqszqgpqyqszqgpqyqszqgpjnp7du"
	)
	alphaSeed, _ := hex.DecodeString(alphaSeedHex)
	betaSeed, _ := hex.DecodeString(betaSeedHex)
	batch, batchHash, err := authorization.BuildDemoAuthorizationBatch(context.Background(), authorization.DemoAuthorizationRequest{
		Action:          authorization.DemoActionGrant,
		BatchID:         73,
		ChainID:         "alpha-demo-1",
		Subject:         subject,
		Receiver:        receiver,
		IssuerAlphaSeed: alphaSeed,
		IssuerBetaSeed:  betaSeed,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := newDemoOutput(authorization.DemoActionGrant, batch, batchHash, authorization.CommitResult{TxHash: "TX123", Height: 321})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, alphaSeedHex) || strings.Contains(text, betaSeedHex) {
		t.Fatal("demo output exposes a private seed")
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"action", "authorization_id", "batch_id", "batch_hash", "tx_hash", "height", "subject", "receiver", "max_amount", "denom"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("demo output missing %q", key)
		}
	}
	if len(fields) != 10 {
		t.Fatalf("demo output contains unexpected fields: %#v", fields)
	}
}
