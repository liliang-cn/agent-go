package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
	agentgolog "github.com/liliang-cn/agent-go/v3/pkg/log"
	"github.com/liliang-cn/agent-go/v3/pkg/mcp"
)

// MCPMemoryStoreType is the store_type name this backend registers under.
// Set `store_type = "mcp-memory"` in agentgo.toml, or
// WithMemory(WithMemoryStoreType(store.MCPMemoryStoreType)) in code.
const MCPMemoryStoreType = "mcp-memory"

const (
	defaultMCPMemoryTimeout = 30 * time.Second
	// mcpMemoryListCap bounds how much of a remote brain one List() can pull
	// when the remote list tool has no offset parameter and paging has to be
	// done client-side over a capped window.
	mcpMemoryListCap = 2000
	// defaultMCPMemoryMetadataKey is the metadata field the agent-go half of a
	// domain.Memory round-trips through. It matches the key the cortex-remote
	// gRPC backend uses, so the two backends can share one brain.
	defaultMCPMemoryMetadataKey = metadataKeyAgentGo
)

func init() {
	domain.MustRegisterMemoryStore(MCPMemoryStoreType, func(cfg domain.MemoryStoreConfig) (domain.MemoryStore, error) {
		mcpCfg, err := MCPMemoryConfigFromStoreConfig(cfg)
		if err != nil {
			return nil, err
		}
		return NewMCPMemoryStore(mcpCfg)
	})
}

// ============================================================
// Configuration
// ============================================================

// MCPMemoryConfig configures the generic MCP memory backend: which MCP server
// to talk to, and how that server's tool surface maps onto domain.MemoryStore.
//
// Nothing here is specific to any product. The mapping in Options is the whole
// contract — see MCPMemoryMapping for its grammar.
type MCPMemoryConfig struct {
	// Server describes the MCP server to connect to (stdio / http / sse).
	// Ignored when Client is set.
	Server mcp.ServerConfig

	// ClientOptions is passed verbatim to mcp.NewClient. Its Transport field is
	// the seam tests and embedders use to hand in an in-memory MCP session.
	ClientOptions *mcp.ClientOptions

	// Client, when set, is used verbatim and never closed by the store — the
	// seam for an embedder that already owns a connection to this server.
	Client *mcp.Client

	// Options is the raw option map (tool.*, arg.*, const.*, result.*, field.*,
	// profile, ...). See MCPMemoryMapping.
	Options map[string]string

	// Timeout bounds each tool call. Default 30s.
	Timeout time.Duration
}

// MCPMemoryConfigFromStoreConfig maps the generic memory-store registry config
// onto this backend.
//
// Connection, from Options unless noted:
//
//	profile      = "<name>"   a registered mapping preset; explicit options win
//	transport    = stdio|http|sse   (default: stdio when `command` is set, http
//	                                 when a URL is set)
//	command      = "<argv0>"
//	args         = "--a --b"  whitespace-split, or a JSON array
//	url          = "<url>"    (DSN is used when this is unset)
//	working_dir  = "<dir>"
//	env.<KEY>    = "<value>"  environment for a stdio server
//	env_from.<KEY> = "<VAR>"  take env <KEY> from this process's $VAR
//	header.<Name>  = "<value>" header for an http/sse server
//	token          = "<token>" -> Authorization: Bearer <token>
//	token_env      = "<VAR>"   -> same, read from $VAR (never from code)
//	server_name    = "<name>"  label used in errors/logs
//	timeout        = "30s"
//
// The DSN is used as the URL when it looks like a URL, and as the stdio command
// line otherwise.
func MCPMemoryConfigFromStoreConfig(cfg domain.MemoryStoreConfig) (MCPMemoryConfig, error) {
	opts, err := resolveMCPMemoryOptions(cfg.Options)
	if err != nil {
		return MCPMemoryConfig{}, err
	}

	get := func(key string) string { return strings.TrimSpace(opts[key]) }

	out := MCPMemoryConfig{Options: opts}

	url := firstNonBlank(get("url"), urlFromDSN(cfg.DSN))
	command := get("command")
	if command == "" && url == "" {
		command = commandFromDSN(cfg.DSN)
	}

	transport := strings.ToLower(get("transport"))
	if transport == "" {
		if url != "" {
			transport = string(mcp.ServerTypeHTTP)
		} else {
			transport = string(mcp.ServerTypeStdio)
		}
	}
	switch mcp.ServerType(transport) {
	case mcp.ServerTypeStdio, mcp.ServerTypeHTTP, mcp.ServerTypeSSE:
	default:
		return MCPMemoryConfig{}, fmt.Errorf("mcp-memory store: unsupported transport %q (want stdio, http or sse)", transport)
	}

	server := mcp.ServerConfig{
		Name:       firstNonBlank(get("server_name"), cfg.Name, MCPMemoryStoreType),
		Type:       mcp.ServerType(transport),
		URL:        url,
		WorkingDir: get("working_dir"),
		Env:        prefixedOptions(opts, "env."),
		Headers:    prefixedOptions(opts, "header."),
	}
	for key, envVar := range prefixedOptions(opts, "env_from.") {
		if v := os.Getenv(envVar); v != "" {
			if server.Env == nil {
				server.Env = map[string]string{}
			}
			server.Env[key] = v
		}
	}
	if command != "" {
		server.Command = []string{command}
	}
	if args, err := parseMCPArgs(get("args")); err != nil {
		return MCPMemoryConfig{}, err
	} else if len(args) > 0 {
		server.Args = args
	}

	// A token never comes from code: it is an explicit option or an env var.
	tokenEnv := get("token_env")
	token := get("token")
	if token == "" && tokenEnv != "" {
		token = os.Getenv(tokenEnv)
	}
	if token != "" {
		if server.Headers == nil {
			server.Headers = map[string]string{}
		}
		if _, taken := server.Headers["Authorization"]; !taken {
			server.Headers["Authorization"] = "Bearer " + token
		}
	}

	if d, err := time.ParseDuration(strings.TrimSpace(opts["timeout"])); err == nil && d > 0 {
		out.Timeout = d
		server.DefaultTimeout = d
	}

	out.Server = server
	return out, nil
}

