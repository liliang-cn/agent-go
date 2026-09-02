// Package memorystoretest is the conformance suite a memory backend has to
// pass before anyone should trust it.
//
// It exists because the same bug keeps being found by hand. A backend goes
// in, its Store/Search/Get round trip passes, the store looks healthy — and
// the agent sees nothing, because the scope filter drops every row between
// the search and the prompt. That was `d0eb578` for the shared brain, and it
// is the one failure a store-level test cannot see: the store answers, the
// model is not told.
//
// So the suite asserts on the INJECTED TEXT. Every case runs a real
// memory.Service over the backend and checks what the agent would actually
// be shown, with `embedder = nil` — which is how a remote backend that owns
// its own embedding model is built, and the configuration under which the
// bug appeared.
//
// Backends legitimately differ in what they can do. A store that cannot
// search by vector, or cannot delete by session, says so with
// domain.ErrMemoryStoreUnsupported and the suite skips that case rather than
// failing it: an honest unsupported beats a fake implementation, and a
// conformance suite that punishes honesty gets fake implementations.
package memorystoretest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/memory"
)

// Factory builds the backend under test. It is called once per case, so a
// case cannot see another's writes; return a fresh namespace, collection or
// directory each time if the backend supports it.
type Factory func(t *testing.T) domain.MemoryStore

// Options tunes the suite for a backend's real limits.
type Options struct {
	// Eventual is how long to keep re-reading before deciding something was
	// never written. Zero means read-your-writes, which local stores have and
	// a networked index usually does not.
	Eventual time.Duration
	// SkipScoped turns off the session-scoped cases, for a backend that has
	// no notion of scope at all. Prefer leaving it on: scope is where these
	// backends break.
	SkipScoped bool
}

// Run executes the whole suite against one backend.
//
//	memorystoretest.Run(t, func(t *testing.T) domain.MemoryStore {
//	    return newMyStore(t)
//	}, memorystoretest.Options{Eventual: 2 * time.Second})
func Run(t *testing.T, newStore Factory, opts Options) {
	t.Helper()
	t.Run("GlobalMemoryReachesThePrompt", func(t *testing.T) {
		globalMemoryReachesThePrompt(t, newStore, opts)
	})
	t.Run("SessionMemoryReachesThePrompt", func(t *testing.T) {
		if opts.SkipScoped {
			t.Skip("backend declares no scope support")
		}
		sessionMemoryReachesThePrompt(t, newStore, opts)
	})
	t.Run("AnotherSessionsMemoryStaysOut", func(t *testing.T) {
		if opts.SkipScoped {
			t.Skip("backend declares no scope support")
		}
		anotherSessionsMemoryStaysOut(t, newStore, opts)
	})
	t.Run("StoreLevelRoundTrip", func(t *testing.T) {
		storeLevelRoundTrip(t, newStore, opts)
	})
	t.Run("UnsupportedIsHonest", func(t *testing.T) {
		unsupportedIsHonest(t, newStore)
	})
}

// serviceOver builds the memory service the way a backend that owns its own
// embedding model is built: no embedder, no generator.
func serviceOver(st domain.MemoryStore) *memory.Service {
	cfg := memory.DefaultConfig()
	cfg.MinScore = 0
	return memory.NewService(st, nil, nil, cfg)
}

// marker is a token no corpus contains, so a hit is this test's own write.
func marker() string { return "zz" + strings.ReplaceAll(uuid.NewString()[:8], "-", "") }

