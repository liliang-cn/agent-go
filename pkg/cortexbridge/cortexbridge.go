// Package cortexbridge exposes a CortexDB knowledge-graph / RAG / memory
// database as AgentGo tools, so an agent's LLM can persist and query it via
// function-calling.
//
// CortexDB (github.com/liliang-cn/cortexdb) is a pure-Go, single-file AI memory
// and knowledge-graph library. It ships an in-process toolbox (db.GraphRAGTools)
// whose definitions and dispatch this package adapts onto an *agent.Service.
//
// AgentGo uses a CGO SQLite driver and its own database file; CortexDB uses
// pure-Go modernc.org/sqlite and its own file. They are independent and do not
// conflict — open the CortexDB handle yourself and pass it in.
//
// Basic use:
//
//	cortex, _ := cortexdb.Open(cortexdb.DefaultConfig("cortex.db"))
//	defer cortex.Close()
//
//	svc, _ := agent.New("kg-assistant").Build()
//	defer svc.Close()
//
//	cortexbridge.Register(svc, cortex) // expose the whole toolbox
//
// To expose only some tools, pass options:
//
//	cortexbridge.Register(svc, cortex, cortexbridge.WithAllow(
//	    "knowledge_graph_upsert", "knowledge_graph_query", "knowledge_graph_find",
//	))
package cortexbridge

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liliang-cn/agent-go/v2/pkg/agent"
	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// ReadOnlyTools lists the CortexDB GraphRAG tools that never mutate state.
// Tools in this set are registered ReadOnly + ConcurrencySafe so the AgentGo
// runtime may batch them and skip permission prompts.
var ReadOnlyTools = map[string]bool{
	"search_text": true, "search_chunks_by_entities": true, "search_graphrag_lexical": true,
	"expand_graph": true, "get_nodes": true, "get_chunks": true, "build_context": true,
	"knowledge_get": true, "knowledge_search": true,
	"memory_get": true, "memory_search": true,
	"knowledge_graph_find": true, "knowledge_graph_query": true, "knowledge_graph_export": true,
	"knowledge_graph_namespace_list":      true,
	"knowledge_graph_infer_summary":       true,
	"knowledge_graph_infer_explain":       true,
	"knowledge_graph_infer_explain_match": true,
	"knowledge_graph_shacl_validate":      true,
	"knowledge_memory_recall":             true,
	"knowledge_memory_build_context_pack": true,
}

// DestructiveTools lists the delete-style CortexDB tools. They are registered
// Destructive so the runtime can treat them carefully (permission prompts).
var DestructiveTools = map[string]bool{
	"knowledge_delete": true, "memory_delete": true, "knowledge_graph_delete": true,
}

// toolbox is the minimal slice of *cortexdb.DB.GraphRAGTools() this package
// needs. Declaring it as an interface keeps Register testable with a fake.
type toolbox interface {
	Definitions() []cortexdb.ToolDefinition
	Call(ctx context.Context, name string, input json.RawMessage) (any, error)
}

// toolSink is the slice of *agent.Service this package writes to. *agent.Service
// satisfies it; tests pass a fake to verify filtering/naming without an LLM.
type toolSink interface {
	AddToolWithMetadata(name, description string, parameters map[string]interface{},
		handler func(context.Context, map[string]interface{}) (interface{}, error), metadata agent.ToolMetadata)
}

// Option configures Register.
type Option func(*config)

type config struct {
	allow  map[string]bool // nil => expose all
	deny   map[string]bool
	prefix string
}

// WithAllow restricts registration to the named tools (whitelist). Calling it
// more than once unions the names.
func WithAllow(names ...string) Option {
	return func(c *config) {
		if c.allow == nil {
			c.allow = make(map[string]bool, len(names))
		}
		for _, n := range names {
			c.allow[n] = true
		}
	}
}

// WithDeny excludes the named tools (blacklist), applied after any allow-list.
func WithDeny(names ...string) Option {
	return func(c *config) {
		if c.deny == nil {
			c.deny = make(map[string]bool, len(names))
		}
		for _, n := range names {
			c.deny[n] = true
		}
	}
}

// WithNamePrefix prepends a prefix to every registered tool name (e.g. "kg_"),
// useful to avoid collisions with the agent's other tools. The original name is
// still used when dispatching to CortexDB.
func WithNamePrefix(prefix string) Option {
	return func(c *config) { c.prefix = prefix }
}

// Register adapts a CortexDB database's GraphRAG/KG/memory toolbox onto the
// AgentGo service and returns the tool names it registered (with prefix
// applied). It panics on neither nil argument; passing a nil svc or db is a
// programming error and returns an error instead.
func Register(svc *agent.Service, db *cortexdb.DB, opts ...Option) ([]string, error) {
	if svc == nil {
		return nil, fmt.Errorf("cortexbridge: nil agent service")
	}
	if db == nil {
		return nil, fmt.Errorf("cortexbridge: nil cortexdb handle")
	}
	return register(svc, db.GraphRAGTools(), opts...)
}

// register is the testable core that works against the toolbox + sink interfaces.
func register(svc toolSink, tb toolbox, opts ...Option) ([]string, error) {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	var registered []string
	for _, def := range tb.Definitions() {
		name := def.Name // capture for the closure
		if cfg.allow != nil && !cfg.allow[name] {
			continue
		}
		if cfg.deny[name] {
			continue
		}

		ro := ReadOnlyTools[name]
		meta := agent.ToolMetadata{
			ReadOnly:          ro,
			ConcurrencySafe:   ro,
			Destructive:       DestructiveTools[name],
			InterruptBehavior: agent.InterruptBehaviorCancel,
		}
		handler := func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, fmt.Errorf("cortexbridge: marshal args for %s: %w", name, err)
			}
			return tb.Call(ctx, name, json.RawMessage(raw))
		}

		toolName := cfg.prefix + name
		svc.AddToolWithMetadata(toolName, def.Description, def.InputSchema, handler, meta)
		registered = append(registered, toolName)
	}
	return registered, nil
}
