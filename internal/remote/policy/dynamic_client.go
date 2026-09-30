package policy

import "context"

type DynamicPolicyClient interface {
	Evaluate(ctx context.Context, in *Context) (Decision, error)
}
