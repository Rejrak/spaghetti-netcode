package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const (
	demoSubject  = "cosmos1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqnrql8a"
	demoReceiver = "cosmos1qyqszqgpqyqszqgpqyqszqgpqyqszqgpjnp7du"
)

func demoRequest(t *testing.T, action string) DemoAuthorizationRequest {
	t.Helper()
	alphaSeed, err := hex.DecodeString(issuerAlphaSeed)
	if err != nil {
		t.Fatal(err)
	}
	betaSeed, err := hex.DecodeString(issuerBetaSeed)
	if err != nil {
		t.Fatal(err)
	}
	return DemoAuthorizationRequest{
		Action:          action,
		BatchID:         73,
		ChainID:         "alpha-demo-1",
		Subject:         demoSubject,
		Receiver:        demoReceiver,
		IssuerAlphaSeed: alphaSeed,
		IssuerBetaSeed:  betaSeed,
	}
}

func TestBuildDemoAuthorizationBatchGrant(t *testing.T) {
	request := demoRequest(t, DemoActionGrant)
	original := request
	original.IssuerAlphaSeed = append([]byte(nil), request.IssuerAlphaSeed...)
	original.IssuerBetaSeed = append([]byte(nil), request.IssuerBetaSeed...)

	batch, batchHash, err := BuildDemoAuthorizationBatch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, original) {
		t.Fatal("demo builder mutated caller input")
	}
	if batch.SignDoc.BatchID != request.BatchID || len(batch.SignDoc.Records) != 1 {
		t.Fatalf("unexpected demo batch: %#v", batch.SignDoc)
	}
	record := batch.SignDoc.Records[0]
	if record.AuthorizationID != DemoAuthorizationID || record.PolicyID != demoPolicyID || record.PolicyVersion != demoPolicyVersion || record.IssuerSetID != demoIssuerSetID {
		t.Fatalf("unexpected trusted record metadata: %#v", record)
	}
	if record.Subject != request.Subject || record.BankSendConstraints.Receiver != request.Receiver || record.BankSendConstraints.Denom != demoDenom || record.BankSendConstraints.MaxAmount != demoMaxAmount {
		t.Fatalf("unexpected demo record facts/constraints: %#v", record)
	}
	if record.Revoked || record.ValidFromHeight != 1 || record.ValidUntilHeight != math.MaxInt64 {
		t.Fatalf("unexpected demo grant state: %#v", record)
	}
	wantPolicyHash := sha256.Sum256([]byte(demoPolicyHashInput))
	if !bytes.Equal(batch.SignDoc.PolicyHash, wantPolicyHash[:]) {
		t.Fatalf("policy hash: got %x want %x", batch.SignDoc.PolicyHash, wantPolicyHash)
	}
	if len(batch.Signatures) != 2 || batch.Signatures[0].IssuerID != "issuer-alpha" || batch.Signatures[1].IssuerID != "issuer-beta" {
		t.Fatalf("unexpected demo signatures: %#v", batch.Signatures)
	}
	signBytes, wantBatchHash, err := CanonicalBatchSignBytes(batch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	if batchHash != wantBatchHash {
		t.Fatal("demo batch hash does not match canonical sign bytes")
	}
	seeds := map[string][]byte{"issuer-alpha": request.IssuerAlphaSeed, "issuer-beta": request.IssuerBetaSeed}
	for _, signature := range batch.Signatures {
		publicKey := ed25519.NewKeyFromSeed(seeds[signature.IssuerID]).Public().(ed25519.PublicKey)
		if !ed25519.Verify(publicKey, signBytes, signature.Signature) {
			t.Fatalf("signature for %s does not verify", signature.IssuerID)
		}
	}
	if _, err := ToProtoAuthorizationBatch(batch); err != nil {
		t.Fatalf("demo batch is not wire compatible: %v", err)
	}
}

func TestBuildDemoAuthorizationBatchRevocationChangesOnlyRevoked(t *testing.T) {
	grantRequest := demoRequest(t, DemoActionGrant)
	revokeRequest := demoRequest(t, DemoActionRevoke)
	grant, _, err := BuildDemoAuthorizationBatch(context.Background(), grantRequest)
	if err != nil {
		t.Fatal(err)
	}
	revoke, _, err := BuildDemoAuthorizationBatch(context.Background(), revokeRequest)
	if err != nil {
		t.Fatal(err)
	}
	grantRecord := grant.SignDoc.Records[0]
	revokeRecord := revoke.SignDoc.Records[0]
	if !revokeRecord.Revoked || revokeRecord.AuthorizationID != DemoAuthorizationID {
		t.Fatalf("unexpected revocation: %#v", revokeRecord)
	}
	revokeRecord.Revoked = false
	if !reflect.DeepEqual(revokeRecord, grantRecord) {
		t.Fatalf("revocation changed fields other than revoked\n grant=%#v\nrevoke=%#v", grantRecord, revokeRecord)
	}
}

func TestBuildDemoAuthorizationBatchRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DemoAuthorizationRequest)
	}{
		{name: "invalid action", mutate: func(request *DemoAuthorizationRequest) { request.Action = "replace" }},
		{name: "zero batch id", mutate: func(request *DemoAuthorizationRequest) { request.BatchID = 0 }},
		{name: "malformed subject", mutate: func(request *DemoAuthorizationRequest) { request.Subject = "bad" }},
		{name: "malformed receiver", mutate: func(request *DemoAuthorizationRequest) { request.Receiver = "bad" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := demoRequest(t, DemoActionGrant)
			tt.mutate(&request)
			if _, _, err := BuildDemoAuthorizationBatch(context.Background(), request); err == nil {
				t.Fatal("expected invalid demo input error")
			}
		})
	}
}

func TestLoadDemoIssuerSeed(t *testing.T) {
	directory := t.TempDir()
	write := func(name, contents string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	validPath := write("valid.seed", " \n"+issuerAlphaSeed+"\n", 0o600)
	seed, err := LoadDemoIssuerSeed(validPath)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString(issuerAlphaSeed)
	if !bytes.Equal(seed, want) {
		t.Fatal("loaded seed differs from file")
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "missing", path: filepath.Join(directory, "missing.seed")},
		{name: "malformed hex", path: write("malformed.seed", "not-hex", 0o600)},
		{name: "wrong length", path: write("short.seed", "0011", 0o600)},
		{name: "group or world permissions", path: write("broad.seed", issuerBetaSeed, 0o644)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadDemoIssuerSeed(tt.path); err == nil {
				t.Fatal("expected seed file rejection")
			}
		})
	}
}
