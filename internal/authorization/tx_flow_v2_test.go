package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/client"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

type fakeFlowIssuerV2 struct {
	result CertificateIssueResultV2
	err    error
	calls  int
}

func (f *fakeFlowIssuerV2) Issue(_ context.Context, _ CertificateIssueRequestV2) (CertificateIssueResultV2, error) {
	f.calls++
	return f.result, f.err
}

type fakeFlowBroadcasterV2 struct {
	calls int
	bytes []byte
	err   error
	hash  string
}

func (f *fakeFlowBroadcasterV2) Broadcast(_ context.Context, txBytes []byte) (V2BroadcastResult, error) {
	f.calls++
	f.bytes = append([]byte(nil), txBytes...)
	if f.err != nil {
		return V2BroadcastResult{}, f.err
	}
	if f.hash != "" {
		return V2BroadcastResult{TxHash: f.hash}, nil
	}
	hash := sha256.Sum256(txBytes)
	return V2BroadcastResult{TxHash: strings.ToUpper(hex.EncodeToString(hash[:]))}, nil
}

type fakeFlowConfirmerV2 struct {
	calls  int
	txHash string
	result V2InclusionResult
	err    error
}

func (f *fakeFlowConfirmerV2) WaitForInclusion(_ context.Context, txHash string) (V2InclusionResult, error) {
	f.calls++
	f.txHash = txHash
	if f.err != nil {
		return f.result, f.err
	}
	if f.result.TxHash == "" {
		return V2InclusionResult{TxHash: txHash, Height: 51}, nil
	}
	return f.result, nil
}

