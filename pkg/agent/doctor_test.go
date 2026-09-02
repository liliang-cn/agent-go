package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// findCheck returns the named check, or fails the test.
func findCheck(t *testing.T, r *DoctorReport, name string) DoctorCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in report:\n%s", name, r.Summary())
	return DoctorCheck{}
}

func hasCheck(r *DoctorReport, name string) bool {
	for _, c := range r.Checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

// healthyHome writes a home with a provider and an embedder configured, which
// is what a working install looks like.
func healthyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	cfg.LLM = config.LLMConfig{Providers: []pool.Provider{{
		Name: "local", BaseURL: "http://127.0.0.1:43510/v1", Key: "sk-test", ModelName: "test-model",
	}}}
	cfg.RAG.Embedding = config.EmbeddingConfig{Providers: []pool.Provider{{
		Name: "embed", BaseURL: "http://127.0.0.1:43511/v1", Key: "sk-test", ModelName: "embed-model",
	}}}
	if err := saveDoctorProviders(t, cfg); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	return home
}

// saveDoctorProviders persists the providers the way the config layer reads
// them back, so the doctor sees a real database rather than an in-memory
// struct.
func saveDoctorProviders(t *testing.T, cfg *config.Config) error {
	t.Helper()
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		return err
	}
	defer db.Close()
	for _, p := range cfg.LLM.Providers {
		if err := db.SaveProvider(&store.LLMProvider{
			Name: p.Name, BaseURL: p.BaseURL, Key: p.Key, ModelName: p.ModelName,
			Models: p.Models, MaxConcurrency: p.MaxConcurrency, Capability: p.Capability, Enabled: true,
		}); err != nil {
			return err
		}
	}
	for _, p := range cfg.RAG.Embedding.Providers {
		if err := db.SaveEmbeddingProvider(&store.EmbeddingProvider{
			Name: p.Name, BaseURL: p.BaseURL, Key: p.Key, ModelName: p.ModelName,
			MaxConcurrency: p.MaxConcurrency, Capability: p.Capability, Enabled: true,
		}); err != nil {
			return err
		}
	}
	return nil
}

// writeDoctorConfigValue puts one key straight into the config table, past
// any validation the typed setters do — the doctor's job is to notice what
// is already there.
func writeDoctorConfigValue(t *testing.T, cfg *config.Config, key, value string) error {
	t.Helper()
	db, err := store.NewAgentGoDB(cfg.AgentDBPath())
	if err != nil {
		return err
	}
	defer db.Close()
	return db.SaveConfig(key, value)
}

func TestDoctorHealthyHome(t *testing.T) {
	home := healthyHome(t)

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !report.Healthy() {
		t.Fatalf("expected a healthy report:\n%s", report.Summary())
	}
	if report.Home != home {
		t.Errorf("Home = %q, want %q", report.Home, home)
	}
	for _, name := range []string{
		"home.exists", "home.writable", "home.dir.data", "db.exists", "db.schema",
		"db.agents", "db.tasks", "db.checkpoints", "llm.providers",
		"embedding.providers", "memory.store_type", "mcp.config", "skills.paths",
	} {
		if !hasCheck(report, name) {
			t.Errorf("missing check %q", name)
		}
	}
	if got := findCheck(t, report, "llm.provider.local"); got.Status != DoctorOK {
		t.Errorf("provider check = %v %q", got.Status, got.Detail)
	}

	// A report is something people paste into issues. The key must not be in it.
	if strings.Contains(report.Summary(), "sk-test") {
		t.Fatalf("the report leaked an API key:\n%s", report.Summary())
	}
	if !strings.Contains(findCheck(t, report, "llm.provider.local").Detail, "key set") {
		t.Error("expected the provider check to say whether a key is present")
	}
}

// TestDoctorMissingDatabase pins the ordering the check depends on: loading
// config creates agentgo.db, so the doctor has to look before it loads. A
// check that ran afterwards could never report a fresh install.
func TestDoctorMissingDatabase(t *testing.T) {
	home := t.TempDir()

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	db := findCheck(t, report, "db.exists")
	if db.Status != DoctorWarn {
		t.Errorf("db.exists = %v, want warn on a fresh home", db.Status)
	}
	if !strings.Contains(db.Detail, "created empty") {
		t.Errorf("db.exists detail = %q", db.Detail)
	}
	// No providers is a failure, not a warning: nothing can run.
	if got := findCheck(t, report, "llm.providers"); got.Status != DoctorFail {
		t.Errorf("llm.providers = %v, want fail with no provider", got.Status)
	}
	if report.Healthy() {
		t.Error("an install with no provider is not healthy")
	}
}

func TestDoctorBadProviderURL(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	cfg.LLM = config.LLMConfig{Providers: []pool.Provider{
		{Name: "no-scheme", BaseURL: "example.com/v1", Key: "k", ModelName: "m"},
		{Name: "no-url", BaseURL: "", Key: "k", ModelName: "m"},
		// A provider without a model cannot be seeded: the store refuses it
		// at write time, so the doctor never meets one through the database.
	}}
	if err := saveDoctorProviders(t, cfg); err != nil {
		t.Fatalf("seed providers: %v", err)
	}

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	for name, want := range map[string]string{
		"llm.provider.no-scheme": "scheme",
		"llm.provider.no-url":    "no base URL",
	} {
		got := findCheck(t, report, name)
		if got.Status != DoctorFail {
			t.Errorf("%s = %v, want fail", name, got.Status)
		}
		if !strings.Contains(got.Detail, want) {
			t.Errorf("%s detail = %q, want it to mention %q", name, got.Detail, want)
		}
		if got.Fix == "" {
			t.Errorf("%s has no fix", name)
		}
	}
	if report.Healthy() {
		t.Error("a report with three failed providers is not healthy")
	}
}