// resolveMCPMemoryOptions expands `profile = "<name>"` into its preset options
// and then lets the caller's own options override them. A profile is always
// named explicitly — nothing about the server is ever inferred from its tool
// names.
func resolveMCPMemoryOptions(options map[string]string) (map[string]string, error) {
	merged := map[string]string{}
	if name := strings.TrimSpace(options["profile"]); name != "" {
		preset, ok := LookupMCPMemoryProfile(name)
		if !ok {
			return nil, fmt.Errorf("mcp-memory store: unknown profile %q (registered: %v)", name, RegisteredMCPMemoryProfiles())
		}
		for k, v := range preset {
			merged[k] = v
		}
	}
	for k, v := range options {
		merged[k] = v
	}
	return merged, nil
}

func prefixedOptions(options map[string]string, prefix string) map[string]string {
	var out map[string]string
	for k, v := range options {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		key := strings.TrimSpace(strings.TrimPrefix(k, prefix))
		if key == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = v
	}
	return out
}

// parseMCPArgs accepts either a JSON array or a whitespace-separated string.
func parseMCPArgs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return nil, fmt.Errorf("mcp-memory store: option \"args\" is not a JSON array: %w", err)
		}
		return args, nil
	}
	return strings.Fields(raw), nil
}

func urlFromDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if strings.HasPrefix(dsn, "http://") || strings.HasPrefix(dsn, "https://") {
		return dsn
	}
	return ""
}

func commandFromDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || urlFromDSN(dsn) != "" {
		return ""
	}
	return strings.Fields(dsn)[0]
}

// ============================================================
// Mapping
// ============================================================

// Canonical operation names. These are agent-go's vocabulary, not any server's.
const (
	mcpOpStore  = "store"
	mcpOpSearch = "search"
	mcpOpGet    = "get"
	mcpOpUpdate = "update"
	mcpOpDelete = "delete"
	mcpOpList   = "list"
)

// mcpArgOmit is the sentinel that suppresses an argument which would otherwise
// be sent under its default name: `arg.store.content = "-"`.
const mcpArgOmit = "-"

// mcpMemoryArgDefaults are the only arguments this backend will send without
// being told to. Each one is the *identity* mapping of a canonical field — the
// remote parameter is assumed to be called exactly what agent-go calls it —
// and each one is load-bearing: without it the operation has no meaning at all.
//
// Everything else (type, tags, session, scope, importance, metadata, limit,
// offset, and the id on a write) is opt-in, because sending an argument a
// server never declared is how a "generic" client stops being generic. There is
// deliberately no table of synonyms: two servers calling the same thing
// `content` and `text` is a configuration fact, not something to guess at.
var mcpMemoryArgDefaults = map[string]string{
	mcpOpStore + ".content":  "content",
	mcpOpSearch + ".query":   "query",
	mcpOpGet + ".id":         "id",
	mcpOpUpdate + ".id":      "id",
	mcpOpUpdate + ".content": "content",
	mcpOpDelete + ".id":      "id",
}

// mcpMemoryFieldDefaults are the identity mappings used when reading a record
// back. Same rule as the argument defaults: identity only, never a synonym
// list. `session`, `scope_type`, `scope_id`, `type` and `tags` have no default
// on purpose — see MCPMemoryMapping.
var mcpMemoryFieldDefaults = map[string]string{
	"id":         "id",
	"content":    "content",
	"importance": "importance",
	"created_at": "created_at",
	"metadata":   "metadata",
}

