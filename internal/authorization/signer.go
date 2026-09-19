package authorization

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type BatchSigner interface {
	IssuerID() string
	Sign(context.Context, []byte) ([]byte, error)
}

type ed25519BatchSigner struct {
	issuerID   string
	privateKey ed25519.PrivateKey
}

func NewEd25519BatchSigner(issuerID string, privateKey ed25519.PrivateKey) (BatchSigner, error) {
	if strings.TrimSpace(issuerID) == "" {
		return nil, fmt.Errorf("empty issuer id")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid Ed25519 private key length")
	}
	return &ed25519BatchSigner{
		issuerID:   issuerID,
		privateKey: append(ed25519.PrivateKey(nil), privateKey...),
	}, nil
}

func (s *ed25519BatchSigner) IssuerID() string { return s.issuerID }

func (s *ed25519BatchSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	signature := ed25519.Sign(s.privateKey, message)
	if len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid Ed25519 signature length")
	}
	return signature, nil
}

type BatchSignature struct {
	IssuerID  string
	Signature []byte
}

type AuthorizationBatch struct {
	SignDoc    BatchSignDoc
	Signatures []BatchSignature
}

func SignAuthorizationBatch(ctx context.Context, signDoc BatchSignDoc, signers []BatchSigner) (AuthorizationBatch, [sha256.Size]byte, error) {
	if ctx == nil || len(signers) == 0 {
		return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("at least one signer is required")
	}
	canonical, err := CanonicalizeBatchSignDoc(signDoc)
	if err != nil {
		return AuthorizationBatch{}, [sha256.Size]byte{}, err
	}
	signBytes, batchHash, err := CanonicalBatchSignBytes(canonical)
	if err != nil {
		return AuthorizationBatch{}, [sha256.Size]byte{}, err
	}

	signatures := make([]BatchSignature, 0, len(signers))
	seen := make(map[string]struct{}, len(signers))
	for _, signer := range signers {
		if signer == nil || (reflect.ValueOf(signer).Kind() == reflect.Pointer && reflect.ValueOf(signer).IsNil()) {
			return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("nil signer")
		}
		issuerID := signer.IssuerID()
		if strings.TrimSpace(issuerID) == "" {
			return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("empty issuer id")
		}
		if _, exists := seen[issuerID]; exists {
			return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("duplicate issuer id")
		}
		seen[issuerID] = struct{}{}
		signature, err := signer.Sign(ctx, signBytes)
		if err != nil {
			return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("sign batch for issuer %q: %w", issuerID, err)
		}
		if len(signature) != ed25519.SignatureSize {
			return AuthorizationBatch{}, [sha256.Size]byte{}, fmt.Errorf("invalid signature length for issuer %q", issuerID)
		}
		signatures = append(signatures, BatchSignature{
			IssuerID:  issuerID,
			Signature: append([]byte(nil), signature...),
		})
	}
	sort.Slice(signatures, func(i, j int) bool {
		return bytes.Compare([]byte(signatures[i].IssuerID), []byte(signatures[j].IssuerID)) < 0
	})
	return AuthorizationBatch{SignDoc: canonical, Signatures: signatures}, batchHash, nil
}