// TestDoctorProviderWithoutKeyWarns keeps the distinction that matters for a
// local endpoint: no key is normal for one and fatal for a hosted provider,
// and only the caller knows which they have.
func TestDoctorProviderWithoutKeyWarns(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	cfg.LLM = config.LLMConfig{Providers: []pool.Provider{
		{Name: "ollama", BaseURL: "http://127.0.0.1:43512/v1", ModelName: "qwen3"},
	}}
	if err := saveDoctorProviders(t, cfg); err != nil {
		t.Fatalf("seed providers: %v", err)
	}

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	got := findCheck(t, report, "llm.provider.ollama")
	if got.Status != DoctorWarn {
		t.Errorf("keyless provider = %v, want warn", got.Status)
	}
	if !report.Healthy() {
		t.Errorf("a keyless local provider must not make the install unhealthy:\n%s", report.Summary())
	}
}

func TestDoctorUnknownMemoryStoreType(t *testing.T) {
	home := healthyHome(t)
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	// Bypass SetMemoryStoreType, which rejects the value: the point is what
	// the doctor says about a database that already holds one.
	if err := writeDoctorConfigValue(t, cfg, "memory.store_type", "nowhere-flow"); err != nil {
		t.Fatalf("seed store type: %v", err)
	}

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	got := findCheck(t, report, "memory.store_type")
	if got.Status != DoctorFail {
		t.Fatalf("memory.store_type = %v %q, want fail", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "nowhere-flow") {
		t.Errorf("detail = %q, want it to name the store type", got.Detail)
	}
	if !strings.Contains(got.Fix, "RegisterMemoryStore") {
		t.Errorf("fix = %q, want it to name the registration seam", got.Fix)
	}
	if report.Healthy() {
		t.Error("a memory store type nothing can build is a failure")
	}
}

// TestDoctorAcceptsRegisteredMemoryStore is the other half: the same name is
// fine once a plugin claims it, which is the whole point of the registry.
func TestDoctorAcceptsRegisteredMemoryStore(t *testing.T) {
	const name = "doctor-test-store"
	if err := RegisterMemoryStore(name, func(domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer UnregisterMemoryStore(name)

	home := healthyHome(t)
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	if err := writeDoctorConfigValue(t, cfg, "memory.store_type", name); err != nil {
		t.Fatalf("seed store type: %v", err)
	}

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	got := findCheck(t, report, "memory.store_type")
	if got.Status != DoctorOK {
		t.Fatalf("memory.store_type = %v %q, want ok", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "registered plugin") {
		t.Errorf("detail = %q, want it to say the name came from the registry", got.Detail)
	}
}

func TestDoctorParsesMCPServers(t *testing.T) {
	home := healthyHome(t)
	servers := map[string]any{
		"mcpServers": map[string]any{
			"filesystem": map[string]any{
				"command": "definitely-not-installed-agentgo-doctor",
				"args":    []string{"--root", home},
			},
		},
	}
	blob, err := json.Marshal(servers)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "mcpServers.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	cfgCheck := findCheck(t, report, "mcp.config")
	if cfgCheck.Status != DoctorOK {
		t.Fatalf("mcp.config = %v %q", cfgCheck.Status, cfgCheck.Detail)
	}
	// A stdio server whose binary is not on PATH is the commonest MCP
	// breakage, and finding it is a filesystem read rather than a connection.
	srv := findCheck(t, report, "mcp.server.filesystem")
	if srv.Status != DoctorFail || !strings.Contains(srv.Detail, "not on PATH") {
		t.Errorf("mcp.server.filesystem = %v %q", srv.Status, srv.Detail)
	}
}

func TestDoctorRejectsMalformedMCPConfig(t *testing.T) {
	home := healthyHome(t)
	if err := os.WriteFile(filepath.Join(home, "mcpServers.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if got := findCheck(t, report, "mcp.config"); got.Status != DoctorFail {
		t.Errorf("mcp.config = %v %q, want fail", got.Status, got.Detail)
	}
}

func TestDoctorCountsSkills(t *testing.T) {
	home := healthyHome(t)
	skillDir := filepath.Join(home, "skills", "greeter")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := `---
name: greeter
description: Greets people.
when_to_use: when the user says hello
---

Say hello back.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Doctor(context.Background(), WithDoctorHome(home))
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	got := findCheck(t, report, "skills.load")
	if got.Status != DoctorOK || !strings.Contains(got.Detail, "skill(s) parsed") {
		t.Fatalf("skills.load = %v %q", got.Status, got.Detail)
	}
}

// TestDoctorSummaryIsGreppable checks the shape callers actually consume: one
// line per check, the status first, and a trailing tally.
func TestDoctorSummaryIsGreppable(t *testing.T) {
	report := &DoctorReport{Home: "/tmp/home", Checks: []DoctorCheck{
		{Name: "home.exists", Status: DoctorOK, Detail: "/tmp/home"},
		{Name: "llm.providers", Status: DoctorFail, Detail: "none", Fix: "add one"},
		{Name: "skills.paths", Status: DoctorWarn, Detail: "none"},
	}}
	out := report.Summary()
	for _, want := range []string{"[ok  ] home.exists", "[fail] llm.providers", "fix: add one", "1 ok, 1 warn, 1 fail"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	if report.Healthy() {
		t.Error("a report with a failure is not healthy")
	}

	ok, warn, fail := report.Counts()
	if ok != 1 || warn != 1 || fail != 1 {
		t.Errorf("Counts() = %d/%d/%d", ok, warn, fail)
	}

	var nilReport *DoctorReport
	if nilReport.Healthy() {
		t.Error("a nil report is not healthy")
	}
	if nilReport.Summary() == "" {
		t.Error("a nil report should still render something")
	}
}
