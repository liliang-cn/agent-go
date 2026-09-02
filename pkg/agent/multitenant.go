package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Running one Service for many people.
//
// A Service is already safe to run many tasks through at once: every run
// derives its own context, its own session and its own event stream, and the
// registry in run_cancel.go can name and stop each one. What it had no notion
// of was *whose* run a run is. For a desktop app with one user that is a
// distinction without a difference. For a server it is the difference between
// a product and an incident: one caller's runaway loop consumes the whole
// process, and an operator asked to stop "that customer's work" has nothing
// to aim at.
//
// Tenant is the answer, and it is deliberately the smallest one that works.
//
//   - It is an opaque label the caller chooses. This package never parses it,
//     never derives behaviour from its contents, and has no notion of what a
//     tenant *is* — an organisation, a customer, a seat, a Slack workspace.
//   - It is NOT an identity. Identity in this framework is the session UUID
//     and stays that way: a conversation is a session, memory is scoped by
//     session, history is filtered by task. Tenant sits alongside those as an
//     ownership label used for exactly three things — admission control,
//     bulk cancellation, and attributing what a run cost.
//
// The rule that keeps it honest: nothing in the loop may read the tenant. If a
// tenant string ever changes what an agent does, it has stopped being a label
// and become configuration by string matching, which is the same disease as
// the phrase tables constraints.go replaced.

// ErrAtCapacity is returned by a run entry point when the service is already
// running MaxConcurrentRuns runs.
//
// It is a distinct error rather than a queue on purpose. A library that
// silently blocks its caller for an unbounded time is a library that turns a
// capacity problem into a latency mystery; a server that gets this error can
// shed load, queue in its own scheduler, or answer 503 — decisions this
// package has no business making.
var ErrAtCapacity = errors.New("agent service is at its concurrent run limit")

// ErrTenantAtCapacity is the same thing for one tenant's share.
var ErrTenantAtCapacity = errors.New("tenant is at its concurrent run limit")

// CapacityError carries the numbers behind a refusal, so the caller can log
// or report what the limit actually was.
type CapacityError struct {
	// Tenant is empty for a service-wide refusal.
	Tenant string
	// Active is how many runs were in flight, and Limit is the ceiling.
	Active int
	Limit  int
	err    error
}

func (e *CapacityError) Error() string {
	if e.Tenant != "" {
		return fmt.Sprintf("tenant %q is at its concurrent run limit: %d of %d", e.Tenant, e.Active, e.Limit)
	}
	return fmt.Sprintf("agent service is at its concurrent run limit: %d of %d", e.Active, e.Limit)
}

// Unwrap makes errors.Is(err, ErrAtCapacity) work on the detailed error.
func (e *CapacityError) Unwrap() error { return e.err }

// Capacity is a snapshot of what a service is carrying right now. It is what
// a host reads to decide whether to accept more work, and what an operator
// reads to find out who is using the process.
type Capacity struct {
	// ActiveRuns is every run in flight, all tenants.
	ActiveRuns int `json:"active_runs"`
	// MaxConcurrentRuns is the service-wide ceiling; 0 means unlimited.
	MaxConcurrentRuns int `json:"max_concurrent_runs"`
	// MaxRunsPerTenant is each tenant's ceiling; 0 means unlimited.
	MaxRunsPerTenant int `json:"max_runs_per_tenant"`
	// PerTenant counts runs by tenant. Runs with no tenant are counted under
	// the empty string, so the total always adds up.
	PerTenant map[string]int `json:"per_tenant,omitempty"`
	// Tenants are the tenants with a run in flight, sorted, for a host that
	// wants to list them without walking the map.
	Tenants []string `json:"tenants,omitempty"`
}

// Capacity returns what this service is carrying.
func (s *Service) Capacity() Capacity {
	if s == nil {
		return Capacity{}
	}
	s.cancelMu.RLock()
	defer s.cancelMu.RUnlock()

	c := Capacity{
		ActiveRuns:        len(s.runs),
		MaxConcurrentRuns: s.maxConcurrentRuns,
		MaxRunsPerTenant:  s.maxRunsPerTenant,
		PerTenant:         make(map[string]int, len(s.runs)),
	}
	for _, h := range s.runs {
		c.PerTenant[h.Tenant]++
	}
	for tenant := range c.PerTenant {
		if tenant != "" {
			c.Tenants = append(c.Tenants, tenant)
		}
	}
	sort.Strings(c.Tenants)
	return c
}

// admitLocked decides whether one more run may start. Called with cancelMu
// held for writing, by registerRun, so the decision and the registration that
// acts on it cannot be separated by another run starting in between — a check
// that is not atomic with the reservation it authorises is not a limit, it is
// a suggestion.
func (s *Service) admitLocked(tenant string) error {
	if s.maxConcurrentRuns > 0 && len(s.runs) >= s.maxConcurrentRuns {
		return &CapacityError{Active: len(s.runs), Limit: s.maxConcurrentRuns, err: ErrAtCapacity}
	}
	if s.maxRunsPerTenant > 0 && tenant != "" {
		active := 0
		for _, h := range s.runs {
			if h.Tenant == tenant {
				active++
			}
		}
		if active >= s.maxRunsPerTenant {
			return &CapacityError{Tenant: tenant, Active: active, Limit: s.maxRunsPerTenant, err: ErrTenantAtCapacity}
		}
	}
	return nil
}

// ActiveRunsForTenant lists the runs one tenant has in flight.
func (s *Service) ActiveRunsForTenant(tenant string) []ActiveRun {
	tenant = strings.TrimSpace(tenant)
	out := make([]ActiveRun, 0)
	for _, r := range s.ActiveRuns() {
		if r.Tenant == tenant {
			out = append(out, r)
		}
	}
	return out
}

// CancelTenant stops every run belonging to one tenant and reports how many
// it stopped.
//
// The operator's verb. "Stop everything that customer is doing" had no
// expression at all: a host would have had to track run ids per tenant itself
// and call CancelRun in a loop, racing every run that started meanwhile.
//
// Like the other cancels, a run inside a tool marked InterruptBehavior "block"
// is left alone and not counted; call again once it completes.
func (s *Service) CancelTenant(tenant string) int {
	tenant = strings.TrimSpace(tenant)
	if s == nil || tenant == "" {
		return 0
	}
	if s.hasBlockingToolInProgress() {
		s.cancelLog("cancellation deferred: a blocking tool is still in progress")
		return 0
	}

	s.cancelMu.Lock()
	var stop []*runHandle
	for _, h := range s.runs {
		if h.Tenant == tenant {
			stop = append(stop, h)
		}
	}
	s.cancelMu.Unlock()

	for _, h := range stop {
		h.cancel()
	}
	return len(stop)
}
