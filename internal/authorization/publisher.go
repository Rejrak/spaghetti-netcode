package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"google.golang.org/protobuf/proto"
)

type BatchPublisher interface {
	Publish(context.Context, AuthorizationBatch) (BroadcastResult, error)
}

type BroadcastResult struct {
	TxHash string
}

type AlphadPublisherConfig struct {
	BinaryPath     string
	From           string
	ChainID        string
	KeyringBackend string
	Home           string
	Node           string
}

type CommandRunner interface {
	Run(ctx context.Context, binary string, args ...string) (stdout, stderr []byte, err error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, binary string, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

type AlphadBatchPublisher struct {
	config AlphadPublisherConfig
	runner CommandRunner
	logger *slog.Logger
}

func NewAlphadBatchPublisher(config AlphadPublisherConfig, runner CommandRunner, logger *slog.Logger) (*AlphadBatchPublisher, error) {
	if strings.TrimSpace(config.BinaryPath) == "" {
		return nil, fmt.Errorf("empty alphad binary path")
	}
	if strings.TrimSpace(config.From) == "" {
		return nil, fmt.Errorf("empty alphad from address")
	}
	if err := validateAccountAddress(config.From); err != nil {
		return nil, fmt.Errorf("invalid alphad from address: %w", err)
	}
	if strings.TrimSpace(config.ChainID) == "" {
		return nil, fmt.Errorf("empty alphad chain id")
	}
	if strings.TrimSpace(config.KeyringBackend) == "" {
		return nil, fmt.Errorf("empty alphad keyring backend")
	}
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &AlphadBatchPublisher{config: config, runner: runner, logger: logger}, nil
}

func (p *AlphadBatchPublisher) Publish(ctx context.Context, batch AuthorizationBatch) (BroadcastResult, error) {
	if ctx == nil {
		return BroadcastResult{}, fmt.Errorf("nil publish context")
	}
	wireBatch, err := ToProtoAuthorizationBatch(batch)
	if err != nil {
		return BroadcastResult{}, fmt.Errorf("convert authorization batch: %w", err)
	}
	batchBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(wireBatch)
	if err != nil {
		return BroadcastResult{}, fmt.Errorf("marshal authorization batch: %w", err)
	}
	_, batchHash, err := CanonicalBatchSignBytes(batch.SignDoc)
	if err != nil {
		return BroadcastResult{}, fmt.Errorf("hash authorization batch: %w", err)
	}

	file, err := os.CreateTemp("", "alpha-authorization-batch-*.pb")
	if err != nil {
		return BroadcastResult{}, fmt.Errorf("create authorization batch file: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return BroadcastResult{}, fmt.Errorf("restrict authorization batch file: %w", err)
	}
	if written, err := file.Write(batchBytes); err != nil {
		file.Close()
		return BroadcastResult{}, fmt.Errorf("write authorization batch file: %w", err)
	} else if written != len(batchBytes) {
		file.Close()
		return BroadcastResult{}, fmt.Errorf("write authorization batch file: %w", io.ErrShortWrite)
	}
	if err := file.Close(); err != nil {
		return BroadcastResult{}, fmt.Errorf("close authorization batch file: %w", err)
	}

	args := []string{
		"tx", "authzattrs", "submit-authz-batch", path,
		"--from", p.config.From,
		"--chain-id", p.config.ChainID,
		"--keyring-backend", p.config.KeyringBackend,
		"--broadcast-mode", "sync",
		"--output", "json",
		"--yes",
	}
	if p.config.Home != "" {
		args = append(args, "--home", p.config.Home)
	}
	if p.config.Node != "" {
		args = append(args, "--node", p.config.Node)
	}
	stdout, stderr, err := p.runner.Run(ctx, p.config.BinaryPath, args...)
	if err != nil {
		if detail := strings.TrimSpace(string(stderr)); detail != "" {
			return BroadcastResult{}, fmt.Errorf("alphad broadcast failed: %w: %s", err, detail)
		}
		return BroadcastResult{}, fmt.Errorf("alphad broadcast failed: %w", err)
	}
	if len(bytes.TrimSpace(stdout)) == 0 {
		return BroadcastResult{}, fmt.Errorf("alphad returned empty stdout")
	}
	var response struct {
		TxHash    string `json:"txhash"`
		Code      uint32 `json:"code"`
		Codespace string `json:"codespace"`
		RawLog    string `json:"raw_log"`
	}
	if err := json.Unmarshal(stdout, &response); err != nil {
		return BroadcastResult{}, fmt.Errorf("decode alphad broadcast response: %w", err)
	}
	if response.Code != 0 {
		return BroadcastResult{}, fmt.Errorf("alphad rejected transaction: code=%d codespace=%q raw_log=%q", response.Code, response.Codespace, response.RawLog)
	}
	txHash := strings.TrimSpace(response.TxHash)
	if txHash == "" {
		return BroadcastResult{}, fmt.Errorf("alphad returned empty txhash")
	}

	LogBatchBroadcast(ctx, p.logger, batch, batchHash, txHash)
	return BroadcastResult{TxHash: txHash}, nil
}