// MCPMemoryMapping is the parsed form of the option map. It is what makes this
// backend generic: every tool name, every argument name, every response field
// is configuration.
//
//	# which tool performs which operation — no defaults, no guessing
//	"tool.store"  = "memory_save"
//	"tool.search" = "memory_search"
//	"tool.get"    = "memory_get"
//	"tool.update" = "memory_update"
//	"tool.delete" = "memory_delete"
//	"tool.list"   = "memory_list_all"
//
//	# request arguments: arg.<op>.<canonical field> = <remote parameter name>
//	# canonical fields:
//	#   store  -> id content type tags session scope importance metadata
//	#   search -> query limit
//	#   get    -> id
//	#   update -> id content importance metadata
//	#   delete -> id
//	#   list   -> limit offset
//	# "-" suppresses an argument that would otherwise be sent by default.
//	"arg.store.content"  = "content"
//	"arg.store.id"       = "memory_id"
//	"arg.search.limit"   = "top_k"
//
//	# constant arguments always sent with an operation
//	"const.search.retrieval_mode" = "auto"
//	"const.store.namespace"       = "default"
//
//	# where the payload lives in the tool's JSON result (dot paths; "" = root)
//	"result.search.items" = "results"   # the array of hits
//	"result.search.hit"   = "memory"    # the record inside one hit
//	"result.search.score" = "score"     # the score inside one hit
//	"result.list.items"   = "memories"
//	"result.get.item"     = "memory"
//	"result.store.id"     = "memory.id" # id assigned by the server
//
//	# record field names: field.<canonical> = <remote field name>
//	# canonical: id content type tags session scope_type scope_id importance
//	#            created_at metadata
//	"field.id"      = "id"
//	"field.content" = "content"
//
//	# where the agent-go half of a memory round-trips (default "agentgo")
//	"metadata_key" = "agentgo"
//
// `field.session` / `field.scope_type` / `field.scope_id` have no default and
// should only be set when the remote field really holds an agent-go session id
// or scope. A server that answers with its own bucket name (CortexDB replies
// `session_id: "memory:global:default"`) must NOT have it mapped: agent-go
// parses that field with its own bank grammar, the memory then matches no scope
// in the chain, and RetrieveAndInject silently injects nothing. Leaving them
// unmapped keeps foreign records at global scope, where the retrieval filter
// can see them.
type MCPMemoryMapping struct {
	tools       map[string]string
	args        map[string]string
	consts      map[string]map[string]any
	results     map[string]string
	fields      map[string]string
	metadataKey string
}

// ParseMCPMemoryMapping builds a mapping from a raw option map, expanding
// `profile` first.
func ParseMCPMemoryMapping(options map[string]string) (MCPMemoryMapping, error) {
	opts, err := resolveMCPMemoryOptions(options)
	if err != nil {
		return MCPMemoryMapping{}, err
	}

	m := MCPMemoryMapping{
		tools:       map[string]string{},
		args:        map[string]string{},
		consts:      map[string]map[string]any{},
		results:     map[string]string{},
		fields:      map[string]string{},
		metadataKey: defaultMCPMemoryMetadataKey,
	}

	for key, value := range opts {
		key = strings.TrimSpace(key)
		switch {
		case strings.HasPrefix(key, "tool."):
			m.tools[strings.TrimPrefix(key, "tool.")] = strings.TrimSpace(value)
		case strings.HasPrefix(key, "arg."):
			m.args[strings.TrimPrefix(key, "arg.")] = strings.TrimSpace(value)
		case strings.HasPrefix(key, "const."):
			rest := strings.TrimPrefix(key, "const.")
			op, param, ok := strings.Cut(rest, ".")
			if !ok || strings.TrimSpace(param) == "" {
				return MCPMemoryMapping{}, fmt.Errorf("mcp-memory store: option %q must be const.<op>.<param>", key)
			}
			if m.consts[op] == nil {
				m.consts[op] = map[string]any{}
			}
			m.consts[op][param] = decodeConstValue(value)
		case strings.HasPrefix(key, "result."):
			m.results[strings.TrimPrefix(key, "result.")] = strings.TrimSpace(value)
		case strings.HasPrefix(key, "field."):
			m.fields[strings.TrimPrefix(key, "field.")] = strings.TrimSpace(value)
		case key == "metadata_key":
			if v := strings.TrimSpace(value); v != "" {
				m.metadataKey = v
			}
		}
	}
	return m, nil
}

// decodeConstValue keeps a constant argument's JSON type when the option value
// is valid JSON, so `const.search.top_k = "20"` sends a number and
// `const.store.tags = "[\"a\"]"` sends an array.
func decodeConstValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw
	}
	switch trimmed[0] {
	case '{', '[', 't', 'f', 'n', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			return v
		}
	}
	return raw
}

// Tool returns the tool name configured for an operation.
func (m MCPMemoryMapping) Tool(op string) (string, bool) {
	name := strings.TrimSpace(m.tools[op])
	return name, name != ""
}

// arg resolves the remote parameter name for op.field, or "" when the argument
// must not be sent.
func (m MCPMemoryMapping) arg(op, field string) string {
	key := op + "." + field
	if v, ok := m.args[key]; ok {
		if v == mcpArgOmit || v == "" {
			return ""
		}
		return v
	}
	return mcpMemoryArgDefaults[key]
}

// field resolves the remote record field name for a canonical field, or "" when
// this backend must not read it.
func (m MCPMemoryMapping) field(name string) string {
	if v, ok := m.fields[name]; ok {
		if v == mcpArgOmit {
			return ""
		}
		return v
	}
	return mcpMemoryFieldDefaults[name]
}

func (m MCPMemoryMapping) result(key string) string { return m.results[key] }

