package authorization

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"spaghetti/internal/remote/policy"
)

const (
	testSubject  = "cosmos1fl48vsnmsdzcv85q5d2q4z5ajdha8yu34mf0eh"
	testReceiver = "cosmos1f9xjhxm0plzrh9cskf4qee4pc2xwp0n0556gh0"
)

func validInputs() (NormalizedMsgSendFacts, policy.PolicyDecision, TrustedAuthorizationContext) {
	return NormalizedMsgSendFacts{
			Subject:    testSubject,
			MsgTypeURL: MsgSendTypeURL,
			Receiver:   testReceiver,
			Denom:      "uatom",
			Amount:     "25",
		}, policy.PolicyDecision{
			Allow:         true,
			ReasonCode:    policy.ReasonOK,
			PolicyID:      "payments-v1",
			PolicyVersion: "7",
		}, TrustedAuthorizationContext{
			AuthorizationID:  "authz-001",
			IssuerSetID:      3,
			ValidFromHeight:  100,
			ValidUntilHeight: 150,
			AllowedDenom:     "uatom",
			AllowedReceiver:  testReceiver,
			MaxAmount:        "100",
		}
}

func TestBuildAuthorizationRecord(t *testing.T) {
	facts, decision, trusted := validInputs()
	record, err := BuildAuthorizationRecord(facts, decision, trusted)
	if err != nil {
		t.Fatalf("BuildAuthorizationRecord() error = %v", err)
	}
	want := AuthorizationRecord{
		AuthorizationID:  "authz-001",
		Subject:          testSubject,
		MsgTypeURL:       MsgSendTypeURL,
		PolicyID:         "payments-v1",
		PolicyVersion:    7,
		IssuerSetID:      3,
		ValidFromHeight:  100,
		ValidUntilHeight: 150,
		BankSendConstraints: BankSendConstraints{
			Denom:     "uatom",
			Receiver:  testReceiver,
			MaxAmount: "100",
		},
	}
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("record mismatch\n got: %#v\nwant: %#v", record, want)
	}
}

func TestBuildAuthorizationRecordFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NormalizedMsgSendFacts, *policy.PolicyDecision, *TrustedAuthorizationContext)
	}{
		{"deny", func(_ *NormalizedMsgSendFacts, d *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			d.Allow = false
		}},
		{"incomplete decision", func(_ *NormalizedMsgSendFacts, d *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			d.ReasonCode = ""
		}},
		{"missing policy id", func(_ *NormalizedMsgSendFacts, d *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			d.PolicyID = ""
		}},
		{"invalid policy version", func(_ *NormalizedMsgSendFacts, d *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			d.PolicyVersion = "0"
		}},
		{"missing authorization id", func(_ *NormalizedMsgSendFacts, _ *policy.PolicyDecision, c *TrustedAuthorizationContext) {
			c.AuthorizationID = ""
		}},
		{"invalid issuer set", func(_ *NormalizedMsgSendFacts, _ *policy.PolicyDecision, c *TrustedAuthorizationContext) {
			c.IssuerSetID = 0
		}},
		{"unsupported operation", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.MsgTypeURL = "/cosmos.staking.v1beta1.MsgDelegate"
		}},
		{"invalid subject", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.Subject = "not-bech32"
		}},
		{"invalid receiver", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.Receiver = "not-bech32"
		}},
		{"invalid denom", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.Denom = "u"
		}},
		{"invalid amount", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.Amount = "01"
		}},
		{"invalid max amount", func(_ *NormalizedMsgSendFacts, _ *policy.PolicyDecision, c *TrustedAuthorizationContext) {
			c.MaxAmount = "0"
		}},
		{"amount exceeds Cosmos integer range", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.Amount = "115792089237316195423570985008687907853269984665640564039457584007913129639936"
		}},
		{"zero start height", func(_ *NormalizedMsgSendFacts, _ *policy.PolicyDecision, c *TrustedAuthorizationContext) {
			c.ValidFromHeight = 0
		}},
		{"reversed heights", func(_ *NormalizedMsgSendFacts, _ *policy.PolicyDecision, c *TrustedAuthorizationContext) {
			c.ValidUntilHeight = 99
		}},
		{"receiver outside constraints", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.Receiver = testSubject
		}},
		{"denom outside constraints", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.Denom = "ustake"
		}},
		{"amount exceeds max", func(f *NormalizedMsgSendFacts, _ *policy.PolicyDecision, _ *TrustedAuthorizationContext) {
			f.Amount = "101"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts, decision, trusted := validInputs()
			tt.mutate(&facts, &decision, &trusted)
			record, err := BuildAuthorizationRecord(facts, decision, trusted)
			if err == nil {
				t.Fatalf("expected error, got record %#v", record)
			}
			if record != (AuthorizationRecord{}) {
				t.Fatalf("error returned a partial record: %#v", record)
			}
		})
	}
}

func TestRequesterCannotSupplyTrustedAuthorizationFields(t *testing.T) {
	requestFields := reflect.TypeOf(NormalizedMsgSendFacts{})
	for _, forbidden := range []string{
		"AuthorizationID", "PolicyID", "PolicyVersion", "IssuerSetID",
		"ValidFromHeight", "ValidUntilHeight", "AllowedDenom",
		"AllowedReceiver", "MaxAmount",
	} {
		if _, found := requestFields.FieldByName(forbidden); found {
			t.Fatalf("request facts expose trusted field %s", forbidden)
		}
	}

	facts, decision, trusted := validInputs()
	first, err := BuildAuthorizationRecord(facts, decision, trusted)
	if err != nil {
		t.Fatal(err)
	}
	facts.Amount = "50"
	second, err := BuildAuthorizationRecord(facts, decision, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if first.PolicyID != second.PolicyID || first.PolicyVersion != second.PolicyVersion || first.IssuerSetID != second.IssuerSetID || first.BankSendConstraints != second.BankSendConstraints {
		t.Fatal("request facts changed trusted authorization fields")
	}
}

func TestLogAuthorizationBuilt(t *testing.T) {
	facts, decision, trusted := validInputs()
	record, err := BuildAuthorizationRecord(facts, decision, trusted)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	LogAuthorizationBuilt(context.Background(), logger, record)
	logLine := output.String()
	for _, value := range []string{
		`"msg":"authorization_built"`, `"outcome":"success"`,
		`"authorization_id":"authz-001"`, `"policy_id":"payments-v1"`,
		`"policy_version":7`, `"valid_from_height":100`, `"valid_until_height":150`,
	} {
		if !strings.Contains(logLine, value) {
			t.Errorf("log missing %s: %s", value, logLine)
		}
	}
	if strings.Contains(logLine, testSubject) {
		t.Fatalf("log exposes raw subject: %s", logLine)
	}
}