// The headline case: a memory written through the service comes back out of
// the prompt, not merely out of the store.
func globalMemoryReachesThePrompt(t *testing.T, newStore Factory, opts Options) {
	ctx := context.Background()
	svc := serviceOver(newStore(t))
	tok := marker()

	if err := svc.Add(ctx, &domain.Memory{
		ID: uuid.NewString(), Type: domain.MemoryTypeFact, ScopeType: domain.MemoryScopeGlobal,
		Content: "The " + tok + " protocol runs on port 43510.", CreatedAt: time.Now(), Importance: 0.9,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	injected := eventually(t, opts, func() string {
		text, _, err := svc.RetrieveAndInject(ctx, "what port does "+tok+" run on?", "")
		if err != nil {
			t.Fatalf("RetrieveAndInject: %v", err)
		}
		return text
	}, tok)

	if !strings.Contains(injected, tok) {
		t.Fatalf("the memory never reached the prompt.\n"+
			"This is the failure a store-level round trip cannot see: the store answers and the model is not told.\n"+
			"injected:\n%s", injected)
	}
}

// The same, under a session scope — where the scope filter lives.
func sessionMemoryReachesThePrompt(t *testing.T, newStore Factory, opts Options) {
	ctx := context.Background()
	svc := serviceOver(newStore(t))
	tok := marker()
	session := uuid.NewString()

	if err := svc.Add(ctx, &domain.Memory{
		ID: uuid.NewString(), Type: domain.MemoryTypeFact,
		SessionID: session, ScopeType: domain.MemoryScopeSession, ScopeID: session,
		Content:   "The " + tok + " deployment is owned by the platform team.",
		CreatedAt: time.Now(), Importance: 0.9,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	injected := eventually(t, opts, func() string {
		text, _, err := svc.RetrieveAndInject(ctx, "who owns "+tok+"?", session)
		if err != nil {
			t.Fatalf("RetrieveAndInject: %v", err)
		}
		return text
	}, tok)

	if !strings.Contains(injected, tok) {
		t.Fatalf("a session-scoped memory never reached the prompt of its own session.\n"+
			"Check what the backend puts in SessionID on the way back: mapping a remote bucket name\n"+
			"into it makes every row scope-less, and the scope filter then drops them all.\ninjected:\n%s", injected)
	}
}

// And the other half of scope: one conversation must not read another's.
func anotherSessionsMemoryStaysOut(t *testing.T, newStore Factory, opts Options) {
	ctx := context.Background()
	svc := serviceOver(newStore(t))
	tok := marker()
	mine, theirs := uuid.NewString(), uuid.NewString()

	if err := svc.Add(ctx, &domain.Memory{
		ID: uuid.NewString(), Type: domain.MemoryTypeFact,
		SessionID: theirs, ScopeType: domain.MemoryScopeSession, ScopeID: theirs,
		Content: "The " + tok + " credential is hunter2.", CreatedAt: time.Now(), Importance: 0.9,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Give a networked index the same chance to become visible that the
	// positive cases get, so this is not passing merely because it is slow.
	if opts.Eventual > 0 {
		time.Sleep(opts.Eventual)
	}

	text, _, err := svc.RetrieveAndInject(ctx, "what is the "+tok+" credential?", mine)
	if err != nil {
		t.Fatalf("RetrieveAndInject: %v", err)
	}
	if strings.Contains(text, tok) {
		t.Fatalf("another session's memory leaked into this one's prompt:\n%s", text)
	}
}

// The store's own contract, for the operations it claims to support.
func storeLevelRoundTrip(t *testing.T, newStore Factory, opts Options) {
	ctx := context.Background()
	st := newStore(t)
	tok := marker()

	mem := &domain.Memory{
		ID: uuid.NewString(), Type: domain.MemoryTypeFact, ScopeType: domain.MemoryScopeGlobal,
		Content: "The " + tok + " service listens on 47600.", CreatedAt: time.Now(), Importance: 0.8,
	}
	if err := st.Store(ctx, mem); err != nil {
		if unsupported(err) {
			t.Skip("backend cannot Store")
		}
		t.Fatalf("Store: %v", err)
	}

	// The id belongs to the store, not to the caller. A server-backed
	// backend mints its own and writes it back onto the memory; reading the
	// id we proposed instead is how an integration ends up addressing
	// something that does not exist on every later Get and Delete — which is
	// exactly what this suite caught the first time it met one.
	id := strings.TrimSpace(mem.ID)
	if id == "" {
		t.Fatal("Store left the memory with no id; nothing can address it afterwards")
	}

	if got, err := st.Get(ctx, id); err == nil && got != nil {
		if !strings.Contains(got.Content, tok) {
			t.Errorf("Get returned different content: %q", got.Content)
		}
	} else if err != nil && !unsupported(err) {
		t.Errorf("Get: %v", err)
	}

	hits, err := st.SearchByText(ctx, tok, 10)
	switch {
	case unsupported(err):
		// Fine — a vector-only backend says so, and the service falls back.
	case err != nil:
		t.Errorf("SearchByText: %v", err)
	case len(hits) == 0 && opts.Eventual == 0:
		t.Errorf("SearchByText found nothing it had just been given")
	}

	if err := st.Delete(ctx, id); err != nil && !unsupported(err) {
		t.Errorf("Delete: %v", err)
	}
}

// A backend that cannot do something must say so with the sentinel, not
// return a zero value that reads as an answer. An empty result and "I cannot
// do this" are different facts, and a caller that cannot tell them apart
// degrades wrongly.
func unsupportedIsHonest(t *testing.T, newStore Factory) {
	ctx := context.Background()
	st := newStore(t)

	// Every optional method is allowed to refuse; none may panic, and a
	// refusal has to be the documented sentinel.
	checks := map[string]error{
		"IncrementAccess": st.IncrementAccess(ctx, uuid.NewString()),
		"Clear":           st.Clear(ctx),
	}
	if _, err := st.GetByType(ctx, domain.MemoryTypeFact, 1); err != nil {
		checks["GetByType"] = err
	}
	if err := st.DeleteBySession(ctx, uuid.NewString()); err != nil {
		checks["DeleteBySession"] = err
	}
	for name, err := range checks {
		if err == nil || unsupported(err) {
			continue
		}
		// A real failure is fine to report, but it must not be a disguised
		// "unsupported" — those have a sentinel for a reason.
		if strings.Contains(strings.ToLower(err.Error()), "unsupported") ||
			strings.Contains(strings.ToLower(err.Error()), "not implemented") {
			t.Errorf("%s says unsupported in prose (%v); return domain.ErrMemoryStoreUnsupported so callers can branch on it", name, err)
		}
	}
}

func unsupported(err error) bool {
	return err != nil && errors.Is(err, domain.ErrMemoryStoreUnsupported)
}

// eventually re-reads until the marker shows up or the budget runs out. A
// local store answers on the first call; a networked index may need a moment
// to make a write searchable, and failing that as "never written" is how a
// working backend gets rejected.
func eventually(t *testing.T, opts Options, read func() string, want string) string {
	t.Helper()
	last := read()
	if opts.Eventual <= 0 || strings.Contains(last, want) {
		return last
	}
	deadline := time.Now().Add(opts.Eventual)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if last = read(); strings.Contains(last, want) {
			return last
		}
	}
	return last
}
