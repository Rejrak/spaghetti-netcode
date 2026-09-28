package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"

	v2pb "spaghetti/internal/authorization/pb/v2"

	"google.golang.org/protobuf/proto"
)

const (
	CertificateDomainV2        = "alpha.authzattrs.certificate.v2"
	CertificateTypeURLV2       = "/alpha.authzattrs.v2.AuthorizationCertificateV2"
	maxCertificateBytesV2      = 4096
	maxCertificateSignaturesV2 = 16
	maxFeeCoinsV2              = 4
)

// AuthorizationIntentV2 holds proposed transaction facts, not policy authority.
type AuthorizationIntentV2 struct {
	ChainID       string
	Subject       string
	Receiver      string
	Denom         string
	Amount        string
	AccountNumber uint64
	Sequence      uint64
	TimeoutHeight uint64
	Memo          string
	FeeAmount     []FeeCoinV2
	GasLimit      uint64
}

type FeeCoinV2 struct {
	Denom  string
	Amount string
}

// TrustedCertificateContextV2 must be supplied by the trusted policy layer.
type TrustedCertificateContextV2 struct {
	ChainID          string
	PolicyID         string
	PolicyVersion    uint64
	PolicyHash       []byte
	IssuerSetID      uint64
	ValidFromHeight  int64
	ValidUntilHeight int64
}

type AuthorizationCertificateSignDocV2 struct {
	Domain           string
	Intent           AuthorizationIntentV2
	PolicyID         string
	PolicyVersion    uint64
	PolicyHash       []byte
	IssuerSetID      uint64
	ValidFromHeight  int64
	ValidUntilHeight int64
}

type IssuerSignatureV2 struct {
	IssuerID  string
	Signature []byte
}

type AuthorizationCertificateV2 struct {
	SignDoc    AuthorizationCertificateSignDocV2
	Signatures []IssuerSignatureV2
}

// CertificateSignerV2 is a direct-byte capability. Existing Ed25519 BatchSigner
// values satisfy it without bringing V1 batch orchestration into V2.
type CertificateSignerV2 interface {
	IssuerID() string
	Sign(context.Context, []byte) ([]byte, error)
}

func BuildCertificateSignDocV2(intent AuthorizationIntentV2, trusted TrustedCertificateContextV2) (AuthorizationCertificateSignDocV2, error) {
	if intent.ChainID != "" && intent.ChainID != trusted.ChainID {
		return AuthorizationCertificateSignDocV2{}, fmt.Errorf("intent chain ID differs from trusted chain ID")
	}
	intent.ChainID = trusted.ChainID
	return CanonicalizeCertificateSignDocV2(AuthorizationCertificateSignDocV2{
		Domain: CertificateDomainV2, Intent: intent, PolicyID: trusted.PolicyID,
		PolicyVersion: trusted.PolicyVersion, PolicyHash: trusted.PolicyHash,
		IssuerSetID: trusted.IssuerSetID, ValidFromHeight: trusted.ValidFromHeight,
		ValidUntilHeight: trusted.ValidUntilHeight,
	})
}

