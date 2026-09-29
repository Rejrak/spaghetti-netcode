package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"log/slog"
	"math"
	"reflect"
	"testing"

	"spaghetti/internal/remote/policy"
	"spaghetti/internal/user"

	"google.golang.org/protobuf/proto"
	v2pb "spaghetti/internal/authorization/pb/v2"
)

type fixtureAlphaStateV2 struct {
	state   AlphaAccountStateV2
	err     error
	calls   int
	subject string
}

func (p *fixtureAlphaStateV2) AccountState(_ context.Context, subject string) (AlphaAccountStateV2, error) {
	p.calls++
	p.subject = subject
	return p.state, p.err
}

func issuerV2Fixture(t *testing.T) (*CertificateIssuerV2, CertificateIssueRequestV2, *issuerAttributeSource, *issuerPolicyEvaluator, *fixtureAlphaStateV2, *fixtureSignerV2) {
	t.Helper()
	intent, trusted := goldenInputsV2()
	request := CertificateIssueRequestV2{
		Subject: intent.Subject, Receiver: intent.Receiver, Denom: intent.Denom,
		Amount: intent.Amount, TimeoutHeight: intent.TimeoutHeight, Memo: intent.Memo,
		FeeAmount: append([]FeeCoinV2(nil), intent.FeeAmount...), GasLimit: intent.GasLimit,
	}
	source := &issuerAttributeSource{attributes: &user.Attributes{Perms: map[string]bool{"sensitive-permission": true}, Roles: []string{"sensitive-role"}}}
	evaluator := &issuerPolicyEvaluator{decision: policy.PolicyDecision{
		Allow: true, ReasonCode: policy.ReasonOK, PolicyID: trusted.PolicyID, PolicyVersion: "2",
	}}
	state := &fixtureAlphaStateV2{state: AlphaAccountStateV2{
		ChainID: trusted.ChainID, AccountNumber: intent.AccountNumber,
		Sequence: intent.Sequence, CurrentHeight: trusted.ValidFromHeight,
	}}
	signer := &fixtureSignerV2{id: "issuer-alpha", signature: bytes.Repeat([]byte{7}, ed25519.SignatureSize)}
	config := CertificateIssuerV2Config{
		ChainID: trusted.ChainID, PolicyID: trusted.PolicyID, PolicyVersion: trusted.PolicyVersion,
		PolicyHash: trusted.PolicyHash, IssuerSetID: trusted.IssuerSetID, LifetimeBlocks: 11,
	}
	service, err := NewCertificateIssuerV2(source, evaluator, state, []CertificateSignerV2{signer}, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	config.PolicyHash[0] ^= 1
	if service.config.PolicyHash[0] == config.PolicyHash[0] {
		t.Fatal("trusted config hash aliases caller memory")
	}
	return service, request, source, evaluator, state, signer
}

func TestCertificateIssuerV2GoldenCompatibleIssuance(t *testing.T) {
	service, request, source, evaluator, state, signer := issuerV2Fixture(t)
	beta := &fixtureSignerV2{id: "issuer-beta", signature: bytes.Repeat([]byte{8}, ed25519.SignatureSize)}
	service.signers = []CertificateSignerV2{beta, signer}
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	originalFees := append([]FeeCoinV2(nil), request.FeeAmount...)
	result, err := service.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || source.address != request.Subject || evaluator.calls != 1 ||
		evaluator.input.Subject != request.Subject || evaluator.input.Operation != MsgSendTypeURL ||
		evaluator.input.Attributes != source.attributes || state.calls != 1 || state.subject != request.Subject {
		t.Fatal("incorrect attribute, policy, or trusted-state call")
	}
	if !reflect.DeepEqual(request.FeeAmount, originalFees) {
		t.Fatal("request mutated")
	}
	doc := result.Certificate.SignDoc
	if !reflect.DeepEqual(result.Intent, doc.Intent) || doc.Intent.AccountNumber != 7 || doc.Intent.Sequence != 3 ||
		doc.Intent.ChainID != "alpha-1" || doc.PolicyID != "policy-bank-send" || doc.PolicyVersion != 2 ||
		doc.IssuerSetID != 9 || doc.ValidFromHeight != 100 || doc.ValidUntilHeight != 110 ||
		result.AccountState != state.state {
		t.Fatal("trusted state or policy not propagated exactly")
	}
	signBytes, digest, err := CanonicalCertificateSignBytesV2(doc)
	if err != nil || hex.EncodeToString(signBytes) != goldenSignBytesV2 || hex.EncodeToString(digest[:]) != goldenDigestV2 || result.Digest != digest ||
		!bytes.Equal(signer.message, signBytes) || !bytes.Equal(beta.message, signBytes) {
		t.Fatal("issuance is not golden-compatible")
	}
	var wire v2pb.AuthorizationCertificateV2
	if err := proto.Unmarshal(result.CertificateBytes, &wire); err != nil || wire.SignDoc == nil || len(wire.Signatures) != 2 ||
		wire.Signatures[0].IssuerId != "issuer-alpha" || wire.Signatures[1].IssuerId != "issuer-beta" {
		t.Fatal("invalid V2 certificate wire output")
	}
	if !bytes.Contains(logs.Bytes(), []byte("v2_policy_evaluated")) || !bytes.Contains(logs.Bytes(), []byte("v2_certificate_built")) || !bytes.Contains(logs.Bytes(), []byte("v2_certificate_signed")) ||
		bytes.Contains(logs.Bytes(), []byte("sensitive-role")) || bytes.Contains(logs.Bytes(), []byte("sensitive-permission")) {
		t.Fatal("incorrect or sensitive V2 observability")
	}
	request.FeeAmount[0].Amount = "999"
	service.config.PolicyHash[0] ^= 1
	if doc.Intent.FeeAmount[0].Amount != "100" || result.Certificate.SignDoc.PolicyHash[0] == service.config.PolicyHash[0] {
		t.Fatal("returned certificate aliases caller or mutable config")
	}
}

func TestCertificateIssuerV2LogsOnlyPublicCorrelation(t *testing.T) {
	service, request, source, _, _, _ := issuerV2Fixture(t)
	const privateAttribute = "private-policy-attribute-marker"
	const clientSecret = "private-keycloak-client-secret-marker"
	source.attributes.Roles = []string{privateAttribute}
	source.attributes.Perms = map[string]bool{clientSecret: true}
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize) // TEST-ONLY
	privateKey := ed25519.NewKeyFromSeed(seed)
	signer, err := NewEd25519BatchSigner("issuer-alpha", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	service.signers = []CertificateSignerV2{signer}
	var logs bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	issued, err := service.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	events := decodeV2EventLogs(t, logs.Bytes())
	if len(events) != 3 {
		t.Fatalf("unexpected V2 issuance events: %v", events)
	}
	policyEvent := events["v2_policy_evaluated"]
	if policyEvent == nil || policyEvent["subject"] != request.Subject || policyEvent["outcome"] != "allow" ||
		policyEvent["policy_id"] != issued.Certificate.SignDoc.PolicyID || policyEvent["policy_version"] != "2" {
		t.Fatal("policy evaluation correlation fields missing")
	}
	for _, name := range []string{"v2_certificate_built", "v2_certificate_signed"} {
		event := events[name]
		if event == nil || event["subject"] != request.Subject || event["sequence"] != float64(issued.Intent.Sequence) ||
			event["policy_id"] != issued.Certificate.SignDoc.PolicyID ||
			event["policy_version"] != float64(issued.Certificate.SignDoc.PolicyVersion) ||
			event["issuer_set_id"] != float64(issued.Certificate.SignDoc.IssuerSetID) ||
			event["certificate_digest"] != hex.EncodeToString(issued.Digest[:]) {
			t.Fatalf("%s correlation fields missing: %v", name, event)
		}
	}
	if events["v2_certificate_signed"]["signature_count"] != float64(1) {
		t.Fatal("signed event lacks issuer signature count")
	}
	for _, forbidden := range []string{privateAttribute, clientSecret, hex.EncodeToString(seed),
		hex.EncodeToString(privateKey), hex.EncodeToString(issued.Certificate.Signatures[0].Signature),
		hex.EncodeToString(issued.CertificateBytes)} {
		if bytes.Contains(logs.Bytes(), []byte(forbidden)) {
			t.Fatal("V2 issuance log exposed secret or policy attribute contents")
		}
	}
}

func TestCertificateIssuerV2FailClosed(t *testing.T) {
	boom := errors.New("unavailable")
	for _, tc := range []struct {
		name       string
		change     func(*CertificateIssuerV2, *CertificateIssueRequestV2, *issuerAttributeSource, *issuerPolicyEvaluator, *fixtureAlphaStateV2, *fixtureSignerV2)
		wantSource int
		wantEval   int
		wantState  int
	}{
		{"invalid subject", func(_ *CertificateIssuerV2, r *CertificateIssueRequestV2, _ *issuerAttributeSource, _ *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			r.Subject = "invalid"
		}, 0, 0, 0},
		{"invalid amount", func(_ *CertificateIssuerV2, r *CertificateIssueRequestV2, _ *issuerAttributeSource, _ *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			r.Amount = "01"
		}, 0, 0, 0},
		{"attribute error", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, s *issuerAttributeSource, _ *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			s.err = boom
		}, 1, 0, 0},
		{"missing attributes", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, s *issuerAttributeSource, _ *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			s.attributes = nil
		}, 1, 0, 0},
		{"evaluator error", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, e *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			e.err = boom
		}, 1, 1, 0},
		{"policy deny", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, e *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			e.decision.Allow = false
		}, 1, 1, 0},
		{"policy ID mismatch", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, e *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			e.decision.PolicyID = "other"
		}, 1, 1, 0},
		{"policy version mismatch", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, e *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			e.decision.PolicyVersion = "3"
		}, 1, 1, 0},
		{"noncanonical policy version", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, e *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			e.decision.PolicyVersion = "02"
		}, 1, 1, 0},
		{"state error", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, _ *issuerPolicyEvaluator, p *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			p.err = boom
		}, 1, 1, 1},
		{"wrong chain", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, _ *issuerPolicyEvaluator, p *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			p.state.ChainID = "other"
		}, 1, 1, 1},
		{"empty chain", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, _ *issuerPolicyEvaluator, p *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			p.state.ChainID = ""
		}, 1, 1, 1},
		{"invalid height", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, _ *issuerPolicyEvaluator, p *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			p.state.CurrentHeight = 0
		}, 1, 1, 1},
		{"height overflow", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, _ *issuerPolicyEvaluator, p *fixtureAlphaStateV2, _ *fixtureSignerV2) {
			p.state.CurrentHeight = math.MaxInt64
		}, 1, 1, 1},
		{"signer error", func(_ *CertificateIssuerV2, _ *CertificateIssueRequestV2, _ *issuerAttributeSource, _ *issuerPolicyEvaluator, _ *fixtureAlphaStateV2, signer *fixtureSignerV2) {
			signer.err = boom
		}, 1, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, request, source, evaluator, state, signer := issuerV2Fixture(t)
			var logs bytes.Buffer
			service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			tc.change(service, &request, source, evaluator, state, signer)
			result, err := service.Issue(context.Background(), request)
			if err == nil || len(result.CertificateBytes) != 0 || len(result.Certificate.Signatures) != 0 {
				t.Fatal("failure returned a certificate")
			}
			if source.calls != tc.wantSource || evaluator.calls != tc.wantEval || state.calls != tc.wantState {
				t.Fatal("unexpected external calls after failure")
			}
			if bytes.Contains(logs.Bytes(), []byte("v2_certificate_signed")) {
				t.Fatal("signed event emitted after failure")
			}
			if tc.name == "policy deny" {
				var denied *PolicyDeniedError
				if !errors.As(err, &denied) || !bytes.Contains(logs.Bytes(), []byte("v2_policy_evaluated")) {
					t.Fatal("policy denial not observable and typed")
				}
			}
		})
	}
}

