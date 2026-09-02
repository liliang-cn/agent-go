package agent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	"github.com/liliang-cn/agent-go/v3/pkg/mcp"
	"github.com/liliang-cn/agent-go/v3/pkg/pool"
	"github.com/liliang-cn/agent-go/v3/pkg/skills"
	"github.com/liliang-cn/agent-go/v3/pkg/store"
)

// Checking an install before it fails.
//
// v3 has no CLI, and it took with it the one thing that answered "is this
// machine set up correctly" before the first Ask. A config-driven install —
// AGENTGO_HOME, a database of providers, a memory store type, an MCP file, a
// skills directory — has a dozen ways to be quietly wrong, and every one of
// them surfaces the same way: a run fails somewhere deep with a message about
// the symptom rather than the cause. A provider with no base URL reads as a
// connection error. A store type nothing registered reads as a memory backend
// that returns nothing. A skills directory the process cannot read reads as an
// agent that never uses its skills.
//
// Doctor inspects and reports. It repairs nothing, and — the rule that makes
// it safe to run from a health endpoint or a start-up check — it never calls a
// model and, by default, never connects to anything. Loading configuration
// does materialise the home layout, because that is what config.Load does for
// every program that starts; the doctor deliberately looks at the database
// before that happens, so "the database does not exist yet" is a finding
// rather than something the check quietly fixed.

// DoctorStatus is one check's verdict.
type DoctorStatus string

const (
	// DoctorOK means the check found what it expected.
	DoctorOK DoctorStatus = "ok"
	// DoctorWarn means the install works but something is unconfigured or
	// degraded — no embedding provider, no skills, an empty database.
	DoctorWarn DoctorStatus = "warn"
	// DoctorFail means something is broken: a directory that cannot be read,
	// a database that will not open, a provider that cannot be used.
	DoctorFail DoctorStatus = "fail"
)

// DoctorCheck is one finding.
type DoctorCheck struct {
	// Name identifies the check, e.g. "home.layout" or "llm.provider.openai".
	Name string
	// Status is the verdict.
	Status DoctorStatus
	// Detail says what was found, in one line. It never contains an API key:
	// a key is reported as present or absent and nothing else, because a
	// report is something people paste into issues.
	Detail string
	// Fix is what to do about it, and is empty for a passing check.
	Fix string
}

// DoctorReport is everything Doctor found.
type DoctorReport struct {
	// Home is the resolved AgentGo home the report is about.
	Home string
	// Checks are the findings in the order they were made — home, database,
	// providers, memory, MCP, skills — because the first failure usually
	// explains the ones after it.
	Checks []DoctorCheck
}

// Add appends a check.
func (r *DoctorReport) Add(c DoctorCheck) {
	if r == nil {
		return
	}
	r.Checks = append(r.Checks, c)
}

func (r *DoctorReport) add(name string, status DoctorStatus, detail, fix string) {
	r.Add(DoctorCheck{Name: name, Status: status, Detail: detail, Fix: fix})
}

// Healthy reports whether nothing failed.
//
// A warning is not unhealthy. An install with no embedding provider and no
// skills is a working install — RAG is off and that is a supported shape — and
// a health check that called it broken would be wrong about most of them.
func (r *DoctorReport) Healthy() bool {
	if r == nil {
		return false
	}
	for _, c := range r.Checks {
		if c.Status == DoctorFail {
			return false
		}
	}
	return true
}

// Counts returns how many checks landed in each status.
func (r *DoctorReport) Counts() (ok, warn, fail int) {
	if r == nil {
		return 0, 0, 0
	}
	for _, c := range r.Checks {
		switch c.Status {
		case DoctorOK:
			ok++
		case DoctorWarn:
			warn++
		case DoctorFail:
			fail++
		}
	}
	return ok, warn, fail
}

