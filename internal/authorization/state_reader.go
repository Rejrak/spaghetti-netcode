package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type AuthorizationStateReader interface {
	Authorization(context.Context, string, string) (AuthorizationRecord, bool, error)
	CurrentIssuerSet(context.Context, string, string) (uint64, bool, error)
	LastAppliedBatchID(context.Context, uint64) (uint64, bool, error)
}

type AlphadAuthorizationStateReaderConfig struct {
	BinaryPath string
	Home       string
	Node       string
}

type AlphadAuthorizationStateReader struct {
	config AlphadAuthorizationStateReaderConfig
	runner CommandRunner
}

func NewAlphadAuthorizationStateReader(config AlphadAuthorizationStateReaderConfig, runner CommandRunner) (*AlphadAuthorizationStateReader, error) {
	if strings.TrimSpace(config.BinaryPath) == "" {
		return nil, fmt.Errorf("empty alphad binary path")
	}
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &AlphadAuthorizationStateReader{config: config, runner: runner}, nil
}

func (r *AlphadAuthorizationStateReader) Authorization(ctx context.Context, subject, msgTypeURL string) (AuthorizationRecord, bool, error) {
	if ctx == nil {
		return AuthorizationRecord{}, false, fmt.Errorf("nil authorization query context")
	}
	if err := validateAccountAddress(subject); err != nil {
		return AuthorizationRecord{}, false, fmt.Errorf("invalid authorization subject: %w", err)
	}
	if msgTypeURL != MsgSendTypeURL {
		return AuthorizationRecord{}, false, fmt.Errorf("unsupported authorization message type")
	}
	stdout, err := r.query(ctx, "authorization", subject, msgTypeURL)
	if err != nil {
		return AuthorizationRecord{}, false, err
	}
	var response struct {
		Authorization *wireAuthorizationRecord `json:"authorization"`
		Found         *bool                    `json:"found"`
	}
	if err := decodeQueryResponse(stdout, &response); err != nil {
		return AuthorizationRecord{}, false, fmt.Errorf("decode authorization query: %w", err)
	}
	if response.Found != nil && !*response.Found {
		return AuthorizationRecord{}, false, nil
	}
	if response.Authorization == nil {
		if response.Found != nil && *response.Found {
			return AuthorizationRecord{}, false, fmt.Errorf("authorization query reports found without a record")
		}
		return AuthorizationRecord{}, false, nil
	}
	record := response.Authorization.logical()
	if err := validateCanonicalRecord(record); err != nil {
		return AuthorizationRecord{}, false, fmt.Errorf("invalid authorization record: %w", err)
	}
	if record.Subject != subject || record.MsgTypeURL != msgTypeURL {
		return AuthorizationRecord{}, false, fmt.Errorf("authorization query key mismatch")
	}
	return record, true, nil
}