// ============================================================
// Store
// ============================================================

// MCPMemoryStore is a domain.MemoryStore backed by any MCP server that exposes
// memory tools. It owns its own MCP client connection, so it does not depend on
// the agent's MCP service being assembled first.
//
// What it covers, and what it honestly does not:
//
//	Store / StoreWithScope  -> tool.store
//	SearchByText            -> tool.search
//	Get                     -> tool.get
//	Update                  -> tool.update
//	Delete                  -> tool.delete
//	List                    -> tool.list
//	InitSchema              -> no-op; the remote server owns its schema
//
//	Search / SearchBySession / SearchByScope
//	    DEGRADED. These take a []float64 the MCP tool surface has no place for:
//	    an MCP memory server takes a query *string* and owns its own embedding
//	    model. They return no results (not an error) so that memory.Service
//	    falls through to SearchByText, which is the route that actually reaches
//	    the server. Returning an error here instead is what makes automatic
//	    memory injection go quiet.
//
//	IncrementAccess, GetByType, Clear, DeleteBySession, ConfigureBank,
//	Reflect, AddMentalModel
//	    UNSUPPORTED — domain.ErrMemoryStoreUnsupported. There is no portable MCP
//	    tool for any of them, and inventing one per server is exactly the
//	    guessing this backend refuses to do.
//
// Any operation whose `tool.<op>` is unconfigured also returns
// ErrMemoryStoreUnsupported: an honest gap beats a fake implementation.
type MCPMemoryStore struct {
	cfg     MCPMemoryConfig
	mapping MCPMemoryMapping

	mu     sync.Mutex
	client *mcp.Client
	owned  bool
	closed bool

	warnVector sync.Once
}

var _ domain.MemoryStore = (*MCPMemoryStore)(nil)

// NewMCPMemoryStore builds the store. It does not connect: the MCP session is
// established lazily on the first operation, so constructing an agent never
// depends on the memory server being up, and a server that comes back later
// starts working without a restart.
func NewMCPMemoryStore(cfg MCPMemoryConfig) (*MCPMemoryStore, error) {
	mapping, err := ParseMCPMemoryMapping(cfg.Options)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultMCPMemoryTimeout
	}
	if cfg.Client == nil {
		hasTransport := cfg.ClientOptions != nil && cfg.ClientOptions.Transport != nil
		if !hasTransport && len(cfg.Server.Command) == 0 && cfg.Server.URL == "" {
			return nil, errors.New("mcp-memory store: no MCP server configured (set the memory DSN, or the \"command\" / \"url\" option)")
		}
	}
	if len(mapping.tools) == 0 {
		return nil, errors.New("mcp-memory store: no tool mapping configured (set tool.store / tool.search / ... or profile = \"<name>\")")
	}
	return &MCPMemoryStore{cfg: cfg, mapping: mapping}, nil
}

// Mapping exposes the resolved mapping, for diagnostics.
func (s *MCPMemoryStore) Mapping() MCPMemoryMapping { return s.mapping }

// Close tears down the MCP session when this store opened it.
//
// memory.Service.Close() does NOT cascade to its store, so an embedder that
// wants deterministic cleanup should keep the store reference it passed to
// WithMemoryStore and close it. A stdio server started here is bound to the
// client's own context and dies with the process either way.
func (s *MCPMemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.client != nil && s.owned {
		err := s.client.Close()
		s.client = nil
		return err
	}
	s.client = nil
	return nil
}

// connect returns a connected MCP client, dialling on first use. A failed
// connection is not cached, so a later call retries.
func (s *MCPMemoryStore) connect(ctx context.Context) (*mcp.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("mcp-memory store: closed")
	}
	if s.cfg.Client != nil {
		return s.cfg.Client, nil
	}
	if s.client != nil && s.client.IsConnected() {
		return s.client, nil
	}
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}

	serverCfg := s.cfg.Server
	client, err := mcp.NewClient(&serverCfg, s.cfg.ClientOptions)
	if err != nil {
		return nil, fmt.Errorf("mcp-memory store: %w", err)
	}
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("mcp-memory store: connect to %q: %w", serverCfg.Name, err)
	}
	s.client = client
	s.owned = true
	return client, nil
}

// call runs one mapped tool and decodes its JSON result.
func (s *MCPMemoryStore) call(ctx context.Context, op string, args map[string]any) (any, error) {
	tool, ok := s.mapping.Tool(op)
	if !ok {
		return nil, fmt.Errorf("mcp-memory store: %s: %w", op, domain.ErrMemoryStoreUnsupported)
	}
	for k, v := range s.mapping.consts[op] {
		if _, taken := args[k]; !taken {
			args[k] = v
		}
	}

	client, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	res, err := client.CallTool(callCtx, tool, args)
	if err != nil {
		return nil, fmt.Errorf("mcp-memory store: %s: %w", tool, err)
	}
	if res == nil {
		return nil, fmt.Errorf("mcp-memory store: %s: empty result", tool)
	}
	if !res.Success {
		return nil, fmt.Errorf("mcp-memory store: %s: %s", tool, res.Error)
	}
	// A tool that reports its own failure must not be mistaken for a payload:
	// the message text would otherwise be decoded as a memory id or a record.
	if res.IsError {
		return nil, fmt.Errorf("mcp-memory store: %s: %s", tool, stringValue(res.Data))
	}
	return decodeMCPToolPayload(tool, res.Data)
}

