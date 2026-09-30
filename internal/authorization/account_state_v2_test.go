package authorization

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAlphadAccountStateProviderV2(t *testing.T) {
	account := `{"info":{"address":"` + testSubject + `","pub_key":null,"account_number":"7","sequence":"3"}}`
	status := `{"node_info":{"network":"alpha-1"},"sync_info":{"latest_block_height":"100"}}`
	runner := &stateQueryRunner{results: []stateQueryResult{{stdout: []byte(account)}, {stdout: []byte(status)}}}
	provider, err := NewAlphadAccountStateProviderV2(AlphadAuthorizationStateReaderConfig{
		BinaryPath: "/opt/alpha/alphad", Home: "/tmp/alpha-home", Node: "tcp://127.0.0.1:26657",
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.AccountState(context.Background(), testSubject)
	if err != nil || got != (AlphaAccountStateV2{ChainID: "alpha-1", AccountNumber: 7, Sequence: 3, CurrentHeight: 100}) {
		t.Fatalf("state = %+v, error = %v", got, err)
	}
	want := [][]string{
		{"query", "auth", "account-info", testSubject, "--output", "json", "--home", "/tmp/alpha-home", "--node", "tcp://127.0.0.1:26657"},
		{"status", "--output", "json", "--home", "/tmp/alpha-home", "--node", "tcp://127.0.0.1:26657"},
	}
	if !reflect.DeepEqual(runner.args, want) || runner.binaries[0] != "/opt/alpha/alphad" {
		t.Fatalf("unexpected alphad command: %v", runner.args)
	}
}

func TestAlphadAccountStateProviderV2OmittedZeroFields(t *testing.T) {
	status := `{"node_info":{"network":"alpha-1"},"sync_info":{"latest_block_height":"100"}}`
	for _, tc := range []struct {
		name, fields            string
		accountNumber, sequence uint64
	}{
		{"omitted account number", `,"sequence":"3"`, 0, 3},
		{"omitted sequence", `,"account_number":"7"`, 7, 0},
		{"both omitted", ``, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := `{"info":{"address":"` + testSubject + `"` + tc.fields + `}}`
			runner := &stateQueryRunner{results: []stateQueryResult{{stdout: []byte(account)}, {stdout: []byte(status)}}}
			provider, err := NewAlphadAccountStateProviderV2(AlphadAuthorizationStateReaderConfig{BinaryPath: "alphad"}, runner)
			if err != nil {
				t.Fatal(err)
			}
			got, err := provider.AccountState(context.Background(), testSubject)
			if err != nil || got != (AlphaAccountStateV2{ChainID: "alpha-1", AccountNumber: tc.accountNumber, Sequence: tc.sequence, CurrentHeight: 100}) {
				t.Fatalf("state = %+v, error = %v", got, err)
			}
		})
	}
}

func TestAlphadAccountStateProviderV2RejectsUntrustedResponses(t *testing.T) {
	account := `{"info":{"address":"` + testSubject + `","account_number":"7","sequence":"3"}}`
	status := `{"node_info":{"network":"alpha-1"},"sync_info":{"latest_block_height":"100"}}`
	for _, tc := range []struct {
		name, account, status string
		queryErr              error
	}{
		{"malformed account", `{`, status, nil},
		{"wrong address", strings.Replace(account, testSubject, testReceiver, 1), status, nil},
		{"invalid account number", strings.Replace(account, `"7"`, `"07"`, 1), status, nil},
		{"invalid sequence", strings.Replace(account, `"3"`, `"03"`, 1), status, nil},
		{"negative account number", strings.Replace(account, `"7"`, `"-1"`, 1), status, nil},
		{"negative sequence", strings.Replace(account, `"3"`, `"-1"`, 1), status, nil},
		{"overflow account number", strings.Replace(account, `"7"`, `"18446744073709551616"`, 1), status, nil},
		{"overflow sequence", strings.Replace(account, `"3"`, `"18446744073709551616"`, 1), status, nil},
		{"wrong account number type", strings.Replace(account, `"7"`, `true`, 1), status, nil},
		{"wrong sequence type", strings.Replace(account, `"3"`, `true`, 1), status, nil},
		{"null account number", strings.Replace(account, `"7"`, `null`, 1), status, nil},
		{"null sequence", strings.Replace(account, `"3"`, `null`, 1), status, nil},
		{"missing chain", account, strings.Replace(status, `"alpha-1"`, `""`, 1), nil},
		{"invalid height", account, strings.Replace(status, `"100"`, `"0"`, 1), nil},
		{"duplicate account field", `{"info":{"address":"` + testSubject + `","account_number":"7","account_number":"8","sequence":"3"}}`, status, nil},
		{"duplicate sequence field", `{"info":{"address":"` + testSubject + `","account_number":"7","sequence":"3","sequence":"4"}}`, status, nil},
		{"duplicate status field", account, `{"node_info":{"network":"alpha-1","network":"other"},"sync_info":{"latest_block_height":"100"}}`, nil},
		{"command failure", account, status, errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &stateQueryRunner{results: []stateQueryResult{{stdout: []byte(tc.account), err: tc.queryErr}, {stdout: []byte(tc.status)}}}
			provider, err := NewAlphadAccountStateProviderV2(AlphadAuthorizationStateReaderConfig{BinaryPath: "alphad"}, runner)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.AccountState(context.Background(), testSubject); err == nil {
				t.Fatal("accepted untrusted account state")
			}
		})
	}
	runner := &stateQueryRunner{}
	provider, err := NewAlphadAccountStateProviderV2(AlphadAuthorizationStateReaderConfig{BinaryPath: "alphad"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.AccountState(ctx, testSubject); !errors.Is(err, context.Canceled) || runner.calls != 0 {
		t.Fatalf("cancellation not respected: %v", err)
	}
	if _, err := provider.AccountState(context.Background(), "invalid"); err == nil || runner.calls != 0 {
		t.Fatal("invalid subject reached alphad")
	}
}