func (r *AlphadAuthorizationStateReader) CurrentIssuerSet(ctx context.Context, policyID, msgTypeURL string) (uint64, bool, error) {
	if ctx == nil {
		return 0, false, fmt.Errorf("nil current issuer set query context")
	}
	if strings.TrimSpace(policyID) == "" {
		return 0, false, fmt.Errorf("empty policy id")
	}
	if msgTypeURL != MsgSendTypeURL {
		return 0, false, fmt.Errorf("unsupported authorization message type")
	}
	stdout, err := r.query(ctx, "current-issuer-set", policyID, msgTypeURL)
	if err != nil {
		return 0, false, err
	}
	var response struct {
		IssuerSetID flexibleUint64 `json:"issuer_set_id"`
		Found       *bool          `json:"found"`
	}
	if err := decodeQueryResponse(stdout, &response); err != nil {
		return 0, false, fmt.Errorf("decode current issuer set query: %w", err)
	}
	if response.Found != nil && !*response.Found {
		return 0, false, nil
	}
	issuerSetID := uint64(response.IssuerSetID)
	if issuerSetID == 0 {
		if response.Found == nil {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("invalid current issuer set id")
	}
	return issuerSetID, true, nil
}

func (r *AlphadAuthorizationStateReader) LastAppliedBatchID(ctx context.Context, issuerSetID uint64) (uint64, bool, error) {
	if ctx == nil {
		return 0, false, fmt.Errorf("nil last applied batch query context")
	}
	if issuerSetID == 0 {
		return 0, false, fmt.Errorf("invalid issuer set id")
	}
	stdout, err := r.query(ctx, "last-applied-batch-id", strconv.FormatUint(issuerSetID, 10))
	if err != nil {
		return 0, false, err
	}
	var response struct {
		BatchID flexibleUint64 `json:"batch_id"`
		Found   *bool          `json:"found"`
	}
	if err := decodeQueryResponse(stdout, &response); err != nil {
		return 0, false, fmt.Errorf("decode last applied batch query: %w", err)
	}
	if response.Found != nil && !*response.Found {
		return 0, false, nil
	}
	batchID := uint64(response.BatchID)
	if batchID == 0 {
		if response.Found == nil {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("invalid last applied batch id")
	}
	return batchID, true, nil
}

func (r *AlphadAuthorizationStateReader) query(ctx context.Context, command string, positional ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := append([]string{"query", "authzattrs", command}, positional...)
	args = append(args, "--output", "json")
	if r.config.Home != "" {
		args = append(args, "--home", r.config.Home)
	}
	if r.config.Node != "" {
		args = append(args, "--node", r.config.Node)
	}
	stdout, stderr, err := r.runner.Run(ctx, r.config.BinaryPath, args...)
	if err != nil {
		if detail := strings.TrimSpace(string(stderr)); detail != "" {
			return nil, fmt.Errorf("alphad %s query failed: %w: %s", command, err, detail)
		}
		return nil, fmt.Errorf("alphad %s query failed: %w", command, err)
	}
	return stdout, nil
}

func decodeQueryResponse(data []byte, target any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("alphad returned empty stdout")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

type flexibleUint64 uint64

func (value *flexibleUint64) UnmarshalJSON(data []byte) error {
	text := string(data)
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		text = text[1 : len(text)-1]
		if text == "" || (len(text) > 1 && text[0] == '0') {
			return fmt.Errorf("non-canonical uint64")
		}
	}
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return err
	}
	*value = flexibleUint64(parsed)
	return nil
}

type wireAuthorizationRecord struct {
	AuthorizationID     string                  `json:"authorization_id"`
	Subject             string                  `json:"subject"`
	MsgTypeURL          string                  `json:"msg_type_url"`
	PolicyID            string                  `json:"policy_id"`
	PolicyVersion       flexibleUint64          `json:"policy_version"`
	IssuerSetID         flexibleUint64          `json:"issuer_set_id"`
	ValidFromHeight     canonicalFlexibleInt64  `json:"valid_from_height"`
	ValidUntilHeight    canonicalFlexibleInt64  `json:"valid_until_height"`
	Revoked             bool                    `json:"revoked"`
	BankSendConstraints wireBankSendConstraints `json:"bank_send_constraints"`
}

type canonicalFlexibleInt64 int64

func (value *canonicalFlexibleInt64) UnmarshalJSON(data []byte) error {
	text := string(data)
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		text = text[1 : len(text)-1]
		unsigned := text
		if strings.HasPrefix(unsigned, "-") {
			unsigned = unsigned[1:]
		}
		if unsigned == "" || (len(unsigned) > 1 && unsigned[0] == '0') || text == "-0" {
			return fmt.Errorf("non-canonical int64")
		}
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*value = canonicalFlexibleInt64(parsed)
	return nil
}

type wireBankSendConstraints struct {
	Denom     string `json:"denom"`
	Receiver  string `json:"receiver"`
	MaxAmount string `json:"max_amount"`
}

func (record wireAuthorizationRecord) logical() AuthorizationRecord {
	return AuthorizationRecord{
		AuthorizationID:  record.AuthorizationID,
		Subject:          record.Subject,
		MsgTypeURL:       record.MsgTypeURL,
		PolicyID:         record.PolicyID,
		PolicyVersion:    uint64(record.PolicyVersion),
		IssuerSetID:      uint64(record.IssuerSetID),
		ValidFromHeight:  int64(record.ValidFromHeight),
		ValidUntilHeight: int64(record.ValidUntilHeight),
		Revoked:          record.Revoked,
		BankSendConstraints: BankSendConstraints{
			Denom:     record.BankSendConstraints.Denom,
			Receiver:  record.BankSendConstraints.Receiver,
			MaxAmount: record.BankSendConstraints.MaxAmount,
		},
	}
}