// Summary renders the report as flat text, one line per check plus its fix.
// Greppable rather than pretty, for the same reason ActivityLog's format is.
func (r *DoctorReport) Summary() string {
	if r == nil {
		return "no report\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "agentgo doctor — home %s\n", doctorOr(r.Home, "(unresolved)"))
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "[%-4s] %-26s %s\n", c.Status, c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&b, "         fix: %s\n", c.Fix)
		}
	}
	ok, warn, fail := r.Counts()
	fmt.Fprintf(&b, "%d ok, %d warn, %d fail\n", ok, warn, fail)
	return b.String()
}

// doctorOptions is the resolved option set.
type doctorOptions struct {
	home       string
	probeMCP   bool
	mcpTimeout time.Duration
}

// DoctorOption configures a Doctor run.
type DoctorOption func(*doctorOptions)

// WithDoctorHome inspects a specific home directory instead of the one
// AGENTGO_HOME resolves to. For tests, and for checking an install other than
// the calling process's.
func WithDoctorHome(home string) DoctorOption {
	return func(o *doctorOptions) { o.home = home }
}

// WithMCPProbe turns on a bounded connectivity probe of each configured MCP
// server: connect, count the tools, disconnect, within timeout.
//
// It is off by default because starting an MCP server is a side effect. A
// stdio server is a process this machine will spawn, and an inspection that
// launches things is not an inspection. Turn it on deliberately, with a
// timeout you are willing to wait for — it is per server, not for all of them.
func WithMCPProbe(timeout time.Duration) DoctorOption {
	return func(o *doctorOptions) {
		o.probeMCP = true
		if timeout <= 0 {
			timeout = defaultMCPProbeTimeout
		}
		o.mcpTimeout = timeout
	}
}

const (
	defaultMCPProbeTimeout = 10 * time.Second
	// doctorTaskSample bounds how many tasks are read to count checkpoints.
	// A doctor that walks a hundred thousand rows is one nobody runs.
	doctorTaskSample = 200
)

// Doctor inspects an AgentGo install and reports what it finds, without
// calling a model and — unless WithMCPProbe is given — without connecting to
// anything.
//
// The error return is for the inspection itself failing (a home directory that
// cannot be resolved), not for the install being broken: a broken install is a
// report with failures in it. Callers should read Healthy() and Summary(), not
// only err.
func Doctor(ctx context.Context, opts ...DoctorOption) (*DoctorReport, error) {
	o := &doctorOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	home, err := doctorResolveHome(o.home)
	if err != nil {
		return nil, err
	}
	report := &DoctorReport{Home: home}

	// The database is inspected before the config is loaded, because loading
	// creates it. "Not there yet" is a fact worth reporting, not one to erase.
	dbPath := filepath.Join(home, "data", "agentgo.db")
	dbExisted := doctorFileExists(dbPath)

	doctorCheckHome(report, home)

	cfg, cfgErr := doctorLoadConfig(home)
	if cfgErr != nil {
		report.add("config.load", DoctorFail, cfgErr.Error(),
			"check that "+dbPath+" is readable and not corrupt")
		return report, nil
	}

	doctorCheckDatabase(report, cfg, dbPath, dbExisted)
	doctorCheckProviders(report, cfg)
	doctorCheckMemory(report, cfg)
	doctorCheckMCP(ctx, report, cfg, o)
	doctorCheckSkills(ctx, report, cfg)

	return report, nil
}

// doctorResolveHome mirrors config.Load's resolution — AGENTGO_HOME, then
// ~/.agentgo — because the doctor has to know the path before it is allowed to
// load anything from it.
func doctorResolveHome(override string) (string, error) {
	home := strings.TrimSpace(override)
	if home == "" {
		home = strings.TrimSpace(os.Getenv("AGENTGO_HOME"))
	}
	if home == "" {
		home = "~/.agentgo"
	}
	if strings.HasPrefix(home, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		home = filepath.Join(userHome, home[2:])
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", home, err)
	}
	return abs, nil
}

