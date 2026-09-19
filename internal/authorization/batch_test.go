package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

const (
	issuerAlphaSeed = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	issuerBetaSeed  = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
)

func trustedBatchContext(input BatchSignDoc) TrustedBatchContext {
	return TrustedBatchContext{
		ChainID:       input.ChainID,
		BatchID:       input.BatchID,
		PolicyID:      input.PolicyID,
		PolicyVersion: input.PolicyVersion,
		PolicyHash:    input.PolicyHash,
		IssuerSetID:   input.IssuerSetID,
	}
}

func TestBuildBatchSignDocMatchesGoldenFixture(t *testing.T) {
	fixture, input := loadCanonicalFixture(t)
	originalRecords := append([]AuthorizationRecord(nil), input.Records...)
	originalHash := append([]byte(nil), input.PolicyHash...)

	signDoc, err := BuildBatchSignDoc(trustedBatchContext(input), input.Records)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(input.Records, originalRecords) || !bytes.Equal(input.PolicyHash, originalHash) {
		t.Fatal("builder mutated caller input")
	}
	for i, want := range fixture.CanonicalRecordOrder {
		if signDoc.Records[i].Subject != want.Subject || signDoc.Records[i].MsgTypeURL != want.MsgTypeURL {
			t.Fatalf("record %d order mismatch", i)
		}
	}
	signBytes, batchHash, err := CanonicalBatchSignBytes(signDoc)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(signBytes) != fixture.ExpectedSignBytesHex {
		t.Fatal("builder sign bytes differ from golden fixture")
	}
	if hex.EncodeToString(batchHash[:]) != fixture.ExpectedBatchHashHex {
		t.Fatal("builder batch hash differs from golden fixture")
	}

	wantAuthorizationID := signDoc.Records[0].AuthorizationID
	wantDenom := signDoc.Records[0].BankSendConstraints.Denom
	wantHashByte := signDoc.PolicyHash[0]
	input.Records[1].AuthorizationID = "mutated"
	input.Records[1].BankSendConstraints.Denom = "mutated"
	input.PolicyHash[0] ^= 0xff
	if signDoc.Records[0].AuthorizationID != wantAuthorizationID || signDoc.Records[0].BankSendConstraints.Denom != wantDenom || signDoc.PolicyHash[0] != wantHashByte {
		t.Fatal("builder result aliases caller records, constraints, or policy hash")
	}
}

func TestBuildBatchSignDocRejectsInvalidInput(t *testing.T) {
	t.Run("incomplete trusted context", func(t *testing.T) {
		_, input := loadCanonicalFixture(t)
		trusted := trustedBatchContext(input)
		trusted.ChainID = ""
		if _, err := BuildBatchSignDoc(trusted, input.Records); err == nil {
			t.Fatal("expected invalid trusted context error")
		}
	})
	t.Run("metadata mismatch", func(t *testing.T) {
		_, input := loadCanonicalFixture(t)
		trusted := trustedBatchContext(input)
		trusted.PolicyVersion++
		if _, err := BuildBatchSignDoc(trusted, input.Records); err == nil {
			t.Fatal("expected metadata mismatch error")
		}
	})
	t.Run("duplicate logical record", func(t *testing.T) {
		_, input := loadCanonicalFixture(t)
		duplicate := input.Records[0]
		duplicate.AuthorizationID = "different-audit-id"
		input.Records = append(input.Records, duplicate)
		if _, err := BuildBatchSignDoc(trustedBatchContext(input), input.Records); err == nil {
			t.Fatal("expected duplicate record error")
		}
	})
}

