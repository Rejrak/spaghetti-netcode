package authorization

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	authzpb "spaghetti/internal/authorization/pb"

	"google.golang.org/protobuf/proto"
)

type recordingCommandRunner struct {
	stdout    []byte
	stderr    []byte
	err       error
	calls     int
	binary    string
	args      []string
	batchPath string
	batchData []byte
	fileMode  os.FileMode
}

func (r *recordingCommandRunner) Run(_ context.Context, binary string, args ...string) ([]byte, []byte, error) {
	r.calls++
	r.binary = binary
	r.args = append([]string(nil), args...)
	if len(args) >= 4 {
		r.batchPath = args[3]
		info, statErr := os.Stat(r.batchPath)
		if statErr != nil {
			return nil, nil, statErr
		}
		r.fileMode = info.Mode().Perm()
		data, readErr := os.ReadFile(r.batchPath)
		if readErr != nil {
			return nil, nil, readErr
		}
		r.batchData = data
	}
	return r.stdout, r.stderr, r.err
}

func validAlphadPublisherConfig(batch AuthorizationBatch) AlphadPublisherConfig {
	return AlphadPublisherConfig{
		BinaryPath:     "/opt/alpha/bin/alphad",
		From:           batch.SignDoc.Records[0].Subject,
		ChainID:        batch.SignDoc.ChainID,
		KeyringBackend: "test",
	}
}

func TestAlphadBatchPublisherPublishesGoldenBatch(t *testing.T) {
	fixture, batch := goldenLogicalBatch(t)
	original := cloneLogicalBatch(batch)
	expectedWire, err := ToProtoAuthorizationBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{stdout: []byte(`{"txhash":"  A1B2C3  ","code":0}`)}
	config := validAlphadPublisherConfig(batch)
	config.Home = "/tmp/alpha home"
	config.Node = "tcp://127.0.0.1:26657"
	var logs bytes.Buffer
	publisher, err := NewAlphadBatchPublisher(config, runner, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := publisher.Publish(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if result.TxHash != "A1B2C3" {
		t.Fatalf("tx hash: got %q", result.TxHash)
	}
	if !reflect.DeepEqual(batch, original) {
		t.Fatal("publisher mutated caller batch")
	}
	wantArgs := []string{
		"tx", "authzattrs", "submit-authz-batch", runner.batchPath,
		"--from", config.From,
		"--chain-id", config.ChainID,
		"--keyring-backend", config.KeyringBackend,
		"--broadcast-mode", "sync",
		"--output", "json",
		"--yes",
		"--home", config.Home,
		"--node", config.Node,
	}
	if runner.calls != 1 || runner.binary != config.BinaryPath || !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("unexpected command: %q %#v", runner.binary, runner.args)
	}
	if runner.fileMode != 0o600 {
		t.Fatalf("temporary file mode: got %o", runner.fileMode)
	}
	var decoded authzpb.AuthorizationBatch
	if err := proto.Unmarshal(runner.batchData, &decoded); err != nil {
		t.Fatal(err)
	}
	expectedBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(expectedWire)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(runner.batchData, expectedBytes) {
		t.Fatal("temporary file does not contain exact deterministic AuthorizationBatch bytes")
	}
	if !proto.Equal(&decoded, expectedWire) {
		t.Fatal("temporary file is not the expected AuthorizationBatch protobuf")
	}
	if !bytes.Equal(decoded.SignDoc.PolicyHash, expectedWire.SignDoc.PolicyHash) {
		t.Fatal("publisher changed policy hash")
	}
	for i, signature := range decoded.Signatures {
		if !bytes.Equal(signature.Signature, expectedWire.Signatures[i].Signature) {
			t.Fatalf("publisher changed signature %d", i)
		}
	}
	for i, want := range fixture.CanonicalRecordOrder {
		if decoded.SignDoc.Records[i].Subject != want.Subject || decoded.SignDoc.Records[i].MsgTypeUrl != want.MsgTypeURL {
			t.Fatalf("record %d is not canonically ordered", i)
		}
	}
	if _, err := os.Stat(runner.batchPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file still exists after publish: %v", err)
	}
	output := logs.String()
	for _, want := range []string{
		`"msg":"batch_broadcast"`, `"batch_id":42`, `"policy_id":"policy-bank-send"`,
		`"policy_version":7`, `"issuer_set_id":9`, `"signature_count":2`,
		`"batch_hash":"` + fixture.ExpectedBatchHashHex + `"`, `"tx_hash":"A1B2C3"`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("broadcast log missing %s: %s", want, output)
		}
	}
	if strings.Contains(output, "batch_committed") || strings.Contains(output, hex.EncodeToString(batch.Signatures[0].Signature)) {
		t.Fatal("broadcast log contains committed event or signature")
	}
}