func CanonicalizeCertificateSignDocV2(input AuthorizationCertificateSignDocV2) (AuthorizationCertificateSignDocV2, error) {
	if input.Domain != CertificateDomainV2 || strings.TrimSpace(input.Intent.ChainID) == "" ||
		strings.TrimSpace(input.PolicyID) == "" || input.PolicyVersion == 0 ||
		len(input.PolicyHash) != sha256.Size || input.IssuerSetID == 0 ||
		input.ValidFromHeight <= 0 || input.ValidUntilHeight < input.ValidFromHeight {
		return AuthorizationCertificateSignDocV2{}, fmt.Errorf("invalid V2 certificate metadata")
	}
	intent := input.Intent
	if err := validateAccountAddress(intent.Subject); err != nil {
		return AuthorizationCertificateSignDocV2{}, fmt.Errorf("invalid subject: %w", err)
	}
	if err := validateAccountAddress(intent.Receiver); err != nil {
		return AuthorizationCertificateSignDocV2{}, fmt.Errorf("invalid receiver: %w", err)
	}
	if !denomPattern.MatchString(intent.Denom) || !validDecimalV2(intent.Amount, true) || intent.GasLimit == 0 {
		return AuthorizationCertificateSignDocV2{}, fmt.Errorf("invalid V2 transfer or gas")
	}
	if len(intent.FeeAmount) > maxFeeCoinsV2 {
		return AuthorizationCertificateSignDocV2{}, fmt.Errorf("too many fee coins")
	}
	fees := append([]FeeCoinV2(nil), intent.FeeAmount...)
	for _, fee := range fees {
		if !denomPattern.MatchString(fee.Denom) || !validDecimalV2(fee.Amount, false) {
			return AuthorizationCertificateSignDocV2{}, fmt.Errorf("invalid fee coin")
		}
	}
	sort.Slice(fees, func(i, j int) bool { return bytes.Compare([]byte(fees[i].Denom), []byte(fees[j].Denom)) < 0 })
	for i := 1; i < len(fees); i++ {
		if fees[i-1].Denom == fees[i].Denom {
			return AuthorizationCertificateSignDocV2{}, fmt.Errorf("duplicate fee denom")
		}
	}
	intent.FeeAmount = fees
	return AuthorizationCertificateSignDocV2{
		Domain: CertificateDomainV2, Intent: intent, PolicyID: input.PolicyID,
		PolicyVersion: input.PolicyVersion, PolicyHash: append([]byte(nil), input.PolicyHash...),
		IssuerSetID: input.IssuerSetID, ValidFromHeight: input.ValidFromHeight,
		ValidUntilHeight: input.ValidUntilHeight,
	}, nil
}

func validDecimalV2(value string, positive bool) bool {
	if value == "0" && !positive {
		return true
	}
	if !amountPattern.MatchString(value) {
		return false
	}
	n, ok := new(big.Int).SetString(value, 10)
	return ok && n.BitLen() <= 256
}

func CanonicalCertificateSignBytesV2(input AuthorizationCertificateSignDocV2) ([]byte, [sha256.Size]byte, error) {
	canonical, err := CanonicalizeCertificateSignDocV2(input)
	if err != nil {
		return nil, [sha256.Size]byte{}, err
	}
	signBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(toProtoCertificateSignDocV2(canonical))
	if err != nil {
		return nil, [sha256.Size]byte{}, fmt.Errorf("marshal V2 sign doc: %w", err)
	}
	return signBytes, sha256.Sum256(signBytes), nil
}

func BuildAuthorizationCertificateV2(ctx context.Context, intent AuthorizationIntentV2, trusted TrustedCertificateContextV2, signers []CertificateSignerV2) (AuthorizationCertificateV2, []byte, [sha256.Size]byte, error) {
	signDoc, err := BuildCertificateSignDocV2(intent, trusted)
	if err != nil {
		return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, err
	}
	return SignAuthorizationCertificateV2(ctx, signDoc, signers)
}

func SignAuthorizationCertificateV2(ctx context.Context, input AuthorizationCertificateSignDocV2, signers []CertificateSignerV2) (AuthorizationCertificateV2, []byte, [sha256.Size]byte, error) {
	if ctx == nil || ctx.Err() != nil {
		return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, fmt.Errorf("nil or cancelled context")
	}
	if len(signers) == 0 || len(signers) > maxCertificateSignaturesV2 {
		return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, fmt.Errorf("signer count must be 1..16")
	}
	canonical, err := CanonicalizeCertificateSignDocV2(input)
	if err != nil {
		return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, err
	}
	signBytes, digest, err := CanonicalCertificateSignBytesV2(canonical)
	if err != nil {
		return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, err
	}
	seen := make(map[string]struct{}, len(signers))
	signatures := make([]IssuerSignatureV2, 0, len(signers))
	for _, signer := range signers {
		if err := ctx.Err(); err != nil {
			return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, err
		}
		if signer == nil || (reflect.ValueOf(signer).Kind() == reflect.Pointer && reflect.ValueOf(signer).IsNil()) {
			return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, fmt.Errorf("nil signer")
		}
		id := signer.IssuerID()
		if strings.TrimSpace(id) == "" {
			return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, fmt.Errorf("empty issuer ID")
		}
		if _, exists := seen[id]; exists {
			return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, fmt.Errorf("duplicate issuer ID")
		}
		seen[id] = struct{}{}
		sig, err := signer.Sign(ctx, signBytes)
		if err != nil {
			return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, fmt.Errorf("sign V2 certificate for %q: %w", id, err)
		}
		if err := ctx.Err(); err != nil {
			return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, err
		}
		if len(sig) != ed25519.SignatureSize {
			return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, fmt.Errorf("invalid signature length")
		}
		signatures = append(signatures, IssuerSignatureV2{IssuerID: id, Signature: append([]byte(nil), sig...)})
	}
	sort.Slice(signatures, func(i, j int) bool {
		return bytes.Compare([]byte(signatures[i].IssuerID), []byte(signatures[j].IssuerID)) < 0
	})
	cert := AuthorizationCertificateV2{SignDoc: canonical, Signatures: signatures}
	if _, err := MarshalAuthorizationCertificateV2(cert); err != nil {
		return AuthorizationCertificateV2{}, nil, [sha256.Size]byte{}, err
	}
	return cert, signBytes, digest, nil
}

