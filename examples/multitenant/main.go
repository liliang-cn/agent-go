// One service, many customers.
//
// A Service is already safe to run many tasks through at once. What this
// example shows is the part that was missing: telling it whose run a run is,
// so the process can be shared without one caller being able to take it.
//
//	go run ./examples/multitenant
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
)

func main() {
	svc, err := agent.New("support").
		WithSystemPrompt("You answer briefly.").
		// Ceilings for a shared process. Both are off by default; the
		// per-tenant one is what stops a single customer filling the other.
		WithMaxConcurrentRuns(8).
		WithMaxRunsPerTenant(2).
		Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	ctx := context.Background()
	var wg sync.WaitGroup
	for _, tenant := range []string{"acme", "acme", "acme", "globex"} {
		wg.Add(1)
		go func(tenant string) {
			defer wg.Done()
			// The tenant is an opaque label. Nothing in the loop reads it:
			// it exists for admission control, bulk cancellation and
			// attributing what the run cost.
			res, err := svc.Run(ctx, "Say hello.", agent.WithTenant(tenant))
			switch {
			case errors.Is(err, agent.ErrTenantAtCapacity):
				// The third acme run. Shed it, queue it, or answer 503 —
				// the framework refuses immediately rather than blocking,
				// so the decision stays yours.
				fmt.Printf("%-7s refused: %v\n", tenant, err)
			case err != nil:
				fmt.Printf("%-7s failed: %v\n", tenant, err)
			default:
				fmt.Printf("%-7s ok, cost $%.4f\n", res.Tenant, res.EstimatedCostUSD)
			}
		}(tenant)
	}

	wg.Wait()

	// What the process is carrying, for a host deciding whether to accept
	// more work — and for an operator asking who is using it.
	c := svc.Capacity()
	fmt.Printf("\nin flight: %d of %d, tenants %v\n", c.ActiveRuns, c.MaxConcurrentRuns, c.Tenants)

	// The operator's verb: stop everything one customer is doing.
	fmt.Printf("stopped %d acme runs\n", svc.CancelTenant("acme"))
}