func TestAlphadBatchPublisherOmitsOptionalArguments(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	runner := &recordingCommandRunner{stdout: []byte(`{"txhash":"ABC","code":0}`)}
	config := validAlphadPublisherConfig(batch)
	publisher, err := NewAlphadBatchPublisher(config, runner, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"tx", "authzattrs", "submit-authz-batch", runner.batchPath,
		"--from", config.From,
		"--chain-id", config.ChainID,
		"--keyring-backend", config.KeyringBackend,
		"--broadcast-mode", "sync",
		"--output", "json",
		"--yes",
	}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("unexpected command without optional arguments: %#v", runner.args)
	}
}

func TestAlphadBatchPublisherRejectsBroadcastFailures(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	tests := []struct {
		name       string
		stdout     string
		stderr     string
		runnerErr  error
		wantDetail []string
	}{
		{name: "empty stdout", wantDetail: []string{"empty stdout"}},
		{name: "malformed JSON", stdout: `{`, wantDetail: []string{"decode alphad"}},
		{name: "Cosmos code", stdout: `{"txhash":"ABC","code":7,"codespace":"authzattrs","raw_log":"bad signature"}`, wantDetail: []string{"code=7", "authzattrs", "bad signature"}},
		{name: "missing txhash", stdout: `{"code":0}`, wantDetail: []string{"empty txhash"}},
		{name: "runner error with stdout", stdout: `{"txhash":"ABC","code":0}`, stderr: "process failed", runnerErr: errors.New("exit 1"), wantDetail: []string{"exit 1", "process failed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingCommandRunner{stdout: []byte(tt.stdout), stderr: []byte(tt.stderr), err: tt.runnerErr}
			var logs bytes.Buffer
			publisher, err := NewAlphadBatchPublisher(validAlphadPublisherConfig(batch), runner, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			result, err := publisher.Publish(context.Background(), batch)
			if err == nil || result != (BroadcastResult{}) {
				t.Fatalf("expected empty failed result, got %#v err=%v", result, err)
			}
			for _, detail := range tt.wantDetail {
				if !strings.Contains(err.Error(), detail) {
					t.Errorf("error missing %q: %v", detail, err)
				}
			}
			if strings.Contains(logs.String(), "batch_broadcast") {
				t.Fatal("failed publish logged batch_broadcast")
			}
			if _, statErr := os.Stat(runner.batchPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("temporary file still exists after failure: %v", statErr)
			}
		})
	}
}

func TestAlphadBatchPublisherRejectsInvalidInputs(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	base := validAlphadPublisherConfig(batch)
	tests := []struct {
		name   string
		mutate func(*AlphadPublisherConfig)
	}{
		{name: "empty binary", mutate: func(config *AlphadPublisherConfig) { config.BinaryPath = "" }},
		{name: "empty from", mutate: func(config *AlphadPublisherConfig) { config.From = "" }},
		{name: "invalid from", mutate: func(config *AlphadPublisherConfig) { config.From = "not-an-address" }},
		{name: "empty chain ID", mutate: func(config *AlphadPublisherConfig) { config.ChainID = "" }},
		{name: "empty keyring backend", mutate: func(config *AlphadPublisherConfig) { config.KeyringBackend = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := base
			tt.mutate(&config)
			if _, err := NewAlphadBatchPublisher(config, &recordingCommandRunner{}, nil); err == nil {
				t.Fatal("expected config validation error")
			}
		})
	}

	runner := &recordingCommandRunner{}
	publisher, err := NewAlphadBatchPublisher(base, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(nil, batch); err == nil || runner.calls != 0 {
		t.Fatal("nil context did not reject before runner invocation")
	}
	invalidBatch := cloneLogicalBatch(batch)
	invalidBatch.Signatures = nil
	if _, err := publisher.Publish(context.Background(), invalidBatch); err == nil || runner.calls != 0 {
		t.Fatal("batch conversion error did not prevent runner invocation")
	}
}