// ToProtoAuthorizationCertificateV2 rebuilds detached wire values after structural validation.
func ToProtoAuthorizationCertificateV2(input AuthorizationCertificateV2) (*v2pb.AuthorizationCertificateV2, error) {
	canonical, err := CanonicalizeCertificateSignDocV2(input.SignDoc)
	if err != nil {
		return nil, err
	}
	if len(input.Signatures) == 0 || len(input.Signatures) > maxCertificateSignaturesV2 {
		return nil, fmt.Errorf("signature count must be 1..16")
	}
	sigs := append([]IssuerSignatureV2(nil), input.Signatures...)
	seen := make(map[string]struct{}, len(sigs))
	for _, sig := range sigs {
		if strings.TrimSpace(sig.IssuerID) == "" || len(sig.Signature) != ed25519.SignatureSize {
			return nil, fmt.Errorf("invalid issuer signature")
		}
		if _, ok := seen[sig.IssuerID]; ok {
			return nil, fmt.Errorf("duplicate issuer ID")
		}
		seen[sig.IssuerID] = struct{}{}
	}
	sort.Slice(sigs, func(i, j int) bool { return bytes.Compare([]byte(sigs[i].IssuerID), []byte(sigs[j].IssuerID)) < 0 })
	out := &v2pb.AuthorizationCertificateV2{SignDoc: toProtoCertificateSignDocV2(canonical)}
	for _, sig := range sigs {
		out.Signatures = append(out.Signatures, &v2pb.IssuerSignatureV2{IssuerId: sig.IssuerID, Signature: append([]byte(nil), sig.Signature...)})
	}
	return out, nil
}

func MarshalAuthorizationCertificateV2(cert AuthorizationCertificateV2) ([]byte, error) {
	wire, err := ToProtoAuthorizationCertificateV2(cert)
	if err != nil {
		return nil, err
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(wire)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxCertificateBytesV2 {
		return nil, fmt.Errorf("V2 certificate exceeds 4096 bytes")
	}
	return encoded, nil
}

func toProtoCertificateSignDocV2(input AuthorizationCertificateSignDocV2) *v2pb.AuthorizationCertificateSignDocV2 {
	intent := input.Intent
	fees := make([]*v2pb.FeeCoinV2, len(intent.FeeAmount))
	for i, fee := range intent.FeeAmount {
		fees[i] = &v2pb.FeeCoinV2{Denom: fee.Denom, Amount: fee.Amount}
	}
	return &v2pb.AuthorizationCertificateSignDocV2{
		Domain: input.Domain, Intent: &v2pb.AuthorizationIntentV2{
			ChainId: intent.ChainID, Subject: intent.Subject, Receiver: intent.Receiver,
			Denom: intent.Denom, Amount: intent.Amount, AccountNumber: intent.AccountNumber,
			Sequence: intent.Sequence, TimeoutHeight: intent.TimeoutHeight, Memo: intent.Memo,
			FeeAmount: fees, GasLimit: intent.GasLimit,
		}, PolicyId: input.PolicyID, PolicyVersion: input.PolicyVersion,
		PolicyHash: append([]byte(nil), input.PolicyHash...), IssuerSetId: input.IssuerSetID,
		ValidFromHeight: input.ValidFromHeight, ValidUntilHeight: input.ValidUntilHeight,
	}
}
