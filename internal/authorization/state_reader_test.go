package authorization

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type stateQueryResult struct {
	stdout []byte
	stderr []byte
	err    error
}

type stateQueryRunner struct {
	results  []stateQueryResult
	calls    int
	binaries []string
	args     [][]string
}

func (r *stateQueryRunner) Run(_ context.Context, binary string, args ...string) ([]byte, []byte, error) {
	r.binaries = append(r.binaries, binary)
	r.args = append(r.args, append([]string(nil), args...))
	result := r.results[r.calls]
	r.calls++
	return result.stdout, result.stderr, result.err
}

func TestAlphadAuthorizationStateReaderCommandsAndParsing(t *testing.T) {
	recordJSON := `{"authorization":{"authorization_id":"auth-1","subject":"` + testSubject + `","msg_type_url":"` + MsgSendTypeURL + `","policy_id":"policy-bank-send","policy_version":"7","issuer_set_id":8,"valid_from_height":"10","valid_until_height":100,"revoked":true,"bank_send_constraints":{"denom":"uatom","receiver":"` + testReceiver + `","max_amount":"5000"}},"found":true}`
	runner := &stateQueryRunner{results: []stateQueryResult{
		{stdout: []byte(recordJSON)},
		{stdout: []byte(`{"issuer_set_id":"9","found":true}`)},
		{stdout: []byte(`{"batch_id":41,"found":true}`)},
	}}
	reader, err := NewAlphadAuthorizationStateReader(AlphadAuthorizationStateReaderConfig{
		BinaryPath: "/opt/alpha/alphad", Home: "/tmp/alpha-home", Node: "tcp://127.0.0.1:26657",
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := reader.Authorization(context.Background(), testSubject, MsgSendTypeURL)
	if err != nil || !found {
		t.Fatalf("Authorization() found/error = %v/%v", found, err)
	}
	wantRecord := AuthorizationRecord{
		AuthorizationID: "auth-1", Subject: testSubject, MsgTypeURL: MsgSendTypeURL,
		PolicyID: "policy-bank-send", PolicyVersion: 7, IssuerSetID: 8,
		ValidFromHeight: 10, ValidUntilHeight: 100, Revoked: true,
		BankSendConstraints: BankSendConstraints{Denom: "uatom", Receiver: testReceiver, MaxAmount: "5000"},
	}
	if !reflect.DeepEqual(record, wantRecord) {
		t.Fatalf("record = %+v, want %+v", record, wantRecord)
	}
	issuerSetID, found, err := reader.CurrentIssuerSet(context.Background(), "policy-bank-send", MsgSendTypeURL)
	if err != nil || !found || issuerSetID != 9 {
		t.Fatalf("CurrentIssuerSet() = %d/%v/%v", issuerSetID, found, err)
	}
	batchID, found, err := reader.LastAppliedBatchID(context.Background(), 9)
	if err != nil || !found || batchID != 41 {
		t.Fatalf("LastAppliedBatchID() = %d/%v/%v", batchID, found, err)
	}
	wantArgs := [][]string{
		{"query", "authzattrs", "authorization", testSubject, MsgSendTypeURL, "--output", "json", "--home", "/tmp/alpha-home", "--node", "tcp://127.0.0.1:26657"},
		{"query", "authzattrs", "current-issuer-set", "policy-bank-send", MsgSendTypeURL, "--output", "json", "--home", "/tmp/alpha-home", "--node", "tcp://127.0.0.1:26657"},
		{"query", "authzattrs", "last-applied-batch-id", "9", "--output", "json", "--home", "/tmp/alpha-home", "--node", "tcp://127.0.0.1:26657"},
	}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("query args = %#v", runner.args)
	}
	for _, binary := range runner.binaries {
		if binary != "/opt/alpha/alphad" {
			t.Fatalf("binary = %q", binary)
		}
	}
}

func TestAlphadAuthorizationStateReaderMissingAndFailures(t *testing.T) {
	tests := []struct {
		name   string
		result stateQueryResult
		call   func(*AlphadAuthorizationStateReader) error
		want   string
	}{
		{name: "authorization missing", result: stateQueryResult{stdout: []byte(`{"found":false}`)}, call: func(r *AlphadAuthorizationStateReader) error {
			_, found, err := r.Authorization(context.Background(), testSubject, MsgSendTypeURL)
			if err == nil && found {
				return errors.New("reported found")
			}
			return err
		}},
		{name: "current issuer set missing", result: stateQueryResult{stdout: []byte(`{"found":false}`)}, call: func(r *AlphadAuthorizationStateReader) error {
			_, found, err := r.CurrentIssuerSet(context.Background(), "policy", MsgSendTypeURL)
			if err == nil && found {
				return errors.New("reported found")
			}
			return err
		}},
		{name: "last batch missing", result: stateQueryResult{stdout: []byte(`{"found":false}`)}, call: func(r *AlphadAuthorizationStateReader) error {
			_, found, err := r.LastAppliedBatchID(context.Background(), 9)
			if err == nil && found {
				return errors.New("reported found")
			}
			return err
		}},
		{name: "malformed JSON", result: stateQueryResult{stdout: []byte(`{`)}, call: func(r *AlphadAuthorizationStateReader) error {
			_, _, err := r.Authorization(context.Background(), testSubject, MsgSendTypeURL)
			return err
		}, want: "decode authorization"},
		{name: "malformed record", result: stateQueryResult{stdout: []byte(`{"authorization":{"authorization_id":"bad"},"found":true}`)}, call: func(r *AlphadAuthorizationStateReader) error {
			_, _, err := r.Authorization(context.Background(), testSubject, MsgSendTypeURL)
			return err
		}, want: "invalid authorization record"},
		{name: "command failure", result: stateQueryResult{stderr: []byte("rpc unavailable"), err: errors.New("exit 1")}, call: func(r *AlphadAuthorizationStateReader) error {
			_, _, err := r.Authorization(context.Background(), testSubject, MsgSendTypeURL)
			return err
		}, want: "rpc unavailable"},
		{name: "zero current issuer set", result: stateQueryResult{stdout: []byte(`{"issuer_set_id":"0","found":true}`)}, call: func(r *AlphadAuthorizationStateReader) error {
			_, _, err := r.CurrentIssuerSet(context.Background(), "policy", MsgSendTypeURL)
			return err
		}, want: "invalid current issuer set"},
		{name: "zero last batch", result: stateQueryResult{stdout: []byte(`{"batch_id":0,"found":true}`)}, call: func(r *AlphadAuthorizationStateReader) error {
			_, _, err := r.LastAppliedBatchID(context.Background(), 9)
			return err
		}, want: "invalid last applied batch"},
		{name: "noncanonical numeric string", result: stateQueryResult{stdout: []byte(`{"issuer_set_id":"09","found":true}`)}, call: func(r *AlphadAuthorizationStateReader) error {
			_, _, err := r.CurrentIssuerSet(context.Background(), "policy", MsgSendTypeURL)
			return err
		}, want: "decode current issuer set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &stateQueryRunner{results: []stateQueryResult{tt.result}}
			reader, err := NewAlphadAuthorizationStateReader(AlphadAuthorizationStateReaderConfig{BinaryPath: "alphad"}, runner)
			if err != nil {
				t.Fatal(err)
			}
			err = tt.call(reader)
			if tt.want == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestAlphadAuthorizationStateReaderRejectsInvalidInputs(t *testing.T) {
	if _, err := NewAlphadAuthorizationStateReader(AlphadAuthorizationStateReaderConfig{}, &stateQueryRunner{}); err == nil {
		t.Fatal("empty binary accepted")
	}
	runner := &stateQueryRunner{}
	reader, err := NewAlphadAuthorizationStateReader(AlphadAuthorizationStateReaderConfig{BinaryPath: "alphad"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.Authorization(nil, testSubject, MsgSendTypeURL); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, _, err := reader.LastAppliedBatchID(context.Background(), 0); err == nil {
		t.Fatal("zero issuer set accepted")
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
}