// doctorLoadConfig reuses the framework's own loading rather than re-reading
// the database. Anything the doctor parsed itself would be a second opinion,
// and a second opinion that agrees is useless while one that disagrees is a
// bug in the doctor.
func doctorLoadConfig(home string) (*config.Config, error) {
	cfg := &config.Config{Home: home}
	cfg.ApplyHomeLayout()
	if err := cfg.LoadDBBackedRuntime(); err != nil {
		return nil, err
	}
	// The DB-backed runtime carries providers and the embedding model, but
	// not the memory store type: a host names that at build time. An install
	// that recorded one under memory.store_type is checked as it stands —
	// unvalidated on purpose, since noticing a value nothing can build is
	// the point.
	if db, err := store.NewAgentGoDB(cfg.AgentDBPath()); err == nil {
		if v, err := db.GetConfig("memory.store_type"); err == nil && strings.TrimSpace(v) != "" {
			cfg.Memory.StoreType = config.MemoryStoreType(strings.TrimSpace(v))
		}
		_ = db.Close()
	}
	return cfg, nil
}

func doctorCheckHome(r *DoctorReport, home string) {
	info, err := os.Stat(home)
	switch {
	case os.IsNotExist(err):
		r.add("home.exists", DoctorWarn, home+" does not exist yet",
			"it is created on first use; set AGENTGO_HOME to point somewhere else")
		return
	case err != nil:
		r.add("home.exists", DoctorFail, err.Error(),
			"make "+home+" readable by this process, or set AGENTGO_HOME")
		return
	case !info.IsDir():
		r.add("home.exists", DoctorFail, home+" is a file, not a directory",
			"remove it or point AGENTGO_HOME at a directory")
		return
	}
	r.add("home.exists", DoctorOK, home, "")

	if err := doctorCheckWritable(home); err != nil {
		r.add("home.writable", DoctorFail, err.Error(),
			"grant this process write access to "+home)
	} else {
		r.add("home.writable", DoctorOK, "writable", "")
	}

	// The layout from the storage docs. A missing directory is not a failure
	// — ApplyHomeLayout creates them — but one that exists as a file is.
	for _, dir := range []string{"data", "skills", "workspace"} {
		path := filepath.Join(home, dir)
		info, err := os.Stat(path)
		switch {
		case os.IsNotExist(err):
			r.add("home.dir."+dir, DoctorWarn, path+" missing", "created on first use")
		case err != nil:
			r.add("home.dir."+dir, DoctorFail, err.Error(), "make "+path+" readable")
		case !info.IsDir():
			r.add("home.dir."+dir, DoctorFail, path+" is a file, not a directory",
				"remove "+path)
		default:
			r.add("home.dir."+dir, DoctorOK, path, "")
		}
	}
}

// doctorCheckWritable proves write access by creating and removing a file,
// which is the only way to know: a mode bit does not account for ACLs, a
// read-only mount, or a full disk.
func doctorCheckWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".agentgo-doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func doctorCheckDatabase(r *DoctorReport, cfg *config.Config, dbPath string, existed bool) {
	if !existed {
		r.add("db.exists", DoctorWarn, dbPath+" did not exist and was created empty",
			"this is a fresh install; add a provider before running an agent")
	} else {
		r.add("db.exists", DoctorOK, dbPath, "")
	}

	store, err := NewStore(cfg.AgentDBPath())
	if err != nil {
		r.add("db.open", DoctorFail, err.Error(),
			"check that "+dbPath+" is a readable SQLite database")
		return
	}
	defer store.Close()

	// NewStore migrates on open, so reaching here means the schema is current.
	r.add("db.schema", DoctorOK, "schema present and migrated", "")

	agents, err := store.ListAgentModels()
	if err != nil {
		r.add("db.agents", DoctorFail, err.Error(), "the agents table is unreadable")
	} else if len(agents) == 0 {
		r.add("db.agents", DoctorWarn, "no agent definitions stored",
			"Manager.SeedDefaultAgent() creates one; a Service built with agent.New(...) needs none")
	} else {
		r.add("db.agents", DoctorOK, fmt.Sprintf("%d agent definition(s)", len(agents)), "")
	}

	if sessions, err := store.ListSessions(doctorTaskSample); err != nil {
		r.add("db.sessions", DoctorFail, err.Error(), "the sessions table is unreadable")
	} else {
		r.add("db.sessions", DoctorOK,
			fmt.Sprintf("%d session(s) in the most recent %d", len(sessions), doctorTaskSample), "")
	}

	tasks, err := store.ListTasks(doctorTaskSample)
	if err != nil {
		r.add("db.tasks", DoctorFail, err.Error(), "the tasks table is unreadable")
		return
	}
	r.add("db.tasks", DoctorOK, fmt.Sprintf("%d task(s) in the most recent %d", len(tasks), doctorTaskSample), "")

	checkpoints := 0
	for _, task := range tasks {
		if task == nil {
			continue
		}
		cps, err := store.ListTaskCheckpoints(task.ID, MaxCheckpointsPerTask)
		if err != nil {
			r.add("db.checkpoints", DoctorFail, err.Error(), "the task_checkpoints table is unreadable")
			return
		}
		checkpoints += len(cps)
	}
	r.add("db.checkpoints", DoctorOK,
		fmt.Sprintf("%d checkpoint(s) across those tasks", checkpoints), "")
}

