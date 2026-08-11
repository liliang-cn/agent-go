package store

// MCPMemoryProfileCortexDB is the name of the shipped mapping preset for
// CortexDB's MCP memory tools (`cortexdb-mcp-stdio`, or the same tool surface
// over HTTP).
//
// It is a convenience, not a detection: you get it by writing
// `profile = "cortexdb"` in [memory.options], and nothing in this package ever
// looks at a server's tool names to decide which profile to apply. Any option
// you set yourself overrides the preset.
const MCPMemoryProfileCortexDB = "cortexdb"

func init() {
	// Verified against cortexdb-mcp-stdio 2.63.0.
	//
	//   memory_save(memory_id!, content, importance, metadata, namespace)
	//       -> {"memory": {...}}
	//   memory_search(query!, top_k, scope, session_id, user_id, namespace,
	//                 retrieval_mode)
	//       -> {"results": [{"memory": {...}, "score": 0.08}]}
	//   memory_get(memory_id!)        -> {"memory": {...}}
	//   memory_update(memory_id!, content, importance, metadata)
	//   memory_delete(memory_id!)     -> {"memory_id": "...", "deleted": true}
	//   memory_list_all(limit)        -> {"memories": [...], "truncated": bool}
	//
	// A record looks like:
	//   {"id", "session_id", "scope", "namespace", "role", "content",
	//    "metadata", "importance", "created_at"}
	//
	// `field.session` is deliberately NOT mapped. CortexDB answers with its own
	// bucket name there ("memory:global:default"), and agent-go parses
	// Memory.SessionID with its own bank grammar — copying the bucket name in
	// decodes to the nonsense scope {type:"memory", id:"global:default"}, which
	// matches nothing in any scope chain, so every retrieval silently returns
	// zero memories. The agent-go scope survives in the metadata blob instead;
	// foreign records stay global, where they are visible.
	MustRegisterMCPMemoryProfile(MCPMemoryProfileCortexDB, map[string]string{
		"tool.store":  "memory_save",
		"tool.search": "memory_search",
		"tool.get":    "memory_get",
		"tool.update": "memory_update",
		"tool.delete": "memory_delete",
		"tool.list":   "memory_list_all",

		"arg.store.id":         "memory_id",
		"arg.store.content":    "content",
		"arg.store.importance": "importance",
		"arg.store.metadata":   "metadata",

		"arg.search.query": "query",
		"arg.search.limit": "top_k",

		"arg.get.id":            "memory_id",
		"arg.update.id":         "memory_id",
		"arg.update.content":    "content",
		"arg.update.importance": "importance",
		"arg.update.metadata":   "metadata",
		"arg.delete.id":         "memory_id",
		"arg.list.limit":        "limit",

		"result.store.id":     "memory.id",
		"result.search.items": "results",
		"result.search.hit":   "memory",
		"result.search.score": "score",
		"result.get.item":     "memory",
		"result.list.items":   "memories",

		"field.id":         "id",
		"field.content":    "content",
		"field.importance": "importance",
		"field.created_at": "created_at",
		"field.metadata":   "metadata",

		"const.search.retrieval_mode": "auto",
	})
}
