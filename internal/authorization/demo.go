package authorization

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"spaghetti/internal/remote/policy"
)

const (
	DemoActionGrant          = "grant"
	DemoActionRevoke         = "revoke"
	DemoAuthorizationID      = "demo-bank-send-grant-v1"
	demoPolicyID             = "policy-bank-send"
	demoPolicyVersion        = uint64(1)
	demoIssuerSetID          = uint64(9)
	demoDenom                = "token"
	demoMaxAmount            = "5000"
	demoRepresentativeAmount = "1000"
	demoPolicyHashInput      = "alpha-demo-policy-bank-send-v1"
)

type DemoAuthorizationRequest struct {
	Action          string
	BatchID         uint64
	ChainID         string
	Subject         string
	Receiver        string
	IssuerAlphaSeed []byte
	IssuerBetaSeed  []byte
}

func BuildDemoAuthorizationBatch(ctx context.Context, request DemoAuthorizationRequest) (AuthorizationBatch, [sha256.Size]byte, error) {
	if request.Action != DemoActionGrant && request.Action != DemoActionRevoke {
		return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("invalid demo action %q", request.Action)
	}
	if request.BatchID == 0 {
		return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("demo batch id must be positive")
	}
	if len(request.IssuerAlphaSeed) != ed25519.SeedSize || len(request.IssuerBetaSeed) != ed25519.SeedSize {
		return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("demo issuer seeds must be %d bytes", ed25519.SeedSize)
	}
	record, err := BuildAuthorizationRecord(
		NormalizedMsgSendFacts{
			Subject:    request.Subject,
			MsgTypeURL: MsgSendTypeURL,
			Receiver:   request.Receiver,
			Denom:      demoDenom,
			Amount:     demoRepresentativeAmount,
		},
		policy.PolicyDecision{
			Allow:         true,
			ReasonCode:    policy.ReasonOK,
			PolicyID:      demoPolicyID,
			PolicyVersion: "1",
		},
		TrustedAuthorizationContext{
			AuthorizationID:  DemoAuthorizationID,
			IssuerSetID:      demoIssuerSetID,
			ValidFromHeight:  1,
			ValidUntilHeight: math.MaxInt64,
			AllowedDenom:     demoDenom,
			AllowedReceiver:  request.Receiver,
			MaxAmount:        demoMaxAmount,
		},
	)
	if err != nil {
		return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("build demo authorization record: %w", err)
	}
	if request.Action == DemoActionRevoke {
		record = cloneAuthorizationRecord(record)
		record.Revoked = true
	}
	policyHash := sha256.Sum256([]byte(demoPolicyHashInput))
	signDoc, err := BuildBatchSignDoc(TrustedBatchContext{
		ChainID:       request.ChainID,
		BatchID:       request.BatchID,
		PolicyID:      demoPolicyID,
		PolicyVersion: demoPolicyVersion,
		PolicyHash:    policyHash[:],
		IssuerSetID:   demoIssuerSetID,
	}, []AuthorizationRecord{record})
	if err != nil {
		return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("build demo batch: %w", err)
	}

	alphaKey := ed25519.NewKeyFromSeed(request.IssuerAlphaSeed)
	betaKey := ed25519.NewKeyFromSeed(request.IssuerBetaSeed)
	defer clear(alphaKey)
	defer clear(betaKey)
	alphaSigner, err := NewEd25519BatchSigner("issuer-alpha", alphaKey)
	if err != nil {
		return AuthorizationBatch{}, [sha256.Size]byte{}, err
	}
	betaSigner, err := NewEd25519BatchSigner("issuer-beta", betaKey)
	if err != nil {
		return AuthorizationBatch{}, [sha256.Size]byte{}, err
	}
	return SignAuthorizationBatch(ctx, signDoc, []BatchSigner{alphaSigner, betaSigner})
}

func LoadDemoIssuerSeed(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open issuer seed file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat issuer seed file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("issuer seed file must not grant group or world permissions")
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read issuer seed file: %w", err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(contents)))
	if err != nil {
		return nil, fmt.Errorf("decode issuer seed file: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("issuer seed must decode to %d bytes", ed25519.SeedSize)
	}
	return seed, nil
}
