package resource

type Kind string

const (
	KindLLM     Kind = "llm"
	KindTool    Kind = "tool"
	KindMCP     Kind = "mcp"
	KindSkill   Kind = "skill"
	KindMemory  Kind = "memory"
	KindRAG     Kind = "rag"
	KindPTC     Kind = "ptc"
	KindStorage Kind = "storage"
)

type ExecutionMode string

const (
	ExecutionDual       ExecutionMode = "dual"
	ExecutionDirectOnly ExecutionMode = "direct_only"
	ExecutionCodeOnly   ExecutionMode = "code_only"
	ExecutionDisabled   ExecutionMode = "disabled"
)

type Resource struct {
	ID          string         `json:"id"`
	Kind        Kind           `json:"kind"`
	Name        string         `json:"name"`
	Provider    string         `json:"provider,omitempty"`
	Description string         `json:"description,omitempty"`
	Execution   ExecutionMode  `json:"execution,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}