// decodeMCPToolPayload turns an MCP tool result into a JSON value. MCP tools
// answer with text content, and a memory server's text is JSON. Text that is
// not JSON is an error rather than a value: it is almost always a human-facing
// message, and quietly feeding it onward is how an error string ends up stored
// as a memory id.
func decodeMCPToolPayload(tool string, data any) (any, error) {
	switch v := data.(type) {
	case nil:
		return nil, nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, nil
		}
		var out any
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return nil, fmt.Errorf("mcp-memory store: %s: expected a JSON result, got %q", tool, truncateForError(trimmed))
		}
		return out, nil
	default:
		return v, nil
	}
}

func truncateForError(s string) string {
	const max = 160
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ============================================================
// Writes
// ============================================================

func (s *MCPMemoryStore) Store(ctx context.Context, memory *domain.Memory) error {
	if memory == nil {
		return errors.New("mcp-memory store: memory is nil")
	}

	args := map[string]any{}
	if name := s.mapping.arg(mcpOpStore, "content"); name != "" {
		args[name] = memory.Content
	}
	if name := s.mapping.arg(mcpOpStore, "id"); name != "" {
		if memory.ID == "" {
			memory.ID = uuid.NewString()
		}
		args[name] = memory.ID
	}
	if name := s.mapping.arg(mcpOpStore, "type"); name != "" && memory.Type != "" {
		args[name] = string(memory.Type)
	}
	if name := s.mapping.arg(mcpOpStore, "tags"); name != "" && len(memory.Tags) > 0 {
		args[name] = memory.Tags
	}
	if name := s.mapping.arg(mcpOpStore, "session"); name != "" && memory.SessionID != "" {
		args[name] = memory.SessionID
	}
	if name := s.mapping.arg(mcpOpStore, "scope"); name != "" && memory.ScopeType != "" {
		args[name] = string(memory.ScopeType)
	}
	if name := s.mapping.arg(mcpOpStore, "importance"); name != "" {
		args[name] = memory.Importance
	}
	if name := s.mapping.arg(mcpOpStore, "metadata"); name != "" {
		meta, err := s.encodeMetadata(memory)
		if err != nil {
			return err
		}
		args[name] = meta
	}

	payload, err := s.call(ctx, mcpOpStore, args)
	if err != nil {
		return err
	}
	if id := s.storedID(payload); id != "" {
		memory.ID = id
	}
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = time.Now()
	}
	return nil
}

// storedID pulls the id the server assigned out of the store result.
func (s *MCPMemoryStore) storedID(payload any) string {
	if path := s.mapping.result("store.id"); path != "" {
		return stringValue(resolveJSONPath(payload, path))
	}
	switch v := payload.(type) {
	case string:
		return v
	case map[string]any:
		if field := s.mapping.field("id"); field != "" {
			return stringValue(v[field])
		}
	}
	return ""
}

func (s *MCPMemoryStore) StoreWithScope(ctx context.Context, memory *domain.Memory, scope domain.MemoryScope) error {
	if memory == nil {
		return errors.New("mcp-memory store: memory is nil")
	}
	clone := *memory
	clone.ScopeType = scope.Type
	clone.ScopeID = scope.ID
	if scope.Type == domain.MemoryScopeSession && scope.ID != "" {
		clone.SessionID = scope.ID
	}
	if err := s.Store(ctx, &clone); err != nil {
		return err
	}
	memory.ID = clone.ID
	memory.CreatedAt = clone.CreatedAt
	return nil
}

func (s *MCPMemoryStore) Update(ctx context.Context, memory *domain.Memory) error {
	if memory == nil || memory.ID == "" {
		return errors.New("mcp-memory store: update needs a memory with an ID")
	}
	args := map[string]any{}
	if name := s.mapping.arg(mcpOpUpdate, "id"); name != "" {
		args[name] = memory.ID
	}
	if name := s.mapping.arg(mcpOpUpdate, "content"); name != "" {
		args[name] = memory.Content
	}
	if name := s.mapping.arg(mcpOpUpdate, "importance"); name != "" {
		args[name] = memory.Importance
	}
	if name := s.mapping.arg(mcpOpUpdate, "metadata"); name != "" {
		meta, err := s.encodeMetadata(memory)
		if err != nil {
			return err
		}
		args[name] = meta
	}
	if _, err := s.call(ctx, mcpOpUpdate, args); err != nil {
		return err
	}
	memory.UpdatedAt = time.Now()
	return nil
}

func (s *MCPMemoryStore) Delete(ctx context.Context, id string) error {
	args := map[string]any{}
	if name := s.mapping.arg(mcpOpDelete, "id"); name != "" {
		args[name] = id
	}
	_, err := s.call(ctx, mcpOpDelete, args)
	return err
}

// ============================================================
// Reads
// ============================================================

