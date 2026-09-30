package authorization

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

type commandResult struct {
	stdout []byte
	stderr []byte
	err    error
}

type sequenceCommandRunner struct {
	results []commandResult
	calls   int
	binary  []string
	args    [][]string
	hook    func(int)
}

func (r *sequenceCommandRunner) Run(_ context.Context, binary string, args ...string) ([]byte, []byte, error) {
	r.calls++
	r.binary = append(r.binary, binary)
	r.args = append(r.args, append([]string(nil), args...))
	if r.hook != nil {
		r.hook(r.calls)
	}
	if r.calls > len(r.results) {
		return nil, nil, errors.New("unexpected runner call")
	}
	result := r.results[r.calls-1]
	return result.stdout, result.stderr, result.err
}

type testTxResponse struct {
	Height    any                `json:"height"`
	TxHash    string             `json:"txhash"`
	Code      uint32             `json:"code"`
	Codespace string             `json:"codespace,omitempty"`
	RawLog    string             `json:"raw_log,omitempty"`
	Events    []transactionEvent `json:"events"`
}

func validCommitConfig(batch AuthorizationBatch) AlphadCommitConfig {
	return AlphadCommitConfig{
		BinaryPath:  "/opt/alpha/bin/alphad",
		Submitter:   batch.SignDoc.Records[0].Subject,
		MaxAttempts: 3,
	}
}

func validTxResponse(t *testing.T, batch AuthorizationBatch, config AlphadCommitConfig, txHash string) testTxResponse {
	t.Helper()
	_, batchHash, err := CanonicalBatchSignBytes(batch.SignDoc)
	if err != nil {
		t.Fatal(err)
	}
	return testTxResponse{
		Height: "321",
		TxHash: txHash,
		Events: []transactionEvent{{
			Type: "authz_batch_applied",
			Attributes: []transactionAttribute{
				{Key: "batch_id", Value: "42"},
				{Key: "batch_hash", Value: hex.EncodeToString(batchHash[:])},
				{Key: "policy_id", Value: batch.SignDoc.PolicyID},
				{Key: "policy_version", Value: "7"},
				{Key: "issuer_set_id", Value: "9"},
				{Key: "record_count", Value: "2"},
				{Key: "quorum_weight", Value: "777"},
				{Key: "submitter", Value: config.Submitter},
				{Key: "height", Value: "321"},
			},
		}},
	}
}

func marshalTxResponse(t *testing.T, response testTxResponse) []byte {
	t.Helper()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func setEventAttribute(response *testTxResponse, key, value string) {
	for i := range response.Events[0].Attributes {
		if response.Events[0].Attributes[i].Key == key {
			response.Events[0].Attributes[i].Value = value
			return
		}
	}
}

func removeEventAttribute(response *testTxResponse, key string) {
	attributes := response.Events[0].Attributes
	for i := range attributes {
		if attributes[i].Key == key {
			response.Events[0].Attributes = append(attributes[:i], attributes[i+1:]...)
			return
		}
	}
}

func TestAlphadBatchCommitConfirmerConfirmsGoldenBatch(t *testing.T) {
	fixture, batch := goldenLogicalBatch(t)
	original := cloneLogicalBatch(batch)
	config := validCommitConfig(batch)
	config.Home = "/tmp/alpha home"
	config.Node = "tcp://127.0.0.1:26657"
	response := validTxResponse(t, batch, config, " TX123 ")
	runner := &sequenceCommandRunner{results: []commandResult{{stdout: marshalTxResponse(t, response)}}}
	var logs bytes.Buffer
	confirmer, err := NewAlphadBatchCommitConfirmer(config, runner, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}

	result, err := confirmer.WaitForCommit(context.Background(), batch, BroadcastResult{TxHash: "  TX123  "})
	if err != nil {
		t.Fatal(err)
	}
	if result != (CommitResult{TxHash: "TX123", Height: 321}) {
		t.Fatalf("commit result: %#v", result)
	}
	if !reflect.DeepEqual(batch, original) {
		t.Fatal("confirmer mutated caller batch")
	}
	wantArgs := []string{"query", "tx", "TX123", "--output", "json", "--home", config.Home, "--node", config.Node}
	if runner.calls != 1 || runner.binary[0] != config.BinaryPath || !reflect.DeepEqual(runner.args[0], wantArgs) {
		t.Fatalf("unexpected query command: %q %#v", runner.binary, runner.args)
	}
	output := logs.String()
	for _, want := range []string{
		`"msg":"batch_committed"`, `"batch_id":42`, `"batch_hash":"` + fixture.ExpectedBatchHashHex + `"`,
		`"tx_hash":"TX123"`, `"height":321`, `"policy_id":"policy-bank-send"`,
		`"policy_version":7`, `"issuer_set_id":9`, `"record_count":2`, `"quorum_weight":777`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("commit log missing %s: %s", want, output)
		}
	}
	if strings.Count(output, `"msg":"batch_committed"`) != 1 || strings.Contains(output, "batch_broadcast") || strings.Contains(output, hex.EncodeToString(batch.Signatures[0].Signature)) {
		t.Fatal("confirmer emitted incorrect events")
	}
}

