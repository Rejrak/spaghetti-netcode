package policy

import (
	"context"

	"spaghetti/internal/user"
)

const (
	ReasonOK              = "AUTHZ_OK"
	ReasonNotFound        = "AUTHZ_NOT_FOUND"
	ReasonPolicyMismatch  = "AUTHZ_POLICY_MISMATCH"
	ReasonInputStale      = "POLICY_INPUT_STALE"
	ReasonEvaluationError = "POLICY_EVALUATION_ERROR"
)

type PolicyInput struct {
	Subject    string
	Operation  string
	Attributes *user.Attributes
}

type PolicyDecision struct {
	Allow         bool
	ReasonCode    string
	Reason        string
	PolicyID      string
	PolicyVersion string
}

type PolicyEvaluator interface {
	Evaluate(context.Context, PolicyInput) (PolicyDecision, error)
}

type AttributeEvaluator struct {
	PolicyID           string
	PolicyVersion      string
	Operation          string
	RequiredPermission string
}

func (e AttributeEvaluator) Evaluate(ctx context.Context, in PolicyInput) (PolicyDecision, error) {
	if err := ctx.Err(); err != nil {
		return PolicyDecision{}, err
	}
	decision := PolicyDecision{
		ReasonCode:    ReasonPolicyMismatch,
		Reason:        "operation or permission is not allowed by policy",
		PolicyID:      e.PolicyID,
		PolicyVersion: e.PolicyVersion,
	}
	if in.Subject == "" || in.Attributes == nil {
		decision.ReasonCode = ReasonNotFound
		decision.Reason = "normalized attributes not found"
		return decision, nil
	}
	if in.Operation != e.Operation || !in.Attributes.Perms[e.RequiredPermission] {
		return decision, nil
	}
	decision.Allow = true
	decision.ReasonCode = ReasonOK
	decision.Reason = "policy allowed operation"
	return decision, nil
}