func (s *MCPMemoryStore) Get(ctx context.Context, id string) (*domain.Memory, error) {
	args := map[string]any{}
	if name := s.mapping.arg(mcpOpGet, "id"); name != "" {
		args[name] = id
	}
	payload, err := s.call(ctx, mcpOpGet, args)
	if err != nil {
		return nil, err
	}
	record := payload
	if path := s.mapping.result("get.item"); path != "" {
		record = resolveJSONPath(payload, path)
	}
	obj, ok := record.(map[string]any)
	if !ok {
		return nil, ErrMemoryNotFound
	}
	mem := s.memoryFromRecord(obj)
	if mem == nil || (mem.ID == "" && mem.Content == "") {
		return nil, ErrMemoryNotFound
	}
	if mem.ID == "" {
		mem.ID = id
	}
	return mem, nil
}

// SearchByText is the retrieval path that actually reaches an MCP memory
// server: it takes the query string the server's own retrieval expects.
func (s *MCPMemoryStore) SearchByText(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = 10
	}
	args := map[string]any{}
	if name := s.mapping.arg(mcpOpSearch, "query"); name != "" {
		args[name] = query
	}
	if name := s.mapping.arg(mcpOpSearch, "limit"); name != "" {
		args[name] = topK
	}

	payload, err := s.call(ctx, mcpOpSearch, args)
	if err != nil {
		return nil, err
	}

	items := arrayValue(payload, s.mapping.result("search.items"))
	hitPath := s.mapping.result("search.hit")
	scorePath := s.mapping.result("search.score")
	if scorePath == "" {
		scorePath = "score"
	}

	out := make([]*domain.MemoryWithScore, 0, len(items))
	for _, item := range items {
		record := item
		score := 0.0
		if hitPath != "" {
			record = resolveJSONPath(item, hitPath)
			score = floatValue(resolveJSONPath(item, scorePath))
		} else if obj, ok := item.(map[string]any); ok {
			score = floatValue(resolveJSONPath(obj, scorePath))
		}
		obj, ok := record.(map[string]any)
		if !ok {
			continue
		}
		mem := s.memoryFromRecord(obj)
		if mem == nil {
			continue
		}
		out = append(out, &domain.MemoryWithScore{Memory: mem, Score: score})
		if len(out) >= topK {
			break
		}
	}
	return out, nil
}

// List pages over the remote list tool. When the tool has no offset parameter
// (`arg.list.offset` unmapped) the window is fetched whole, capped at
// mcpMemoryListCap, and sliced client-side.
func (s *MCPMemoryStore) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	args := map[string]any{}
	offsetArg := s.mapping.arg(mcpOpList, "offset")
	clientSideOffset := offsetArg == ""
	if offsetArg != "" {
		args[offsetArg] = offset
	}
	if name := s.mapping.arg(mcpOpList, "limit"); name != "" {
		want := limit
		if clientSideOffset {
			want = offset + limit
			if want > mcpMemoryListCap {
				want = mcpMemoryListCap
			}
		}
		args[name] = want
	}

	payload, err := s.call(ctx, mcpOpList, args)
	if err != nil {
		return nil, 0, err
	}

	items := arrayValue(payload, s.mapping.result("list.items"))
	all := make([]*domain.Memory, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if mem := s.memoryFromRecord(obj); mem != nil {
			all = append(all, mem)
		}
	}

	total := len(all)
	if !clientSideOffset {
		if len(all) > limit {
			all = all[:limit]
		}
		return all, total, nil
	}
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}

// ============================================================
// Degraded: vector search
// ============================================================

// Search cannot be honoured: an MCP memory tool takes a query string, and the
// server owns the embedding model, so a locally computed vector means nothing
// there. It returns no results rather than an error so memory.Service degrades
// to SearchByText instead of failing the whole retrieval — the difference
// between "the agent falls back" and "the agent silently remembers nothing".
func (s *MCPMemoryStore) Search(ctx context.Context, vector []float64, topK int, minScore float64) ([]*domain.MemoryWithScore, error) {
	s.warnVectorSearch()
	return nil, nil
}

// SearchBySession has the same limitation as Search.
func (s *MCPMemoryStore) SearchBySession(ctx context.Context, sessionID string, vector []float64, topK int) ([]*domain.MemoryWithScore, error) {
	s.warnVectorSearch()
	return nil, nil
}

// SearchByScope has the same limitation as Search.
func (s *MCPMemoryStore) SearchByScope(ctx context.Context, vector []float64, scopes []domain.MemoryScope, topK int) ([]*domain.MemoryWithScore, error) {
	s.warnVectorSearch()
	return nil, nil
}

func (s *MCPMemoryStore) warnVectorSearch() {
	s.warnVector.Do(func() {
		agentgolog.Warn("mcp-memory store: vector search is served by the MCP server, not by a client-supplied vector; retrieval falls back to SearchByText")
	})
}

// ============================================================
// Unsupported
// ============================================================

// InitSchema is a no-op: the MCP server owns its own storage.
func (s *MCPMemoryStore) InitSchema(ctx context.Context) error { return nil }

// IncrementAccess is unsupported: there is no portable MCP access counter.
func (s *MCPMemoryStore) IncrementAccess(ctx context.Context, id string) error {
	return domain.ErrMemoryStoreUnsupported
}