func TestAlphadBatchCommitConfirmerOmitsOptionalArguments(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	config := validCommitConfig(batch)
	runner := &sequenceCommandRunner{results: []commandResult{{stdout: marshalTxResponse(t, validTxResponse(t, batch, config, "TX123"))}}}
	confirmer, err := NewAlphadBatchCommitConfirmer(config, runner, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmer.WaitForCommit(context.Background(), batch, BroadcastResult{TxHash: "TX123"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"query", "tx", "TX123", "--output", "json"}
	if !reflect.DeepEqual(runner.args[0], want) {
		t.Fatalf("unexpected query without optional arguments: %#v", runner.args[0])
	}
}

func TestAlphadBatchCommitConfirmerPollsOnlyNotFound(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	config := validCommitConfig(batch)
	committed := commandResult{stdout: marshalTxResponse(t, validTxResponse(t, batch, config, "TX123"))}
	for name, results := range map[string][]commandResult{
		"once": {
			{stderr: []byte("tx not found"), err: errors.New("exit 1")},
			committed,
		},
		"multiple": {
			{stdout: []byte("transaction not found")},
			{stderr: []byte("rpc error: tx TX123 not found"), err: errors.New("exit 1")},
			committed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config.MaxAttempts = len(results)
			runner := &sequenceCommandRunner{results: results}
			confirmer, err := NewAlphadBatchCommitConfirmer(config, runner, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := confirmer.WaitForCommit(context.Background(), batch, BroadcastResult{TxHash: "TX123"}); err != nil {
				t.Fatal(err)
			}
			if runner.calls != len(results) {
				t.Fatalf("query calls: got %d want %d", runner.calls, len(results))
			}
		})
	}

	config.MaxAttempts = 3
	exhausted := &sequenceCommandRunner{results: []commandResult{
		{stderr: []byte("tx not found"), err: errors.New("exit 1")},
		{stderr: []byte("transaction not found"), err: errors.New("exit 1")},
		{stdout: []byte("not found")},
	}}
	confirmer, err := NewAlphadBatchCommitConfirmer(config, exhausted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmer.WaitForCommit(context.Background(), batch, BroadcastResult{TxHash: "TX123"}); err == nil || !strings.Contains(err.Error(), "not committed after 3 attempts") || exhausted.calls != 3 {
		t.Fatalf("unexpected exhaustion: calls=%d err=%v", exhausted.calls, err)
	}

	fatalRunner := &sequenceCommandRunner{results: []commandResult{{stderr: []byte("invalid node address"), err: errors.New("exit 1")}}}
	fatalConfirmer, err := NewAlphadBatchCommitConfirmer(config, fatalRunner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fatalConfirmer.WaitForCommit(context.Background(), batch, BroadcastResult{TxHash: "TX123"}); err == nil || fatalRunner.calls != 1 {
		t.Fatalf("unrelated command failure was retried: calls=%d err=%v", fatalRunner.calls, err)
	}
}

func TestAlphadBatchCommitConfirmerHonorsContext(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	config := validCommitConfig(batch)
	config.PollInterval = time.Hour

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &sequenceCommandRunner{}
	confirmer, err := NewAlphadBatchCommitConfirmer(config, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmer.WaitForCommit(cancelled, batch, BroadcastResult{TxHash: "TX123"}); !errors.Is(err, context.Canceled) || runner.calls != 0 {
		t.Fatalf("pre-cancel result: calls=%d err=%v", runner.calls, err)
	}

	waiting, stop := context.WithCancel(context.Background())
	waitRunner := &sequenceCommandRunner{
		results: []commandResult{{stderr: []byte("tx not found"), err: errors.New("exit 1")}},
		hook:    func(int) { stop() },
	}
	waitConfirmer, err := NewAlphadBatchCommitConfirmer(config, waitRunner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitConfirmer.WaitForCommit(waiting, batch, BroadcastResult{TxHash: "TX123"}); !errors.Is(err, context.Canceled) || waitRunner.calls != 1 {
		t.Fatalf("wait cancellation result: calls=%d err=%v", waitRunner.calls, err)
	}
}

func TestAlphadBatchCommitConfirmerRejectsInvalidResponses(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	config := validCommitConfig(batch)
	validOutput := func(mutate func(*testTxResponse)) []byte {
		response := validTxResponse(t, batch, config, "TX123")
		if mutate != nil {
			mutate(&response)
		}
		return marshalTxResponse(t, response)
	}
	tests := []struct {
		name       string
		output     []byte
		wantDetail []string
	}{
		{name: "empty stdout", output: nil, wantDetail: []string{"empty stdout"}},
		{name: "malformed JSON", output: []byte(`{`), wantDetail: []string{"decode alphad"}},
		{name: "empty returned txhash", output: validOutput(func(response *testTxResponse) { response.TxHash = "" }), wantDetail: []string{"empty txhash"}},
		{name: "mismatched txhash", output: validOutput(func(response *testTxResponse) { response.TxHash = "OTHER" }), wantDetail: []string{"txhash mismatch"}},
		{name: "zero height", output: validOutput(func(response *testTxResponse) { response.Height = "0" }), wantDetail: []string{"invalid committed transaction height"}},
		{name: "invalid height", output: validOutput(func(response *testTxResponse) { response.Height = "bad" }), wantDetail: []string{"decode alphad"}},
		{name: "committed code", output: validOutput(func(response *testTxResponse) {
			response.Code, response.Codespace, response.RawLog = 8, "authzattrs", "tx not found: bad quorum"
		}), wantDetail: []string{"code=8", "authzattrs", "tx not found: bad quorum"}},
		{name: "missing event", output: validOutput(func(response *testTxResponse) { response.Events = nil }), wantDetail: []string{"exactly one", "got 0"}},
		{name: "duplicate event", output: validOutput(func(response *testTxResponse) { response.Events = append(response.Events, response.Events[0]) }), wantDetail: []string{"exactly one", "got 2"}},
		{name: "missing batch id", output: validOutput(func(response *testTxResponse) { removeEventAttribute(response, "batch_id") }), wantDetail: []string{"missing batch_id"}},
		{name: "mismatched batch id", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "batch_id", "43") }), wantDetail: []string{"batch_id mismatch"}},
		{name: "mismatched batch hash", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "batch_hash", "00") }), wantDetail: []string{"batch_hash mismatch"}},
		{name: "mismatched policy id", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "policy_id", "other") }), wantDetail: []string{"policy_id mismatch"}},
		{name: "mismatched policy version", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "policy_version", "8") }), wantDetail: []string{"policy_version mismatch"}},
		{name: "mismatched issuer set", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "issuer_set_id", "10") }), wantDetail: []string{"issuer_set_id mismatch"}},
		{name: "mismatched record count", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "record_count", "3") }), wantDetail: []string{"record_count mismatch"}},
		{name: "mismatched submitter", output: validOutput(func(response *testTxResponse) {
			setEventAttribute(response, "submitter", batch.SignDoc.Records[1].Subject)
		}), wantDetail: []string{"submitter mismatch"}},
		{name: "mismatched event height", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "height", "322") }), wantDetail: []string{"height mismatch"}},
		{name: "missing quorum", output: validOutput(func(response *testTxResponse) { removeEventAttribute(response, "quorum_weight") }), wantDetail: []string{"missing quorum_weight"}},
		{name: "zero quorum", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "quorum_weight", "0") }), wantDetail: []string{"invalid", "quorum_weight"}},
		{name: "invalid quorum", output: validOutput(func(response *testTxResponse) { setEventAttribute(response, "quorum_weight", "nope") }), wantDetail: []string{"invalid", "quorum_weight"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &sequenceCommandRunner{results: []commandResult{{stdout: tt.output}}}
			var logs bytes.Buffer
			confirmer, err := NewAlphadBatchCommitConfirmer(config, runner, slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			result, err := confirmer.WaitForCommit(context.Background(), batch, BroadcastResult{TxHash: "TX123"})
			if err == nil || result != (CommitResult{}) || runner.calls != 1 {
				t.Fatalf("expected final rejection: result=%#v calls=%d err=%v", result, runner.calls, err)
			}
			for _, detail := range tt.wantDetail {
				if !strings.Contains(err.Error(), detail) {
					t.Errorf("error missing %q: %v", detail, err)
				}
			}
			if strings.Contains(logs.String(), "batch_committed") {
				t.Fatal("failed confirmation logged batch_committed")
			}
		})
	}
}