func goldenSigner(t *testing.T, issuerID, seedHex string) BatchSigner {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	signer, err := NewEd25519BatchSigner(issuerID, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	for i := range privateKey {
		privateKey[i] = 0
	}
	return signer
}

func fixtureIssuer(t *testing.T, fixture canonicalFixture, issuerID string) (ed25519.PublicKey, []byte) {
	t.Helper()
	for _, issuer := range fixture.Issuers {
		if issuer.IssuerID == issuerID {
			publicKey, err := hex.DecodeString(issuer.PublicKeyHex)
			if err != nil {
				t.Fatal(err)
			}
			signature, err := hex.DecodeString(issuer.SignatureHex)
			if err != nil {
				t.Fatal(err)
			}
			return ed25519.PublicKey(publicKey), signature
		}
	}
	t.Fatalf("fixture issuer %q not found", issuerID)
	return nil, nil
}

func TestSignAuthorizationBatchMatchesGoldenSignatures(t *testing.T) {
	fixture, input := loadCanonicalFixture(t)
	alpha := goldenSigner(t, "issuer-alpha", issuerAlphaSeed)
	beta := goldenSigner(t, "issuer-beta", issuerBetaSeed)
	original := BatchSignDoc{
		Domain: input.Domain, ChainID: input.ChainID, BatchID: input.BatchID,
		PolicyID: input.PolicyID, PolicyVersion: input.PolicyVersion,
		PolicyHash: append([]byte(nil), input.PolicyHash...), IssuerSetID: input.IssuerSetID,
		Records: append([]AuthorizationRecord(nil), input.Records...),
	}

	signers := []BatchSigner{beta, alpha}
	batch, batchHash, err := SignAuthorizationBatch(context.Background(), input, signers)
	if err != nil {
		t.Fatal(err)
	}
	if signers[0] != beta || signers[1] != alpha {
		t.Fatal("signing mutated caller signer order")
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("signing mutated caller sign document")
	}
	if len(batch.Signatures) != 2 || batch.Signatures[0].IssuerID != "issuer-alpha" || batch.Signatures[1].IssuerID != "issuer-beta" {
		t.Fatalf("signatures are not canonically ordered: %#v", batch.Signatures)
	}
	signBytes, expectedHash, err := CanonicalBatchSignBytes(batch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	if batchHash != expectedHash || hex.EncodeToString(batchHash[:]) != fixture.ExpectedBatchHashHex {
		t.Fatal("signed batch hash mismatch")
	}
	for _, signature := range batch.Signatures {
		publicKey, expectedSignature := fixtureIssuer(t, fixture, signature.IssuerID)
		if !bytes.Equal(signature.Signature, expectedSignature) {
			t.Fatalf("signature for %s differs from fixture", signature.IssuerID)
		}
		if !ed25519.Verify(publicKey, signBytes, signature.Signature) {
			t.Fatalf("signature for %s did not verify", signature.IssuerID)
		}
		if ed25519.Verify(publicKey, batchHash[:], signature.Signature) {
			t.Fatalf("signature for %s verified over batch hash", signature.IssuerID)
		}
	}

	again, againHash, err := SignAuthorizationBatch(context.Background(), input, []BatchSigner{alpha, beta})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batch.Signatures, again.Signatures) || batchHash != againHash {
		t.Fatal("signer input order changed logical signature order")
	}

	wantRecord := batch.SignDoc.Records[0]
	wantHashByte := batch.SignDoc.PolicyHash[0]
	input.Records[1].AuthorizationID = "mutated"
	input.Records[1].BankSendConstraints.Denom = "mutated"
	input.PolicyHash[0] ^= 0xff
	if batch.SignDoc.Records[0] != wantRecord || batch.SignDoc.PolicyHash[0] != wantHashByte {
		t.Fatal("signed batch aliases caller sign document")
	}
}

type testBatchSigner struct {
	issuerID string
	sign     func(context.Context, []byte) ([]byte, error)
}

func (s *testBatchSigner) IssuerID() string { return s.issuerID }
func (s *testBatchSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	return s.sign(ctx, message)
}

func TestSignAuthorizationBatchRejectsInvalidSigners(t *testing.T) {
	_, input := loadCanonicalFixture(t)
	valid := goldenSigner(t, "issuer-alpha", issuerAlphaSeed)
	tests := []struct {
		name    string
		signers []BatchSigner
	}{
		{name: "empty signer list"},
		{name: "nil signer", signers: []BatchSigner{nil}},
		{name: "typed nil signer", signers: []BatchSigner{(*testBatchSigner)(nil)}},
		{name: "empty issuer id", signers: []BatchSigner{&testBatchSigner{sign: func(context.Context, []byte) ([]byte, error) { return make([]byte, ed25519.SignatureSize), nil }}}},
		{name: "duplicate issuer id", signers: []BatchSigner{valid, valid}},
		{name: "malformed signature", signers: []BatchSigner{&testBatchSigner{issuerID: "bad", sign: func(context.Context, []byte) ([]byte, error) { return make([]byte, ed25519.SignatureSize-1), nil }}}},
		{name: "signer error after valid signer", signers: []BatchSigner{valid, &testBatchSigner{issuerID: "failed", sign: func(context.Context, []byte) ([]byte, error) { return nil, errors.New("sign failed") }}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch, batchHash, err := SignAuthorizationBatch(context.Background(), input, tt.signers)
			if err == nil {
				t.Fatal("expected signer error")
			}
			if len(batch.SignDoc.Records) != 0 || len(batch.Signatures) != 0 || batchHash != ([32]byte{}) {
				t.Fatalf("signer error returned partial batch: %#v", batch)
			}
		})
	}
}

func TestNewEd25519BatchSignerRejectsInvalidInput(t *testing.T) {
	if _, err := NewEd25519BatchSigner("", make(ed25519.PrivateKey, ed25519.PrivateKeySize)); err == nil {
		t.Fatal("expected empty issuer error")
	}
	if _, err := NewEd25519BatchSigner("issuer", make(ed25519.PrivateKey, ed25519.PrivateKeySize-1)); err == nil {
		t.Fatal("expected private key length error")
	}
}

func TestBatchObservability(t *testing.T) {
	_, input := loadCanonicalFixture(t)
	alpha := goldenSigner(t, "issuer-alpha", issuerAlphaSeed)
	batch, batchHash, err := SignAuthorizationBatch(context.Background(), input, []BatchSigner{alpha})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	LogBatchBuilt(context.Background(), logger, batch.SignDoc, batchHash)
	LogBatchSigned(context.Background(), logger, batch, batchHash)
	logs := output.String()
	for _, expected := range []string{
		`"msg":"batch_built"`, `"record_count":2`, `"msg":"batch_signed"`,
		`"signature_count":1`, `"batch_id":42`, `"policy_id":"policy-bank-send"`,
		`"policy_version":7`, `"issuer_set_id":9`, `"batch_hash":"14d2938e8ea496984403147ea96e33e792bb5fc2fce0d5cba41b3b90a147ca7f"`,
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("logs missing %s: %s", expected, logs)
		}
	}
	if strings.Contains(logs, hex.EncodeToString(batch.Signatures[0].Signature)) || strings.Contains(logs, issuerAlphaSeed) {
		t.Fatal("batch logs expose signature or private seed")
	}
}
