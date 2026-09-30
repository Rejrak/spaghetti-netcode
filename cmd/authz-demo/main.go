package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"spaghetti/internal/authorization"
)

type options struct {
	action          string
	batchID         uint64
	subject         string
	receiver        string
	submitter       string
	alphad          string
	chainID         string
	keyringBackend  string
	issuerAlphaSeed string
	issuerBetaSeed  string
	home            string
	node            string
	maxAttempts     int
	pollInterval    time.Duration
}

type demoOutput struct {
	Action          string `json:"action"`
	AuthorizationID string `json:"authorization_id"`
	BatchID         uint64 `json:"batch_id"`
	BatchHash       string `json:"batch_hash"`
	TxHash          string `json:"tx_hash"`
	Height          int64  `json:"height"`
	Subject         string `json:"subject"`
	Receiver        string `json:"receiver"`
	MaxAmount       string `json:"max_amount"`
	Denom           string `json:"denom"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	alphaSeed, err := authorization.LoadDemoIssuerSeed(options.issuerAlphaSeed)
	if err != nil {
		return fmt.Errorf("load issuer-alpha seed: %w", err)
	}
	betaSeed, err := authorization.LoadDemoIssuerSeed(options.issuerBetaSeed)
	if err != nil {
		return fmt.Errorf("load issuer-beta seed: %w", err)
	}
	defer clear(alphaSeed)
	defer clear(betaSeed)

	batch, batchHash, err := authorization.BuildDemoAuthorizationBatch(ctx, authorization.DemoAuthorizationRequest{
		Action:          options.action,
		BatchID:         options.batchID,
		ChainID:         options.chainID,
		Subject:         options.subject,
		Receiver:        options.receiver,
		IssuerAlphaSeed: alphaSeed,
		IssuerBetaSeed:  betaSeed,
	})
	if err != nil {
		return err
	}
	publisher, err := authorization.NewAlphadBatchPublisher(authorization.AlphadPublisherConfig{
		BinaryPath:     options.alphad,
		From:           options.submitter,
		ChainID:        options.chainID,
		KeyringBackend: options.keyringBackend,
		Home:           options.home,
		Node:           options.node,
	}, nil, nil)
	if err != nil {
		return err
	}
	confirmer, err := authorization.NewAlphadBatchCommitConfirmer(authorization.AlphadCommitConfig{
		BinaryPath:   options.alphad,
		Node:         options.node,
		Home:         options.home,
		Submitter:    options.submitter,
		MaxAttempts:  options.maxAttempts,
		PollInterval: options.pollInterval,
	}, nil, nil)
	if err != nil {
		return err
	}
	broadcast, err := publisher.Publish(ctx, batch)
	if err != nil {
		return err
	}
	commit, err := confirmer.WaitForCommit(ctx, batch, broadcast)
	if err != nil {
		return err
	}
	output, err := newDemoOutput(options.action, batch, batchHash, commit)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(output)
}

func parseOptions(args []string) (options, error) {
	var parsed options
	flags := flag.NewFlagSet("authz-demo", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&parsed.action, "action", "", "grant or revoke")
	flags.Uint64Var(&parsed.batchID, "batch-id", 0, "positive authorization batch ID")
	flags.StringVar(&parsed.subject, "subject", "", "authorized Cosmos account")
	flags.StringVar(&parsed.receiver, "receiver", "", "allowed Cosmos receiver")
	flags.StringVar(&parsed.submitter, "submitter", "", "Cosmos transaction broadcaster")
	flags.StringVar(&parsed.alphad, "alphad", "", "alphad binary path")
	flags.StringVar(&parsed.chainID, "chain-id", "", "Alpha chain ID")
	flags.StringVar(&parsed.keyringBackend, "keyring-backend", "", "alphad keyring backend")
	flags.StringVar(&parsed.issuerAlphaSeed, "issuer-alpha-seed-file", "", "issuer-alpha Ed25519 seed file")
	flags.StringVar(&parsed.issuerBetaSeed, "issuer-beta-seed-file", "", "issuer-beta Ed25519 seed file")
	flags.StringVar(&parsed.home, "home", "", "optional alphad home")
	flags.StringVar(&parsed.node, "node", "", "optional Alpha RPC node")
	flags.IntVar(&parsed.maxAttempts, "max-attempts", 20, "maximum commit queries")
	flags.DurationVar(&parsed.pollInterval, "poll-interval", 500*time.Millisecond, "commit query interval")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments")
	}
	required := map[string]string{
		"action": parsed.action, "subject": parsed.subject, "receiver": parsed.receiver,
		"submitter": parsed.submitter, "alphad": parsed.alphad, "chain-id": parsed.chainID,
		"keyring-backend": parsed.keyringBackend, "issuer-alpha-seed-file": parsed.issuerAlphaSeed,
		"issuer-beta-seed-file": parsed.issuerBetaSeed,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return options{}, fmt.Errorf("--%s is required", name)
		}
	}
	if parsed.batchID == 0 {
		return options{}, fmt.Errorf("--batch-id must be positive")
	}
	if parsed.action != authorization.DemoActionGrant && parsed.action != authorization.DemoActionRevoke {
		return options{}, fmt.Errorf("--action must be grant or revoke")
	}
	return parsed, nil
}

func newDemoOutput(action string, batch authorization.AuthorizationBatch, batchHash [sha256.Size]byte, commit authorization.CommitResult) (demoOutput, error) {
	if len(batch.SignDoc.Records) != 1 {
		return demoOutput{}, fmt.Errorf("demo batch must contain exactly one record")
	}
	record := batch.SignDoc.Records[0]
	return demoOutput{
		Action:          action,
		AuthorizationID: record.AuthorizationID,
		BatchID:         batch.SignDoc.BatchID,
		BatchHash:       hex.EncodeToString(batchHash[:]),
		TxHash:          commit.TxHash,
		Height:          commit.Height,
		Subject:         record.Subject,
		Receiver:        record.BankSendConstraints.Receiver,
		MaxAmount:       record.BankSendConstraints.MaxAmount,
		Denom:           record.BankSendConstraints.Denom,
	}, nil
}