func TestCertificateIssuerV2TrustedConfiguration(t *testing.T) {
	service, request, source, evaluator, state, signer := issuerV2Fixture(t)
	config := service.config
	for _, tc := range []struct {
		name   string
		change func(*CertificateIssuerV2Config)
	}{
		{"chain", func(c *CertificateIssuerV2Config) { c.ChainID = "" }},
		{"policy", func(c *CertificateIssuerV2Config) { c.PolicyID = "" }},
		{"version", func(c *CertificateIssuerV2Config) { c.PolicyVersion = 0 }},
		{"hash", func(c *CertificateIssuerV2Config) { c.PolicyHash = c.PolicyHash[:31] }},
		{"issuer set", func(c *CertificateIssuerV2Config) { c.IssuerSetID = 0 }},
		{"lifetime", func(c *CertificateIssuerV2Config) { c.LifetimeBlocks = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := config
			tc.change(&bad)
			if _, err := NewCertificateIssuerV2(source, evaluator, state, []CertificateSignerV2{signer}, bad, nil); err == nil {
				t.Fatal("accepted invalid trusted configuration")
			}
		})
	}
	if _, err := NewCertificateIssuerV2(nil, evaluator, state, []CertificateSignerV2{signer}, config, nil); err == nil {
		t.Fatal("accepted nil attribute source")
	}
	if _, err := NewCertificateIssuerV2(source, evaluator, state, nil, config, nil); err == nil {
		t.Fatal("accepted no signers")
	}
	for _, forbidden := range []string{"ChainID", "AccountNumber", "Sequence", "PolicyID", "PolicyVersion", "PolicyHash", "IssuerSetID", "ValidFromHeight", "ValidUntilHeight", "Quorum"} {
		if _, exists := reflect.TypeOf(request).FieldByName(forbidden); exists {
			t.Fatalf("request exposes trusted field %s", forbidden)
		}
	}
	if _, err := service.Issue(nil, request); err == nil {
		t.Fatal("accepted nil context")
	}
	if source.calls != 0 || state.calls != 0 {
		t.Fatal("invalid context caused external calls")
	}
}
