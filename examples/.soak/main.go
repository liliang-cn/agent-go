// A soak run: a real coding task, driven through RunSegments, with a sandbox.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/agent-go/v3/pkg/sandbox"
)

// filePlans persists the plan to disk so it survives the process, which is the
// whole point on a task that may outlive it.
type filePlans struct {
	mu   sync.Mutex
	path string
}

func (f *filePlans) LoadPlan(_ context.Context, key string) ([]agent.PlanItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(f.path)
	if err != nil {
		return nil, nil
	}
	var all map[string][]agent.PlanItem
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, nil
	}
	return all[key], nil
}

func (f *filePlans) SavePlan(_ context.Context, key string, items []agent.PlanItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := map[string][]agent.PlanItem{}
	if b, err := os.ReadFile(f.path); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	all[key] = items
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.path, b, 0o644)
}

func env(k string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		fmt.Fprintf(os.Stderr, "missing %s\n", k)
		os.Exit(2)
	}
	return v
}

func envInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func main() {
	work := env("SOAK_DIR")
	if err := os.MkdirAll(work, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	p, err := pool.NewPool(pool.PoolConfig{Enabled: true, Strategy: pool.StrategyRoundRobin,
		Providers: []pool.Provider{{Name: "cpa", BaseURL: env("CPA_BASE_URL"), Key: env("CPA_API_KEY"),
			ModelName: env("CPA_MODEL"), MaxConcurrency: 4, Capability: 8}}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer p.Close()
	client, err := p.Get()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	sb, err := sandbox.NewLocal(sandbox.WithWorkspace(filepath.Join(work, "workspace")))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer sb.Close()

	home := filepath.Join(work, "agentgo-home")
	cfg := &config.Config{Home: home, RAG: config.RAGConfig{Enabled: false},
		Memory: config.MemoryConfig{StoreType: "file", MemoryPath: filepath.Join(home, "data", "memories")}}
	cfg.ApplyHomeLayout()

	activity, err := os.OpenFile(filepath.Join(work, "activity.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer activity.Close()

	svc, err := agent.New("kernel-builder").
		WithConfig(cfg).
		WithLLM(client).
		WithSandbox(sb).
		WithObserver(agent.NewActivityLog(activity)).
		WithAutonomy(agent.AutonomyProfile{Scratchpad: true, CheckpointEveryRounds: 1}).
		Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer svc.Close()

	goal := `Build a production-shaped e-commerce REST API in Go, in the workspace.

Nothing is there yet. Start with "go mod init shop". Use only the standard library plus
modernc.org/sqlite (pure Go, no cgo) — run "go mod tidy" after adding it.

The gate for every milestone is exactly this, from the workspace root:

  go build ./... && go vet ./... && go test ./... 2>&1 | tail -30

A milestone counts only when that command passes with its tests present and passing,
and every earlier milestone's tests still passing. Never delete or skip a test to go green.

Milestones, in order:

  1.  BUILD-OK        go.mod, cmd/server/main.go, an http.ServeMux, GET /healthz returning
                      {"status":"ok"}, and a test that starts the handler and asserts it.
  2.  STORE-OK        SQLite schema and migrations run at startup: users, products, carts,
                      cart_items, orders, order_items. A store package with an interface and
                      tests against a temp file DB.
  3.  CATALOG-OK      products CRUD over HTTP, plus list with search, category filter,
                      price range, sort and keyset pagination. Table-driven tests.
  4.  AUTH-OK         register and login with salted password hashing, signed session tokens,
                      an auth middleware, and a role field. Tests for happy path, wrong
                      password, expired token and missing header.
  5.  CART-OK         per-user cart: add, change quantity, remove, view with a computed total.
                      Rejects unknown products and non-positive quantities. Tests.
  6.  ORDER-OK        checkout turns a cart into an order inside one transaction and decrements
                      stock. Rejects checkout when stock is short. Tests.
  7.  CONCURRENCY-OK  a test that fires N concurrent checkouts against stock of 1 and asserts
                      exactly one succeeds and stock never goes negative.
  8.  IDEMPOTENCY-OK  an Idempotency-Key header on checkout: the same key replays the first
                      order instead of creating a second. Test with concurrent duplicates.
  9.  PAYMENT-OK      a mock payment provider behind an interface, and an order state machine
                      (pending → paid → shipped → delivered, plus cancelled/refunded) that
                      refuses illegal transitions. Tests cover every legal and one illegal edge.
  10. ADMIN-OK        admin-only endpoints (create product, adjust stock, list all orders)
                      returning 403 for non-admins. Tests.
  11. RATELIMIT-OK    a per-client token-bucket middleware returning 429 with Retry-After.
                      A test that exhausts and then recovers the bucket.
  12. E2E-OK          one test that runs the whole flow against a real httptest server:
                      register, login, browse, add to cart, checkout, pay, admin ships it.
  13. DOCS-OK         an OpenAPI 3 document served at GET /openapi.json, and a test asserting
                      every route the mux registers appears in it.

Keep files small and packages honest: cmd/server, internal/store, internal/api,
internal/auth, internal/payment. Write the test with the code, never after.

Whenever you settle something a later stretch of work must not contradict or rediscover,
write it into AGENT_NOTES.md straight away: exact function signatures, route paths and
request shapes, table columns, an approach you ruled out and why. That file's contents
travel with you; nothing else in the workspace does.

Keep a scratchpad plan with one step per milestone. The moment the gate command passes for a
milestone, check its step off and put the evidence in its note: the test names that prove it,
the files that hold it, and any decision the next stretch of work must not contradict (a table
column, a route shape, an interface signature). That note is all a later stretch will have to
go on, so write it as if the next person has never seen this code.

When all thirteen are done and the gate command passes, reply with the final output of
"go test ./..." and nothing else.`

	segments := envInt("SOAK_SEGMENTS", 12)
	rounds := envInt("SOAK_ROUNDS", 25)
	minutes := envInt("SOAK_MINUTES", 45)

	fmt.Printf("soak: model=%s dir=%s\n", env("CPA_MODEL"), work)
	fmt.Printf("      %d segments x %d rounds, %d minute limit\n\n", segments, rounds, minutes)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(minutes+10)*time.Minute)
	defer cancel()

	began := time.Now()
	segOpts := []agent.RunOption{agent.WithLLMRetries(8)}
	if tid := strings.TrimSpace(os.Getenv("SOAK_TASK_ID")); tid != "" {
		segOpts = append(segOpts, agent.WithTaskID(tid))
		fmt.Printf("      resuming task %s\n", tid)
	}

	res, err := svc.RunSegments(ctx, goal, agent.LongRunConfig{
		MaxSegments:            segments,
		RoundsPerSegment:       rounds,
		MaxConsecutiveFailures: 4,
		MaxDuration:            time.Duration(minutes) * time.Minute,
		SegmentRetryBackoff:    30 * time.Second,
	}, segOpts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "RunSegments:", err)
		os.Exit(1)
	}

	fmt.Printf("\n════ soak result ════\n")
	fmt.Printf("stop        : %s (done=%v)\n", res.Stop, res.Done())
	fmt.Printf("segments    : %d\n", len(res.Segments))
	for _, s := range res.Segments {
		status := string(s.StopReason)
		if s.Error != "" {
			status = "FAILED: " + trunc(s.Error, 60)
		}
		w := ""
		if s.WaitedBefore > 0 {
			w = fmt.Sprintf(" (waited %s first)", s.WaitedBefore.Round(time.Second))
		}
		fmt.Printf("  #%-2d %-14s %-7s%s\n", s.Index, status, s.Duration.Round(time.Second), w)
	}
	fmt.Printf("wall clock  : %s\n", time.Since(began).Round(time.Second))
	fmt.Printf("cost        : $%.4f\n", res.TotalCostUSD)
	if u := res.TotalUsage; u != nil {
		fmt.Printf("tokens      : %d prompt (%d cached), %d completion\n",
			u.PromptTokens, u.CachedPromptTokens, u.CompletionTokens)
	}
	fmt.Printf("\nplan:\n%s\n", res.PlanSummary)
	fmt.Printf("\nfinal:\n%s\n", trunc(res.Text, 2000))
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ = domain.Message{}