func doctorCheckProviders(r *DoctorReport, cfg *config.Config) {
	if len(cfg.LLM.Providers) == 0 {
		r.add("llm.providers", DoctorFail, "no LLM provider configured",
			"add one to agentgo.db, or inject a model with agent.New(...).WithLLM(...)")
	} else {
		r.add("llm.providers", DoctorOK,
			fmt.Sprintf("%d provider(s), strategy %s", len(cfg.LLM.Providers), doctorOr(string(cfg.LLM.Strategy), "default")), "")
		for _, p := range cfg.LLM.Providers {
			doctorCheckProvider(r, "llm.provider", p)
		}
	}

	embedders := cfg.RAG.Embedding.Providers
	switch {
	case len(embedders) == 0:
		r.add("embedding.providers", DoctorWarn, "no embedding provider configured",
			"vector memory and RAG stay disabled; agent, tools, MCP and skills work without one")
	default:
		r.add("embedding.providers", DoctorOK, fmt.Sprintf("%d provider(s)", len(embedders)), "")
		for _, p := range embedders {
			doctorCheckProvider(r, "embedding.provider", p)
		}
	}

	if cfg.RAG.Enabled && len(embedders) == 0 && strings.TrimSpace(cfg.RAG.EmbeddingModel) == "" {
		r.add("rag.enabled", DoctorFail, "RAG is enabled but nothing can embed",
			"configure an embedding provider or turn RAG off")
	}
}

// doctorCheckProvider validates one provider's shape. It never prints the key
// and never contacts the endpoint: a syntactically valid URL that is down is a
// different problem from one that was never filled in, and only the second is
// a configuration fault.
func doctorCheckProvider(r *DoctorReport, prefix string, p pool.Provider) {
	name := doctorOr(p.Name, "(unnamed)")
	check := prefix + "." + name

	var problems []string
	base := strings.TrimSpace(p.BaseURL)
	switch {
	case base == "":
		problems = append(problems, "no base URL")
	default:
		u, err := url.Parse(base)
		switch {
		case err != nil:
			problems = append(problems, "base URL does not parse: "+err.Error())
		case u.Scheme != "http" && u.Scheme != "https":
			problems = append(problems, "base URL scheme is "+doctorOr(u.Scheme, "(none)")+", want http or https")
		case u.Host == "":
			problems = append(problems, "base URL has no host")
		}
	}
	if strings.TrimSpace(p.ModelName) == "" && len(p.Models) == 0 {
		problems = append(problems, "no model name")
	}

	keyState := "key set"
	if strings.TrimSpace(p.Key) == "" {
		keyState = "no key"
	}

	if len(problems) > 0 {
		r.add(check, DoctorFail, strings.Join(problems, "; ")+" ("+keyState+")",
			"fix this provider's record in agentgo.db")
		return
	}
	if keyState == "no key" {
		r.add(check, DoctorWarn, base+" — "+doctorOr(p.ModelName, strings.Join(p.Models, ","))+", no key",
			"local endpoints often need none; a hosted one will reject every call")
	} else {
		r.add(check, DoctorOK, base+" — "+doctorOr(p.ModelName, strings.Join(p.Models, ","))+", key set", "")
	}
	if prefix == "llm.provider" {
		doctorCheckPricing(r, check, p)
	}
}

