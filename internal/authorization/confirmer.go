package authorization

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

type CommitResult struct {
	TxHash string
	Height int64
}

type BatchCommitConfirmer interface {
	WaitForCommit(context.Context, AuthorizationBatch, BroadcastResult) (CommitResult, error)
}

type AlphadCommitConfig struct {
	BinaryPath   string
	Node         string
	Home         string
	Submitter    string
	MaxAttempts  int
	PollInterval time.Duration
}

type AlphadBatchCommitConfirmer struct {
	config AlphadCommitConfig
	runner CommandRunner
	logger *slog.Logger
}

func NewAlphadBatchCommitConfirmer(config AlphadCommitConfig, runner CommandRunner, logger *slog.Logger) (*AlphadBatchCommitConfirmer, error) {
	if strings.TrimSpace(config.BinaryPath) == "" {
		return nil, fmt.Errorf("empty alphad binary path")
	}
	if err := validateAccountAddress(config.Submitter); err != nil {
		return nil, fmt.Errorf("invalid submitter: %w", err)
	}
	if config.MaxAttempts <= 0 {
		return nil, fmt.Errorf("max attempts must be positive")
	}
	if config.PollInterval < 0 {
		return nil, fmt.Errorf("poll interval must not be negative")
	}
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &AlphadBatchCommitConfirmer{config: config, runner: runner, logger: logger}, nil
}

func (c *AlphadBatchCommitConfirmer) WaitForCommit(ctx context.Context, batch AuthorizationBatch, broadcast BroadcastResult) (CommitResult, error) {
	if ctx == nil {
		return CommitResult{}, fmt.Errorf("nil commit context")
	}
	if err := ctx.Err(); err != nil {
		return CommitResult{}, err
	}
	txHash := strings.TrimSpace(broadcast.TxHash)
	if txHash == "" {
		return CommitResult{}, fmt.Errorf("empty broadcast txhash")
	}
	_, batchHash, err := CanonicalBatchSignBytes(batch.SignDoc)
	if err != nil {
		return CommitResult{}, fmt.Errorf("hash authorization batch: %w", err)
	}

	args := []string{"query", "tx", txHash, "--output", "json"}
	if c.config.Home != "" {
		args = append(args, "--home", c.config.Home)
	}
	if c.config.Node != "" {
		args = append(args, "--node", c.config.Node)
	}
	for attempt := 1; attempt <= c.config.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return CommitResult{}, err
		}
		stdout, stderr, runErr := c.runner.Run(ctx, c.config.BinaryPath, args...)
		retryableNotFound := transactionNotFound(stdout, stderr) && (runErr != nil || !hasTransactionHashField(stdout))
		if retryableNotFound {
			if attempt == c.config.MaxAttempts {
				break
			}
			if err := waitForNextAttempt(ctx, c.config.PollInterval); err != nil {
				return CommitResult{}, err
			}
			continue
		}
		if runErr != nil {
			if detail := strings.TrimSpace(string(stderr)); detail != "" {
				return CommitResult{}, fmt.Errorf("alphad tx query failed: %w: %s", runErr, detail)
			}
			return CommitResult{}, fmt.Errorf("alphad tx query failed: %w", runErr)
		}
		response, err := parseCommittedTransaction(stdout, txHash)
		if err != nil {
			return CommitResult{}, err
		}
		quorumWeight, err := correlateBatchApplied(response, batch, batchHash, c.config.Submitter)
		if err != nil {
			return CommitResult{}, err
		}
		LogBatchCommitted(ctx, c.logger, batch, batchHash, txHash, response.Height, quorumWeight)
		return CommitResult{TxHash: txHash, Height: response.Height}, nil
	}
	return CommitResult{}, fmt.Errorf("transaction not committed after %d attempts", c.config.MaxAttempts)
}

func hasTransactionHashField(stdout []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(stdout, &fields) != nil {
		return false
	}
	_, exists := fields["txhash"]
	return exists
}

func waitForNextAttempt(ctx context.Context, interval time.Duration) error {
	if interval == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func transactionNotFound(stdout, stderr []byte) bool {
	text := strings.ToLower(strings.TrimSpace(string(bytes.Join([][]byte{stdout, stderr}, []byte(" ")))))
	if text == "not found" {
		return true
	}
	if strings.Contains(text, "executable file not found") {
		return false
	}
	return strings.Contains(text, "tx not found") || strings.Contains(text, "transaction not found") ||
		(strings.Contains(text, "not found") && (strings.Contains(text, "tx ") || strings.Contains(text, "transaction ")))
}

type flexibleInt64 int64

func (value *flexibleInt64) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*value = flexibleInt64(parsed)
	return nil
}

