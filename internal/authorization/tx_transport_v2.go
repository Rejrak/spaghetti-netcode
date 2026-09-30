package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	cmtrpc "github.com/cometbft/cometbft/rpc/client/http"
	rpctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

// V2TxRPC is the two-method CometBFT RPC surface needed by the one-tx path.
type V2TxRPC interface {
	BroadcastTxSync(context.Context, cmttypes.Tx) (*rpctypes.ResultBroadcastTx, error)
	Tx(context.Context, []byte, bool) (*rpctypes.ResultTx, error)
}

func NewCometV2TxRPC(node string) (V2TxRPC, error) {
	if strings.TrimSpace(node) == "" {
		return nil, fmt.Errorf("empty V2 RPC node")
	}
	return cmtrpc.New(node, "/websocket")
}

type V2BroadcastResult struct{ TxHash string }

type V2TxBroadcaster interface {
	Broadcast(context.Context, []byte) (V2BroadcastResult, error)
}

type CometV2TxBroadcaster struct{ rpc V2TxRPC }

func NewCometV2TxBroadcaster(rpc V2TxRPC) (*CometV2TxBroadcaster, error) {
	if isNilDependency(rpc) {
		return nil, fmt.Errorf("nil V2 RPC client")
	}
	return &CometV2TxBroadcaster{rpc: rpc}, nil
}

func (b *CometV2TxBroadcaster) Broadcast(ctx context.Context, txBytes []byte) (V2BroadcastResult, error) {
	if ctx == nil {
		return V2BroadcastResult{}, fmt.Errorf("nil V2 broadcast context")
	}
	if err := ctx.Err(); err != nil {
		return V2BroadcastResult{}, err
	}
	if len(txBytes) == 0 {
		return V2BroadcastResult{}, fmt.Errorf("empty signed V2 transaction")
	}
	result, err := b.rpc.BroadcastTxSync(ctx, cmttypes.Tx(append([]byte(nil), txBytes...)))
	if ctx.Err() != nil {
		return V2BroadcastResult{}, ctx.Err()
	}
	if err != nil {
		return V2BroadcastResult{}, fmt.Errorf("broadcast signed V2 transaction: %w", err)
	}
	if result == nil {
		return V2BroadcastResult{}, fmt.Errorf("empty V2 broadcast response")
	}
	if result.Code != 0 {
		return V2BroadcastResult{}, fmt.Errorf("V2 CheckTx rejected: code=%d codespace=%q", result.Code, result.Codespace)
	}
	localHash := sha256.Sum256(txBytes)
	if len(result.Hash) != sha256.Size || string(result.Hash) != string(localHash[:]) {
		return V2BroadcastResult{}, fmt.Errorf("V2 broadcast tx hash mismatch")
	}
	return V2BroadcastResult{TxHash: strings.ToUpper(hex.EncodeToString(result.Hash))}, nil
}

type V2InclusionResult struct {
	TxHash    string
	Height    int64
	Code      uint32
	Codespace string
	RawLog    string
}

type V2TxConfirmer interface {
	WaitForInclusion(context.Context, string) (V2InclusionResult, error)
}

type CometV2TxConfirmer struct {
	rpc          V2TxRPC
	maxAttempts  int
	pollInterval time.Duration
}

func NewCometV2TxConfirmer(rpc V2TxRPC, maxAttempts int, pollInterval time.Duration) (*CometV2TxConfirmer, error) {
	if isNilDependency(rpc) || maxAttempts <= 0 || pollInterval < 0 {
		return nil, fmt.Errorf("invalid V2 confirmer configuration")
	}
	return &CometV2TxConfirmer{rpc: rpc, maxAttempts: maxAttempts, pollInterval: pollInterval}, nil
}

func (c *CometV2TxConfirmer) WaitForInclusion(ctx context.Context, txHash string) (V2InclusionResult, error) {
	if ctx == nil {
		return V2InclusionResult{}, fmt.Errorf("nil V2 confirmation context")
	}
	if err := ctx.Err(); err != nil {
		return V2InclusionResult{}, err
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(txHash))
	if err != nil || len(decoded) != sha256.Size {
		return V2InclusionResult{}, fmt.Errorf("invalid V2 transaction hash")
	}
	normalized := strings.ToUpper(hex.EncodeToString(decoded))
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return V2InclusionResult{}, err
		}
		response, err := c.rpc.Tx(ctx, decoded, false)
		if ctx.Err() != nil {
			return V2InclusionResult{}, ctx.Err()
		}
		if err != nil {
			if !v2TxNotFound(err) {
				return V2InclusionResult{}, fmt.Errorf("query V2 transaction: %w", err)
			}
			if attempt < c.maxAttempts {
				if err := waitForNextAttempt(ctx, c.pollInterval); err != nil {
					return V2InclusionResult{}, err
				}
			}
			continue
		}
		if response == nil || response.Height <= 0 || len(response.Hash) != sha256.Size ||
			string(response.Hash) != string(decoded) || len(response.Tx) == 0 {
			return V2InclusionResult{}, fmt.Errorf("invalid or mismatched V2 inclusion response")
		}
		actualHash := sha256.Sum256(response.Tx)
		if string(actualHash[:]) != string(decoded) {
			return V2InclusionResult{}, fmt.Errorf("V2 inclusion transaction bytes mismatch")
		}
		included := V2InclusionResult{
			TxHash: normalized, Height: response.Height, Code: response.TxResult.Code,
			Codespace: response.TxResult.Codespace, RawLog: response.TxResult.Log,
		}
		if included.Code != 0 {
			return included, fmt.Errorf("V2 transaction included with code=%d codespace=%q", included.Code, included.Codespace)
		}
		return included, nil
	}
	return V2InclusionResult{}, fmt.Errorf("V2 transaction not included after %d attempts", c.maxAttempts)
}

func v2TxNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not found") &&
		(strings.Contains(message, "tx (") || strings.Contains(message, "transaction "))
}

// logV2TxEvent intentionally excludes raw bytes, signatures, and key material.
func logV2TxEvent(ctx context.Context, logger *slog.Logger, event, txHash string, digest [sha256.Size]byte, fields ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	args := []any{"component", "v2_transaction", "certificate_digest", hex.EncodeToString(digest[:])}
	if txHash != "" {
		args = append(args, "tx_hash", txHash)
	}
	logger.InfoContext(ctx, event, append(args, fields...)...)
}
