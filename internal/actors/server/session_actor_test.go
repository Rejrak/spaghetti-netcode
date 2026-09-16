package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"spaghetti/internal/remote/policy"
)

type evaluatorFunc func(context.Context, policy.PolicyInput) (policy.PolicyDecision, error)

func (f evaluatorFunc) Evaluate(ctx context.Context, input policy.PolicyInput) (policy.PolicyDecision, error) {
	return f(ctx, input)
}

func TestEvaluatePolicyFailsClosedOnEvaluatorError(t *testing.T) {
	evaluator := evaluatorFunc(func(context.Context, policy.PolicyInput) (policy.PolicyDecision, error) {
		return policy.PolicyDecision{Allow: true, PolicyID: "test", PolicyVersion: "1"}, errors.New("backend unavailable")
	})
	decision := evaluatePolicy(context.Background(), evaluator, policy.PolicyInput{}, nil)
	if decision.Allow {
		t.Fatal("evaluator error must deny")
	}
	if decision.ReasonCode != policy.ReasonEvaluationError {
		t.Fatalf("unexpected reason code %q", decision.ReasonCode)
	}
	if decisionResponse(decision).ResponseMessage.Success {
		t.Fatal("transport response must preserve fail-closed decision")
	}
}

func TestEvaluatePolicyPreservesDeny(t *testing.T) {
	evaluator := evaluatorFunc(func(context.Context, policy.PolicyInput) (policy.PolicyDecision, error) {
		return policy.PolicyDecision{ReasonCode: policy.ReasonPolicyMismatch, Reason: "denied"}, nil
	})
	decision := evaluatePolicy(context.Background(), evaluator, policy.PolicyInput{}, nil)
	if decision.Allow || decisionResponse(decision).ResponseMessage.Success {
		t.Fatal("deny decision became an implicit allow")
	}
}

func TestEvaluatePolicyRejectsMalformedAllow(t *testing.T) {
	evaluator := evaluatorFunc(func(context.Context, policy.PolicyInput) (policy.PolicyDecision, error) {
		return policy.PolicyDecision{Allow: true}, nil
	})
	decision := evaluatePolicy(context.Background(), evaluator, policy.PolicyInput{}, nil)
	if decision.Allow || decision.ReasonCode != policy.ReasonEvaluationError {
		t.Fatalf("malformed allow must fail closed, got %+v", decision)
	}
}

func TestStaleAttributesFailClosed(t *testing.T) {
	now := time.Now()
	if attributesAreFresh(now.Add(-2*time.Minute).Unix(), time.Minute, now) {
		t.Fatal("stale attributes accepted")
	}
	decision := evaluatePolicy(context.Background(), nil, policy.PolicyInput{}, errAttributesStale)
	if decision.Allow || decision.ReasonCode != policy.ReasonInputStale {
		t.Fatalf("stale attributes must fail closed, got %+v", decision)
	}
}

func TestPolicyEvaluatedLogIsStructured(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	s := session{logger: logger}
	s.logPolicyDecision(context.Background(), "cosmos1private", "/cosmos.bank.v1beta1.MsgSend", policy.PolicyDecision{
		Allow:         false,
		ReasonCode:    policy.ReasonPolicyMismatch,
		PolicyID:      "bank-send-policy",
		PolicyVersion: "1",
	}, time.Now())

	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"msg":            "policy_evaluated",
		"component":      "policy",
		"outcome":        "deny",
		"reason_code":    policy.ReasonPolicyMismatch,
		"policy_id":      "bank-send-policy",
		"policy_version": "1",
	} {
		if event[key] != want {
			t.Fatalf("%s: got %v want %s", key, event[key], want)
		}
	}
	if _, ok := event["duration_ms"]; !ok {
		t.Fatal("duration_ms missing")
	}
	if strings.Contains(output.String(), "cosmos1private") {
		t.Fatal("raw subject leaked into policy log")
	}
}