type committedTransaction struct {
	Height    int64
	TxHash    string
	Code      uint32
	Codespace string
	RawLog    string
	Events    []transactionEvent
}

type transactionEvent struct {
	Type       string                 `json:"type"`
	Attributes []transactionAttribute `json:"attributes"`
}

type transactionAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func parseCommittedTransaction(stdout []byte, expectedTxHash string) (committedTransaction, error) {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return committedTransaction{}, fmt.Errorf("alphad returned empty stdout")
	}
	var wire struct {
		Height    flexibleInt64      `json:"height"`
		TxHash    string             `json:"txhash"`
		Code      uint32             `json:"code"`
		Codespace string             `json:"codespace"`
		RawLog    string             `json:"raw_log"`
		Events    []transactionEvent `json:"events"`
	}
	if err := json.Unmarshal(stdout, &wire); err != nil {
		return committedTransaction{}, fmt.Errorf("decode alphad tx response: %w", err)
	}
	txHash := strings.TrimSpace(wire.TxHash)
	if txHash == "" {
		return committedTransaction{}, fmt.Errorf("alphad tx response has empty txhash")
	}
	if txHash != expectedTxHash {
		return committedTransaction{}, fmt.Errorf("alphad txhash mismatch: got %q want %q", txHash, expectedTxHash)
	}
	height := int64(wire.Height)
	if height <= 0 {
		return committedTransaction{}, fmt.Errorf("invalid committed transaction height %d", height)
	}
	if wire.Code != 0 {
		return committedTransaction{}, fmt.Errorf("committed transaction failed: code=%d codespace=%q raw_log=%q", wire.Code, wire.Codespace, wire.RawLog)
	}
	return committedTransaction{Height: height, TxHash: txHash, Code: wire.Code, Codespace: wire.Codespace, RawLog: wire.RawLog, Events: wire.Events}, nil
}

func correlateBatchApplied(response committedTransaction, batch AuthorizationBatch, batchHash [32]byte, submitter string) (uint64, error) {
	var relevant []transactionEvent
	for _, event := range response.Events {
		if event.Type == "authz_batch_applied" {
			relevant = append(relevant, event)
		}
	}
	if len(relevant) != 1 {
		return 0, fmt.Errorf("expected exactly one authz_batch_applied event, got %d", len(relevant))
	}
	attributes := make(map[string]string, len(relevant[0].Attributes))
	for _, attribute := range relevant[0].Attributes {
		if _, duplicate := attributes[attribute.Key]; duplicate {
			return 0, fmt.Errorf("duplicate authz_batch_applied attribute %q", attribute.Key)
		}
		attributes[attribute.Key] = attribute.Value
	}
	expected := map[string]string{
		"batch_id":       strconv.FormatUint(batch.SignDoc.BatchID, 10),
		"batch_hash":     hex.EncodeToString(batchHash[:]),
		"policy_id":      batch.SignDoc.PolicyID,
		"policy_version": strconv.FormatUint(batch.SignDoc.PolicyVersion, 10),
		"issuer_set_id":  strconv.FormatUint(batch.SignDoc.IssuerSetID, 10),
		"record_count":   strconv.Itoa(len(batch.SignDoc.Records)),
		"submitter":      submitter,
		"height":         strconv.FormatInt(response.Height, 10),
	}
	for key, want := range expected {
		got, exists := attributes[key]
		if !exists {
			return 0, fmt.Errorf("authz_batch_applied missing %s", key)
		}
		if got != want {
			return 0, fmt.Errorf("authz_batch_applied %s mismatch: got %q want %q", key, got, want)
		}
	}
	quorumText, exists := attributes["quorum_weight"]
	if !exists {
		return 0, fmt.Errorf("authz_batch_applied missing quorum_weight")
	}
	quorumWeight, err := strconv.ParseUint(quorumText, 10, 64)
	if err != nil || quorumWeight == 0 {
		return 0, fmt.Errorf("invalid authz_batch_applied quorum_weight %q", quorumText)
	}
	return quorumWeight, nil
}