// GetByType is unsupported: a type filter would have to be invented per server,
// and scanning a whole remote brain per call is not a substitute.
func (s *MCPMemoryStore) GetByType(ctx context.Context, memoryType domain.MemoryType, limit int) ([]*domain.Memory, error) {
	return nil, domain.ErrMemoryStoreUnsupported
}

// Clear is unsupported: no portable bulk delete, and wiping a brain that may be
// shared with other agents is not something this store offers.
func (s *MCPMemoryStore) Clear(ctx context.Context) error {
	return domain.ErrMemoryStoreUnsupported
}

// DeleteBySession is unsupported: no portable bulk delete.
func (s *MCPMemoryStore) DeleteBySession(ctx context.Context, sessionID string) error {
	return domain.ErrMemoryStoreUnsupported
}

// ConfigureBank is unsupported: memory-bank disposition is a local-store concept.
func (s *MCPMemoryStore) ConfigureBank(ctx context.Context, sessionID string, config *domain.MemoryBankConfig) error {
	return domain.ErrMemoryStoreUnsupported
}

// Reflect is unsupported: consolidation is the server's business, and no two
// memory servers agree on what a "reflect" tool means.
func (s *MCPMemoryStore) Reflect(ctx context.Context, sessionID string) (string, error) {
	return "", domain.ErrMemoryStoreUnsupported
}

// AddMentalModel is unsupported: no portable MCP concept.
func (s *MCPMemoryStore) AddMentalModel(ctx context.Context, model *domain.MentalModel) error {
	return domain.ErrMemoryStoreUnsupported
}

// ============================================================
// Record mapping
// ============================================================

// encodeMetadata builds the metadata argument: the agent-go half of the memory
// as a JSON blob under metadata_key, plus the few human-readable mirrors other
// clients of the same brain are likely to read.
func (s *MCPMemoryStore) encodeMetadata(m *domain.Memory) (map[string]any, error) {
	extras := agentGoMemoryExtras{
		Type:       m.Type,
		ScopeType:  m.ScopeType,
		ScopeID:    m.ScopeID,
		SessionID:  m.SessionID,
		Keywords:   m.Keywords,
		Tags:       m.Tags,
		SourceType: m.SourceType,
		Confidence: m.Confidence,
		Metadata:   m.Metadata,
	}
	blob, err := json.Marshal(extras)
	if err != nil {
		return nil, fmt.Errorf("mcp-memory store: encode memory metadata: %w", err)
	}
	meta := map[string]any{
		s.mapping.metadataKey: string(blob),
		"source":              "agent-go",
	}
	if m.Type != "" {
		meta["kind"] = string(m.Type)
	}
	if len(m.Tags) > 0 {
		meta["tags"] = strings.Join(m.Tags, ",")
	}
	return meta, nil
}

// memoryFromRecord decodes one remote record using the configured field names.
//
// Session and scope are only ever taken from our own round-trip blob or from an
// explicitly mapped field. A record written by some other client of the same
// server therefore comes back at global scope — visible to every scope chain —
// instead of carrying a foreign string into domain.Memory.SessionID, which
// agent-go would parse as a bank id and then filter the memory out of every
// retrieval.
func (s *MCPMemoryStore) memoryFromRecord(rec map[string]any) *domain.Memory {
	if rec == nil {
		return nil
	}
	m := &domain.Memory{Type: domain.MemoryTypeFact}

	if f := s.mapping.field("id"); f != "" {
		m.ID = stringValue(resolveJSONPath(rec, f))
	}
	if f := s.mapping.field("content"); f != "" {
		m.Content = stringValue(resolveJSONPath(rec, f))
	}
	if f := s.mapping.field("importance"); f != "" {
		m.Importance = floatValue(resolveJSONPath(rec, f))
	}
	if f := s.mapping.field("type"); f != "" {
		if v := stringValue(resolveJSONPath(rec, f)); v != "" {
			m.Type = domain.MemoryType(v)
		}
	}
	if f := s.mapping.field("tags"); f != "" {
		m.Tags = stringSliceValue(resolveJSONPath(rec, f))
	}
	if f := s.mapping.field("session"); f != "" {
		m.SessionID = stringValue(resolveJSONPath(rec, f))
	}
	if f := s.mapping.field("scope_type"); f != "" {
		if v := stringValue(resolveJSONPath(rec, f)); v != "" {
			m.ScopeType = domain.MemoryScopeType(v)
		}
	}
	if f := s.mapping.field("scope_id"); f != "" {
		m.ScopeID = stringValue(resolveJSONPath(rec, f))
	}
	if f := s.mapping.field("created_at"); f != "" {
		if ts := timeValue(resolveJSONPath(rec, f)); !ts.IsZero() {
			m.CreatedAt = ts
			m.UpdatedAt = ts
		}
	}
	if f := s.mapping.field("metadata"); f != "" {
		if meta, ok := resolveJSONPath(rec, f).(map[string]any); ok {
			s.applyMetadata(m, meta)
		}
	}

	if m.ID == "" && m.Content == "" {
		return nil
	}
	return m
}