// doctorCheckPricing says whether the runtime can put a price on this
// provider's models. An unpriced model is not an error — the run works — but
// every cost figure reads 0 and the two spending ceilings (MaxBudgetUSD,
// MaxTotalCostUSD) never trigger, which on a multi-hour task is the one
// safeguard the operator thought they had. Seen live: a gateway alias like
// gemini-3.7-flash-high, 900k tokens, "$0".
func doctorCheckPricing(r *DoctorReport, check string, p pool.Provider) {
	models := p.Models
	if m := strings.TrimSpace(p.ModelName); m != "" {
		models = append([]string{m}, models...)
	}
	var unpriced []string
	for _, m := range models {
		if _, known := pool.LookupModelPricing(m); !known {
			unpriced = append(unpriced, m)
		}
	}
	if len(unpriced) == 0 {
		r.add(check+".pricing", DoctorOK, "every model is priced", "")
		return
	}
	r.add(check+".pricing", DoctorWarn,
		"no pricing for "+strings.Join(unpriced, ", ")+"; cost reads 0 and MaxBudgetUSD / MaxTotalCostUSD never trigger",
		"pool.RegisterModelPricing(\""+unpriced[0]+"\", pool.ModelPricing{InputPer1K: …, CachedInputPer1K: …, OutputPer1K: …})")
}

func doctorCheckMemory(r *DoctorReport, cfg *config.Config) {
	storeType := cfg.GetMemoryStoreType()
	name := storeType.String()

	switch {
	case storeType.IsBuiltin():
		r.add("memory.store_type", DoctorOK, name+" (built in)", "")
	case domain.MemoryStoreRegistered(name):
		r.add("memory.store_type", DoctorOK, name+" (registered plugin)", "")
	default:
		known := domain.RegisteredMemoryStores()
		detail := name + " is neither built in nor registered"
		fix := "call agent.RegisterMemoryStore(\"" + name + "\", ...) before building a service"
		if len(known) > 0 {
			detail += "; registered: " + strings.Join(known, ", ")
		}
		r.add("memory.store_type", DoctorFail, detail, fix)
		return
	}

	if storeType.UsesFile() {
		path := cfg.Memory.MemoryPath
		info, err := os.Stat(path)
		switch {
		case os.IsNotExist(err):
			r.add("memory.path", DoctorWarn, path+" missing", "created on first write")
		case err != nil:
			r.add("memory.path", DoctorFail, err.Error(), "make "+path+" readable")
		case !info.IsDir():
			r.add("memory.path", DoctorFail, path+" is a file, not a directory", "remove "+path)
		default:
			r.add("memory.path", DoctorOK, path, "")
		}
	}

	if storeType.UsesVector() && len(cfg.RAG.Embedding.Providers) == 0 {
		r.add("memory.embedder", DoctorWarn,
			name+" is vector-backed but no embedding provider is configured",
			"retrieval falls back to text search; configure an embedder for vector recall")
	}
}

func doctorCheckMCP(ctx context.Context, r *DoctorReport, cfg *config.Config, o *doctorOptions) {
	paths := cfg.MCPServersPaths()
	present := make([]string, 0, len(paths))
	for _, p := range paths {
		if doctorFileExists(p) {
			present = append(present, p)
		}
	}
	if len(present) == 0 {
		r.add("mcp.config", DoctorWarn, "no mcpServers.json found in "+strings.Join(paths, ", "),
			"MCP tools are unavailable; write one at "+cfg.MCPServersPath()+" to add servers")
		return
	}

	mcpCfg, err := config.LoadMCPConfig(present...)
	if err != nil {
		r.add("mcp.config", DoctorFail, err.Error(),
			"fix the JSON in "+strings.Join(present, ", "))
		return
	}
	servers := mcpCfg.GetLoadedServers()
	r.add("mcp.config", DoctorOK,
		fmt.Sprintf("%s parsed, %d server(s)", strings.Join(present, ", "), len(servers)), "")

	for _, s := range servers {
		doctorCheckMCPServer(ctx, r, s, o)
	}
}

