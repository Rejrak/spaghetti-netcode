package authorization

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/cosmos/btcutil/bech32"

	"spaghetti/internal/remote/policy"
)

const MsgSendTypeURL = "/cosmos.bank.v1beta1.MsgSend"

var (
	denomPattern  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9/:._-]{2,127}$`)
	amountPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// NormalizedMsgSendFacts contains only facts supplied by a direct MsgSend.
// Trusted policy metadata and authorization constraints deliberately do not
// belong to this type.
type NormalizedMsgSendFacts struct {
	Subject    string
	MsgTypeURL string
	Receiver   string
	Denom      string
	Amount     string
}

// TrustedAuthorizationContext contains values chosen by the trusted
// authorization layer, never by the requester.
type TrustedAuthorizationContext struct {
	AuthorizationID  string
	IssuerSetID      uint64
	ValidFromHeight  int64
	ValidUntilHeight int64
	AllowedDenom     string
	AllowedReceiver  string
	MaxAmount        string
}

type BankSendConstraints struct {
	Denom     string `json:"denom"`
	Receiver  string `json:"receiver"`
	MaxAmount string `json:"max_amount"`
}

// AuthorizationRecord is the Protocol V1.1 logical record. Its chain lookup
// key is (Subject, MsgTypeURL); AuthorizationID is audit metadata only.
type AuthorizationRecord struct {
	AuthorizationID     string              `json:"authorization_id"`
	Subject             string              `json:"subject"`
	MsgTypeURL          string              `json:"msg_type_url"`
	PolicyID            string              `json:"policy_id"`
	PolicyVersion       uint64              `json:"policy_version"`
	IssuerSetID         uint64              `json:"issuer_set_id"`
	ValidFromHeight     int64               `json:"valid_from_height"`
	ValidUntilHeight    int64               `json:"valid_until_height"`
	Revoked             bool                `json:"revoked"`
	BankSendConstraints BankSendConstraints `json:"bank_send_constraints"`
}

// BuildAuthorizationRecord validates all three trust-boundary inputs and
// constructs a new, non-revoked Protocol V1.1 grant without performing I/O.
func BuildAuthorizationRecord(facts NormalizedMsgSendFacts, decision policy.PolicyDecision, trusted TrustedAuthorizationContext) (AuthorizationRecord, error) {
	if !decision.Allow {
		return AuthorizationRecord{}, fmt.Errorf("policy decision denied: %s", decision.ReasonCode)
	}
	if decision.ReasonCode != policy.ReasonOK || strings.TrimSpace(decision.PolicyID) == "" {
		return AuthorizationRecord{}, fmt.Errorf("incomplete policy decision")
	}
	policyVersion, err := strconv.ParseUint(decision.PolicyVersion, 10, 64)
	if err != nil || policyVersion == 0 {
		return AuthorizationRecord{}, fmt.Errorf("invalid policy version")
	}
	if strings.TrimSpace(trusted.AuthorizationID) == "" {
		return AuthorizationRecord{}, fmt.Errorf("missing authorization id")
	}
	if trusted.IssuerSetID == 0 {
		return AuthorizationRecord{}, fmt.Errorf("invalid issuer set id")
	}
	if trusted.ValidFromHeight <= 0 || trusted.ValidUntilHeight < trusted.ValidFromHeight {
		return AuthorizationRecord{}, fmt.Errorf("invalid authorization height window")
	}
	if facts.MsgTypeURL != MsgSendTypeURL {
		return AuthorizationRecord{}, fmt.Errorf("unsupported message type %q", facts.MsgTypeURL)
	}
	if err := validateAccountAddress(facts.Subject); err != nil {
		return AuthorizationRecord{}, fmt.Errorf("invalid subject: %w", err)
	}
	if err := validateAccountAddress(facts.Receiver); err != nil {
		return AuthorizationRecord{}, fmt.Errorf("invalid receiver: %w", err)
	}
	if err := validateAccountAddress(trusted.AllowedReceiver); err != nil {
		return AuthorizationRecord{}, fmt.Errorf("invalid allowed receiver: %w", err)
	}
	if !denomPattern.MatchString(facts.Denom) {
		return AuthorizationRecord{}, fmt.Errorf("invalid denom")
	}
	if !denomPattern.MatchString(trusted.AllowedDenom) {
		return AuthorizationRecord{}, fmt.Errorf("invalid allowed denom")
	}
	amount, ok := canonicalAmount(facts.Amount)
	if !ok {
		return AuthorizationRecord{}, fmt.Errorf("invalid amount")
	}
	maxAmount, ok := canonicalAmount(trusted.MaxAmount)
	if !ok {
		return AuthorizationRecord{}, fmt.Errorf("invalid max amount")
	}
	if facts.Receiver != trusted.AllowedReceiver || facts.Denom != trusted.AllowedDenom || amount.Cmp(maxAmount) > 0 {
		return AuthorizationRecord{}, fmt.Errorf("MsgSend facts are not representable by trusted constraints")
	}

	return AuthorizationRecord{
		AuthorizationID:  trusted.AuthorizationID,
		Subject:          facts.Subject,
		MsgTypeURL:       facts.MsgTypeURL,
		PolicyID:         decision.PolicyID,
		PolicyVersion:    policyVersion,
		IssuerSetID:      trusted.IssuerSetID,
		ValidFromHeight:  trusted.ValidFromHeight,
		ValidUntilHeight: trusted.ValidUntilHeight,
		Revoked:          false,
		BankSendConstraints: BankSendConstraints{
			Denom:     trusted.AllowedDenom,
			Receiver:  trusted.AllowedReceiver,
			MaxAmount: trusted.MaxAmount,
		},
	}, nil
}

func canonicalAmount(value string) (*big.Int, bool) {
	if !amountPattern.MatchString(value) {
		return nil, false
	}
	n, ok := new(big.Int).SetString(value, 10)
	return n, ok && n.BitLen() <= 256
}

func validateAccountAddress(address string) error {
	if address != strings.ToLower(address) {
		return fmt.Errorf("address is not canonical lowercase bech32")
	}
	hrp, data, err := bech32.Decode(address, 1023)
	if err != nil || hrp != "cosmos" {
		return fmt.Errorf("invalid cosmos bech32 address")
	}
	payload, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil || len(payload) == 0 || len(payload) > 255 {
		return fmt.Errorf("invalid account address payload")
	}
	canonical, err := bech32.Encode(hrp, data)
	if err != nil || canonical != address {
		return fmt.Errorf("address is not canonical bech32")
	}
	return nil
}