// applyMetadata restores the agent-go half of a memory from the round-trip
// blob. The blob is accepted both as a JSON string (what the cortex-remote gRPC
// backend writes, so the two share a brain) and as a nested object (what a
// server that keeps structured metadata may hand back).
func (s *MCPMemoryStore) applyMetadata(m *domain.Memory, meta map[string]any) {
	if imp, ok := meta["importance"].(float64); ok && m.Importance == 0 {
		m.Importance = imp
	}

	raw, present := meta[s.mapping.metadataKey]
	if !present {
		if kind, ok := meta["kind"].(string); ok && kind != "" && kind != "memory" {
			m.Type = domain.MemoryType(kind)
		}
		return
	}

	var blob []byte
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return
		}
		blob = []byte(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return
		}
		blob = encoded
	}

	var extras agentGoMemoryExtras
	if err := json.Unmarshal(blob, &extras); err != nil {
		return
	}
	if extras.Type != "" {
		m.Type = extras.Type
	}
	m.ScopeType = extras.ScopeType
	m.ScopeID = extras.ScopeID
	m.SessionID = extras.SessionID
	m.Keywords = extras.Keywords
	if len(extras.Tags) > 0 {
		m.Tags = extras.Tags
	}
	m.SourceType = extras.SourceType
	m.Confidence = extras.Confidence
	m.Metadata = extras.Metadata
}

// ============================================================
// JSON helpers
// ============================================================

// resolveJSONPath walks a dot-separated path. An empty path is the value
// itself, so `result.get.item = ""` means "the result is the record".
func resolveJSONPath(value any, path string) any {
	path = strings.TrimSpace(path)
	if path == "" {
		return value
	}
	current := value
	for _, part := range strings.Split(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = obj[part]
		if !ok {
			return nil
		}
	}
	return current
}

// arrayValue resolves a path and returns it as a slice. A payload that is
// already an array is returned as-is when the path is empty.
func arrayValue(payload any, path string) []any {
	v := resolveJSONPath(payload, path)
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

func stringValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func floatValue(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return 0
}

func stringSliceValue(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s := stringValue(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		parts := strings.Split(t, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

func timeValue(v any) time.Time {
	switch t := v.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if ts, err := time.Parse(layout, strings.TrimSpace(t)); err == nil {
				return ts
			}
		}
	case float64:
		if t > 1e12 {
			return time.UnixMilli(int64(t))
		}
		if t > 0 {
			return time.Unix(int64(t), 0)
		}
	}
	return time.Time{}
}

// ============================================================
// Profiles
// ============================================================

var mcpMemoryProfiles = struct {
	sync.RWMutex
	presets map[string]map[string]string
}{presets: map[string]map[string]string{}}

// RegisterMCPMemoryProfile registers a named preset of mcp-memory options, so a
// consumer can write `profile = "<name>"` instead of two dozen mapping lines.
//
// A profile is a *convenience*, never a detection: it is selected by name in
// configuration and nothing about the server is ever sniffed. Registering a
// duplicate name is an error rather than a silent overwrite.
func RegisterMCPMemoryProfile(name string, options map[string]string) error {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return errors.New("register mcp-memory profile: name is required")
	}
	if len(options) == 0 {
		return fmt.Errorf("register mcp-memory profile %q: no options", name)
	}
	mcpMemoryProfiles.Lock()
	defer mcpMemoryProfiles.Unlock()
	if _, exists := mcpMemoryProfiles.presets[key]; exists {
		return fmt.Errorf("register mcp-memory profile %q: already registered", name)
	}
	preset := make(map[string]string, len(options))
	for k, v := range options {
		preset[k] = v
	}
	mcpMemoryProfiles.presets[key] = preset
	return nil
}

// MustRegisterMCPMemoryProfile is RegisterMCPMemoryProfile for package init().
func MustRegisterMCPMemoryProfile(name string, options map[string]string) {
	if err := RegisterMCPMemoryProfile(name, options); err != nil {
		panic(err)
	}
}

// UnregisterMCPMemoryProfile removes a profile. It reports whether one existed.
func UnregisterMCPMemoryProfile(name string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	mcpMemoryProfiles.Lock()
	defer mcpMemoryProfiles.Unlock()
	_, existed := mcpMemoryProfiles.presets[key]
	delete(mcpMemoryProfiles.presets, key)
	return existed
}

// LookupMCPMemoryProfile returns a copy of a registered profile.
func LookupMCPMemoryProfile(name string) (map[string]string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	mcpMemoryProfiles.RLock()
	defer mcpMemoryProfiles.RUnlock()
	preset, ok := mcpMemoryProfiles.presets[key]
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(preset))
	for k, v := range preset {
		out[k] = v
	}
	return out, true
}

// RegisteredMCPMemoryProfiles lists the registered profile names, sorted.
func RegisteredMCPMemoryProfiles() []string {
	mcpMemoryProfiles.RLock()
	defer mcpMemoryProfiles.RUnlock()
	names := make([]string, 0, len(mcpMemoryProfiles.presets))
	for name := range mcpMemoryProfiles.presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