func doctorCheckMCPServer(ctx context.Context, r *DoctorReport, s mcp.ServerConfig, o *doctorOptions) {
	check := "mcp.server." + doctorOr(s.Name, "(unnamed)")

	// NewClient validates the config's shape — a URL for http/sse, a command
	// for stdio — without dialing anything.
	client, err := mcp.NewClient(&s, nil)
	if err != nil {
		r.add(check, DoctorFail, err.Error(), "fix this server's entry in mcpServers.json")
		return
	}

	// For a stdio server the commonest breakage is not the config but a
	// missing binary, and looking it up on PATH is a filesystem read, not a
	// connection.
	if (s.Type == mcp.ServerTypeStdio || s.Type == "") && len(s.Command) > 0 {
		if !mcp.CheckCommandAvailable(s.Command[0]) {
			r.add(check, DoctorFail, s.Command[0]+" is not on PATH",
				"install it, or give an absolute path in mcpServers.json")
			return
		}
	}

	if !o.probeMCP {
		r.add(check, DoctorOK, string(doctorMCPType(s))+", configured (not probed)", "")
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, o.mcpTimeout)
	defer cancel()
	if err := client.Connect(probeCtx); err != nil {
		r.add(check, DoctorFail, "probe failed: "+err.Error(),
			"start the server, or remove it from mcpServers.json")
		return
	}
	tools := len(client.GetTools())
	_ = client.Close()
	r.add(check, DoctorOK, fmt.Sprintf("%s, probed, %d tool(s)", doctorMCPType(s), tools), "")
}

func doctorMCPType(s mcp.ServerConfig) mcp.ServerType {
	if s.Type == "" {
		return mcp.ServerTypeStdio
	}
	return s.Type
}

func doctorCheckSkills(ctx context.Context, r *DoctorReport, cfg *config.Config) {
	paths := cfg.SkillsPaths()
	readable := make([]string, 0, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			r.add("skills.path", DoctorFail, p+": "+err.Error(), "make "+p+" readable")
		case !info.IsDir():
			r.add("skills.path", DoctorFail, p+" is a file, not a directory", "remove "+p)
		default:
			readable = append(readable, p)
		}
	}
	if len(readable) == 0 {
		r.add("skills.paths", DoctorWarn, "no skills directory found",
			"put SKILL.md files under "+cfg.SkillsDir())
		return
	}
	r.add("skills.paths", DoctorOK, strings.Join(readable, ", "), "")

	// The same loader the builder uses, so a skill the doctor cannot parse is
	// exactly a skill the agent will not see.
	skillsCfg := skills.DefaultConfig()
	skillsCfg.Paths = readable
	skillsCfg.DBPath = cfg.AgentDBPath()
	svc, err := skills.NewService(skillsCfg)
	if err != nil {
		r.add("skills.load", DoctorFail, err.Error(), "check the skills configuration")
		return
	}
	if err := svc.LoadAll(ctx); err != nil {
		r.add("skills.load", DoctorFail, err.Error(),
			"a SKILL.md failed to parse; the message names the file")
		return
	}
	loaded, err := svc.ListSkills(ctx, skills.SkillFilter{})
	if err != nil {
		r.add("skills.load", DoctorFail, err.Error(), "the skills registry is unreadable")
		return
	}
	if len(loaded) == 0 {
		r.add("skills.load", DoctorWarn, "directories exist but hold no skills",
			"a skill is a directory containing SKILL.md")
		return
	}
	r.add("skills.load", DoctorOK, fmt.Sprintf("%d skill(s) parsed", len(loaded)), "")
}

func doctorFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func doctorOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
