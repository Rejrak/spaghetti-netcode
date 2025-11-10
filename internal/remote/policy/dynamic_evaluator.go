package policy

import (
	"context"
	"log"
	"spaghetti/internal/utils/cache"
	"time"
)

type DynamicEvaluator struct {
	Client   DynamicPolicyClient
	Timeout  time.Duration
	FailOpen bool
	Cache    *cache.TTLCache[Decision]
}

func (e *DynamicEvaluator) Evaluate(ctx context.Context, pc *Context) (Decision, error) {
	// if e.Cache != nil {
	// 	if d, ok := e.Cache.Get(cacheKey(pc)); ok {
	// 		return d, nil
	// 	}
	// }
	tctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()

	d, err := e.Client.Evaluate(tctx, pc)
	if err != nil {
		log.Default().Printf("FAIL OPEN: %v", e.FailOpen)
		if e.FailOpen {
			return Decision{Allow: true, Message: "dynamic backend error; allowed (fail-open)"}, nil
		}
		return Decision{Allow: false, Message: "dynamic backend error; denied (fail-close)"}, err
	}
	// if e.Cache != nil && d.TTL > 0 {
	// 	e.Cache.Put(cacheKey(pc), d, d.TTL)
	// }
	return d, nil
}

func cacheKey(pc *Context) string {
	return pc.Address + "|" + pc.Operation + "|" + pc.Resources["receiver"] + "|" + pc.Resources["amount"]
}
