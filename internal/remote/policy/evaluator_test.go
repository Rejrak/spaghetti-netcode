package policy

import (
	"context"
	"testing"

	"spaghetti/internal/user"
)

func TestAttributeEvaluatorDecisions(t *testing.T) {
	evaluator := AttributeEvaluator{
		PolicyID:           "bank-send-policy",
		PolicyVersion:      "1",
		Operation:          "/cosmos.bank.v1beta1.MsgSend",
		RequiredPermission: "supply.transaction.send",
	}

	tests := []struct {
		name       string
		input      PolicyInput
		allow      bool
		reasonCode string
	}{
		{
			name: "allow",
			input: PolicyInput{
				Subject:   "cosmos1allowed",
				Operation: "/cosmos.bank.v1beta1.MsgSend",
				Attributes: &user.Attributes{Perms: map[string]bool{
					"supply.transaction.send": true,
				}},
			},
			allow:      true,
			reasonCode: ReasonOK,
		},
		{
			name: "deny missing permission",
			input: PolicyInput{
				Subject:    "cosmos1denied",
				Operation:  "/cosmos.bank.v1beta1.MsgSend",
				Attributes: &user.Attributes{Perms: map[string]bool{}},
			},
			reasonCode: ReasonPolicyMismatch,
		},
		{
			name: "deny missing attributes",
			input: PolicyInput{
				Subject:   "cosmos1unknown",
				Operation: "/cosmos.bank.v1beta1.MsgSend",
			},
			reasonCode: ReasonNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := evaluator.Evaluate(context.Background(), tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allow != tc.allow || decision.ReasonCode != tc.reasonCode {
				t.Fatalf("got allow=%v reason=%s", decision.Allow, decision.ReasonCode)
			}
			if decision.PolicyID != evaluator.PolicyID || decision.PolicyVersion != evaluator.PolicyVersion {
				t.Fatal("policy identity missing from decision")
			}
		})
	}
}