func flowFixtureV2(t *testing.T) (*V2OneTxService, *fakeFlowIssuerV2, *fakeFlowBroadcasterV2, *fakeFlowConfirmerV2, *int) {
	t.Helper()
	issued, signer := signedV2Fixture(t)
	seed := bytes.Repeat([]byte{9}, ed25519.SeedSize) // TEST-ONLY
	issuerSigner, err := NewEd25519BatchSigner("issuer-alpha", ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	doc := issued.Certificate.SignDoc
	certificate, _, digest, err := BuildAuthorizationCertificateV2(context.Background(), issued.Intent,
		TrustedCertificateContextV2{
			ChainID: doc.Intent.ChainID, PolicyID: doc.PolicyID, PolicyVersion: doc.PolicyVersion,
			PolicyHash: doc.PolicyHash, IssuerSetID: doc.IssuerSetID,
			ValidFromHeight: doc.ValidFromHeight, ValidUntilHeight: doc.ValidUntilHeight,
		}, []CertificateSignerV2{issuerSigner})
	if err != nil {
		t.Fatal(err)
	}
	issued.Certificate = certificate
	issued.Digest = digest
	issued.CertificateBytes, err = MarshalAuthorizationCertificateV2(certificate)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &fakeFlowIssuerV2{result: issued}
	broadcaster := &fakeFlowBroadcasterV2{}
	confirmer := &fakeFlowConfirmerV2{}
	txConfig, err := NewCosmosTxConfigV2()
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewV2OneTxService(issuer, txConfig, signer, broadcaster, confirmer, nil)
	if err != nil {
		t.Fatal(err)
	}
	buildCalls := new(int)
	service.build = func(ctx context.Context, config client.TxConfig, result CertificateIssueResultV2, account CosmosAccountSignerV2) (SignedTransactionV2, error) {
		*buildCalls++
		return BuildSignedV2Transaction(ctx, config, result, account)
	}
	return service, issuer, broadcaster, confirmer, buildCalls
}

func TestV2OneTxFlowSuccess(t *testing.T) {
	service, issuer, broadcaster, confirmer, builds := flowFixtureV2(t)
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	result, err := service.IssueAndSubmitV2(context.Background(), CertificateIssueRequestV2{})
	if err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 1 || *builds != 1 || broadcaster.calls != 1 || confirmer.calls != 1 ||
		confirmer.txHash != result.TxHash || result.Height != 51 || result.Code != 0 ||
		result.Sequence != issuer.result.Intent.Sequence || result.CertificateDigest != issuer.result.Digest ||
		len(broadcaster.bytes) == 0 {
		t.Fatal("V2 path was not exactly issue/build/broadcast/confirm once")
	}
	decoded, err := service.txConfig.TxDecoder()(broadcaster.bytes)
	if err != nil {
		t.Fatal(err)
	}
	shape, ok := decoded.(interface {
		sdk.Tx
		GetExtensionOptions() []*codectypes.Any
	})
	if !ok || len(shape.GetMsgs()) != 1 || len(shape.GetExtensionOptions()) != 1 ||
		shape.GetExtensionOptions()[0].TypeUrl != CertificateTypeURLV2 {
		t.Fatal("broadcast was not one direct V2 transaction")
	}
	if _, ok := shape.GetMsgs()[0].(*banktypes.MsgSend); !ok {
		t.Fatal("broadcast message was not direct MsgSend")
	}
	for _, event := range []string{"v2_tx_built", "v2_tx_broadcast", "v2_tx_confirmed"} {
		if bytes.Count(logs.Bytes(), []byte(event)) != 1 {
			t.Fatalf("expected one %s event", event)
		}
	}
}

func TestV2ZeroFeeTransactionBuilds(t *testing.T) {
	issued, signer := signedV2Fixture(t)
	intent := issued.Intent
	intent.FeeAmount = nil
	doc := issued.Certificate.SignDoc
	certificate, _, digest, err := BuildAuthorizationCertificateV2(context.Background(), intent,
		TrustedCertificateContextV2{
			ChainID: doc.Intent.ChainID, PolicyID: doc.PolicyID, PolicyVersion: doc.PolicyVersion,
			PolicyHash: doc.PolicyHash, IssuerSetID: doc.IssuerSetID,
			ValidFromHeight: doc.ValidFromHeight, ValidUntilHeight: doc.ValidUntilHeight,
		}, []CertificateSignerV2{&fixtureSignerV2{id: "issuer-alpha", signature: bytes.Repeat([]byte{7}, ed25519.SignatureSize)}})
	if err != nil {
		t.Fatal(err)
	}
	issued.Intent, issued.Certificate, issued.Digest = certificate.SignDoc.Intent, certificate, digest
	issued.CertificateBytes, err = MarshalAuthorizationCertificateV2(certificate)
	if err != nil {
		t.Fatal(err)
	}
	config, err := NewCosmosTxConfigV2()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildSignedV2Transaction(context.Background(), config, issued, signer); err != nil {
		t.Fatal("zero-fee V2 transaction failed to build:", err)
	}
}

func TestV2OneTxFlowFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		change      func(*V2OneTxService, *fakeFlowIssuerV2, *fakeFlowBroadcasterV2, *fakeFlowConfirmerV2)
		wantBuild   int
		wantSend    int
		wantConfirm int
		wantError   string
	}{
		{"issuance denied", func(_ *V2OneTxService, i *fakeFlowIssuerV2, _ *fakeFlowBroadcasterV2, _ *fakeFlowConfirmerV2) {
			i.err = &PolicyDeniedError{ReasonCode: "DENY"}
		}, 0, 0, 0, "DENY"},
		{"wrong account signer", func(s *V2OneTxService, _ *fakeFlowIssuerV2, _ *fakeFlowBroadcasterV2, _ *fakeFlowConfirmerV2) {
			s.account.Name = "not-alice"
		}, 1, 0, 0, "account signer"},
		{"tampered certificate after issuance", func(_ *V2OneTxService, i *fakeFlowIssuerV2, _ *fakeFlowBroadcasterV2, _ *fakeFlowConfirmerV2) {
			i.result.CertificateBytes[0] ^= 1
		}, 1, 0, 0, "certificate bytes mismatch"},
		{"broadcast failure", func(_ *V2OneTxService, _ *fakeFlowIssuerV2, b *fakeFlowBroadcasterV2, _ *fakeFlowConfirmerV2) {
			b.err = errors.New("CheckTx rejected")
		}, 1, 1, 0, "CheckTx rejected"},
		{"broadcast wrong hash", func(_ *V2OneTxService, _ *fakeFlowIssuerV2, b *fakeFlowBroadcasterV2, _ *fakeFlowConfirmerV2) {
			b.hash = strings.Repeat("0", 64)
		}, 1, 1, 0, "hash differs"},
		{"stale sequence", func(_ *V2OneTxService, _ *fakeFlowIssuerV2, b *fakeFlowBroadcasterV2, _ *fakeFlowConfirmerV2) {
			b.err = errors.New("account sequence mismatch")
		}, 1, 1, 0, "account sequence mismatch"},
		{"expired certificate", func(_ *V2OneTxService, _ *fakeFlowIssuerV2, b *fakeFlowBroadcasterV2, _ *fakeFlowConfirmerV2) {
			b.err = errors.New("certificate expired")
		}, 1, 1, 0, "certificate expired"},
		{"bad issuer signature", func(_ *V2OneTxService, i *fakeFlowIssuerV2, b *fakeFlowBroadcasterV2, _ *fakeFlowConfirmerV2) {
			i.result.Certificate.Signatures[0].Signature[0] ^= 1
			i.result.CertificateBytes, _ = MarshalAuthorizationCertificateV2(i.result.Certificate)
			b.err = errors.New("invalid issuer signature")
		}, 1, 1, 0, "invalid issuer signature"},
		{"included nonzero code", func(_ *V2OneTxService, _ *fakeFlowIssuerV2, _ *fakeFlowBroadcasterV2, c *fakeFlowConfirmerV2) {
			c.result = V2InclusionResult{Code: 9, Height: 51}
			c.err = errors.New("included with code=9")
		}, 1, 1, 1, "code=9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, issuer, broadcaster, confirmer, builds := flowFixtureV2(t)
			var logs bytes.Buffer
			service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			tc.change(service, issuer, broadcaster, confirmer)
			result, err := service.IssueAndSubmitV2(context.Background(), CertificateIssueRequestV2{})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || issuer.calls != 1 || *builds != tc.wantBuild ||
				broadcaster.calls != tc.wantSend || confirmer.calls != tc.wantConfirm {
				t.Fatal("V2 failure crossed a boundary or retried", err)
			}
			if tc.name == "included nonzero code" && result.Code != 9 {
				t.Fatal("nonzero included code was hidden")
			}
			if bytes.Contains(logs.Bytes(), []byte("v2_tx_confirmed")) {
				t.Fatal("failure logged as confirmed")
			}
		})
	}
}