func TestAlphadBatchCommitConfirmerRejectsInvalidInputs(t *testing.T) {
	_, batch := goldenLogicalBatch(t)
	base := validCommitConfig(batch)
	tests := []struct {
		name   string
		mutate func(*AlphadCommitConfig)
	}{
		{name: "empty binary", mutate: func(config *AlphadCommitConfig) { config.BinaryPath = "" }},
		{name: "empty submitter", mutate: func(config *AlphadCommitConfig) { config.Submitter = "" }},
		{name: "invalid submitter", mutate: func(config *AlphadCommitConfig) { config.Submitter = "not-an-address" }},
		{name: "zero attempts", mutate: func(config *AlphadCommitConfig) { config.MaxAttempts = 0 }},
		{name: "negative interval", mutate: func(config *AlphadCommitConfig) { config.PollInterval = -time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := base
			tt.mutate(&config)
			if _, err := NewAlphadBatchCommitConfirmer(config, &sequenceCommandRunner{}, nil); err == nil {
				t.Fatal("expected config validation error")
			}
		})
	}

	runner := &sequenceCommandRunner{}
	confirmer, err := NewAlphadBatchCommitConfirmer(base, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confirmer.WaitForCommit(context.Background(), batch, BroadcastResult{TxHash: "   "}); err == nil || runner.calls != 0 {
		t.Fatal("empty txhash did not reject before runner invocation")
	}
	if _, err := confirmer.WaitForCommit(nil, batch, BroadcastResult{TxHash: "TX123"}); err == nil || runner.calls != 0 {
		t.Fatal("nil context did not reject before runner invocation")
	}
}
