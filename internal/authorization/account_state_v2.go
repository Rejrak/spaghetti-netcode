package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// AlphadAccountStateProviderV2 reads account and chain facts from Alpha. It
// never accepts those facts from the certificate requester.
type AlphadAccountStateProviderV2 struct {
	config AlphadAuthorizationStateReaderConfig
	runner CommandRunner
}

func NewAlphadAccountStateProviderV2(config AlphadAuthorizationStateReaderConfig, runner CommandRunner) (*AlphadAccountStateProviderV2, error) {
	if strings.TrimSpace(config.BinaryPath) == "" {
		return nil, fmt.Errorf("empty alphad binary path")
	}
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &AlphadAccountStateProviderV2{config: config, runner: runner}, nil
}

func (p *AlphadAccountStateProviderV2) AccountState(ctx context.Context, subject string) (AlphaAccountStateV2, error) {
	if ctx == nil {
		return AlphaAccountStateV2{}, fmt.Errorf("nil account query context")
	}
	if err := ctx.Err(); err != nil {
		return AlphaAccountStateV2{}, err
	}
	if err := validateAccountAddress(subject); err != nil {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid subject: %w", err)
	}
	accountJSON, err := p.run(ctx, "query", "auth", "account-info", subject)
	if err != nil {
		return AlphaAccountStateV2{}, err
	}
	accountResponse, err := strictJSONObject(accountJSON)
	if err != nil {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid account response: %w", err)
	}
	account, err := strictJSONObject(accountResponse["info"])
	if err != nil {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid account info: %w", err)
	}
	var address string
	if err := json.Unmarshal(account["address"], &address); err != nil || address != subject {
		return AlphaAccountStateV2{}, fmt.Errorf("account address mismatch")
	}
	accountNumber, err := parseCanonicalUintJSON(account["account_number"])
	if err != nil {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid account number: %w", err)
	}
	sequence, err := parseCanonicalUintJSON(account["sequence"])
	if err != nil {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid account sequence: %w", err)
	}
	statusJSON, err := p.run(ctx, "status")
	if err != nil {
		return AlphaAccountStateV2{}, err
	}
	status, err := strictJSONObject(statusJSON)
	if err != nil {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid status: %w", err)
	}
	node, err := strictJSONObject(status["node_info"])
	if err != nil {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid node info: %w", err)
	}
	var chainID string
	if err := json.Unmarshal(node["network"], &chainID); err != nil || strings.TrimSpace(chainID) == "" {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid chain ID")
	}
	sync, err := strictJSONObject(status["sync_info"])
	if err != nil {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid sync info: %w", err)
	}
	var height canonicalFlexibleInt64
	if err := json.Unmarshal(sync["latest_block_height"], &height); err != nil || height <= 0 {
		return AlphaAccountStateV2{}, fmt.Errorf("invalid current height")
	}
	return AlphaAccountStateV2{ChainID: chainID, AccountNumber: accountNumber, Sequence: sequence, CurrentHeight: int64(height)}, nil
}

func (p *AlphadAccountStateProviderV2) run(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args = append(args, "--output", "json")
	if p.config.Home != "" {
		args = append(args, "--home", p.config.Home)
	}
	if p.config.Node != "" {
		args = append(args, "--node", p.config.Node)
	}
	stdout, _, err := p.runner.Run(ctx, p.config.BinaryPath, args...)
	if err != nil {
		return nil, fmt.Errorf("alphad state query failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return stdout, nil
}

func strictJSONObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, fmt.Errorf("expected JSON object")
	}
	out := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key := keyToken.(string)
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("duplicate JSON key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON value")
	}
	return out, nil
}

func parseCanonicalUintJSON(data []byte) (uint64, error) {
	if len(data) == 0 {
		return 0, fmt.Errorf("missing number")
	}
	var value flexibleUint64
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, err
	}
	return uint64(value), nil
}
