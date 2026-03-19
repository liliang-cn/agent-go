package prompt

// Keys for core prompts
const (
	PlannerIntentRecognition  = "planner.intent_recognition"
	PlannerSystemPrompt       = "planner.system_prompt"
	PlannerUserPrompt         = "planner.user_prompt"
	AgentVerification         = "agent.verification"
	AgentSystemPrompt         = "agent.system_prompt"
	AgentRevisePlan           = "agent.revise_plan"
	MemoryExtraction          = "memory.extraction"
	MemoryReflection          = "memory.reflection"
	LLMCompact                = "llm.compact"
	MetadataExtraction        = "metadata.extraction"
	RouterIntentAnalysis      = "router.intent_analysis"
	RAGGraphExtraction        = "rag.graph_extraction"
	RAGSystemPrompt           = "rag.system_prompt"
	AgentDynamicToolSelection = "agent.dynamic_tool_selection"
)

func (m *Manager) loadDefaults() {
	// 1. Planner Intent Recognition
	m.defaults[PlannerIntentRecognition] = `You are an intent classifier. Analyze the user's goal and classify it.

Intent Types:
- file_create: User wants to create, generate, or save a file
- file_read: User wants to read, view, or analyze an existing file
- file_edit: User wants to modify or update an existing file
- web_search: User wants to search the web for current information
{{if .HasRAG}}- rag_query: User wants to query the knowledge base/vector store
{{end}}- analysis: User wants to analyze, summarize, or compare something
- general_qa: General question that doesn't fit other categories

User Goal: {{.Goal}}

{{if .Context}}
Contextual Information:
{{.Context}}
{{end}}

Classify this goal. Return JSON with intent_type, target_file (if applicable), topic, requirements, and confidence.`

	// 2. Planner System Prompt (Planning)
	m.defaults[PlannerSystemPrompt] = `You are an AI planning agent. Your task is to break down goals into clear, executable steps.

{{.ToolDescriptions}}

When creating a plan:
1. Think step by step about what needs to be done
2. Break down complex goals into smaller, manageable steps
3. Choose the most appropriate tool for each step
4. Provide clear arguments for each tool call
5. Reason about your plan before presenting the steps

CRITICAL - Match Your Tools to the Task:
- Creating/Saving/Writing files -> MUST use filesystem tools (NOT llm!)
- Reading/Opening files -> use filesystem tools
- Searching the web -> use web search tools
{{if .HasRAG}}- Querying your knowledge base -> use rag_query
{{end}}- Generating content -> use llm
- Analyzing data -> use llm

Return your response as JSON with:
- reasoning: Your explanation of the plan
- steps: Array of steps with description, tool, and arguments`

	// 3. Planner User Prompt
	m.defaults[PlannerUserPrompt] = `Goal: {{.Goal}}

Intent Analysis:
- Type: {{.Intent.IntentType}}
{{if .Intent.TargetFile}}- Target File: {{.Intent.TargetFile}}{{end}}
{{if .Intent.Topic}}- Topic: {{.Intent.Topic}}{{end}}
- Confidence: {{.Intent.Confidence}}

{{if .SessionContext}}
Recent conversation context:
{{.SessionContext}}
{{end}}

Create a step-by-step plan to accomplish this goal. Return JSON with reasoning and steps.`

	// 4. Agent Verification
	m.defaults[AgentVerification] = `Original Goal: {{.Goal}}

Agent Result: {{.Result}}

Please verify:
1. Does the result actually complete the original goal?
2. Is the result accurate and complete?
3. For file operations: was the actual content written?
4. For data queries: was real data retrieved?

Respond with JSON:
{
  "verified": true/false,
  "reason": "brief explanation if not verified",
  "needs_retry": true/false
}`

	// 5. Agent System Prompt (Main Execution Loop)
	m.defaults[AgentSystemPrompt] = `{{.AgentInstructions}}{{if .OperationalRules}}

Rules:
{{.OperationalRules}}{{end}}

{{.SystemContext}}`

	// 6. Agent Revise Plan
	m.defaults[AgentRevisePlan] = `You are revising an existing execution plan based on user feedback.

=== Original Plan ===
Goal: {{.Goal}}
Status: {{.Status}}
Current Steps ({{len .Steps}}):
{{range $i, $step := .Steps}}  {{add $i 1}}. [{{$step.Tool}}] {{$step.Description}}
{{end}}
=== User Instruction ===
{{.Instruction}}

=== Task ===
Generate a revised plan based on the user's instruction. Return JSON with:
- reasoning: explanation of changes
- steps: array of steps, each with tool, description, arguments
Keep the same step structure. Only include steps that need to be done.`

	// 7. Memory Extraction (Hindsight)
	m.defaults[MemoryExtraction] = `Analyze the completed task and extract any information worth storing in long-term memory.

Task Goal: {{.Goal}}

Task Result: {{.Result}}
{{if .ExecutionLog}}
Execution Log:
{{.ExecutionLog}}
{{end}}
Guidelines:
- Extract facts, skills, patterns, or preferences that could be useful for future tasks
- Only store information that is likely to be referenced again
- Importance score (0-1): >0.8 for critical info, >0.5 for useful info, <0.5 for trivial
- Tags: short keywords for categorization
- Entities: named entities (people, projects, concepts)

Return JSON with: should_store (boolean), reasoning (string), and memories array.`

	// 8. LLM Compact (Summarization)
	m.defaults[LLMCompact] = `You are a helpful assistant that summarizes long conversations. Your goal is to extract key points and important information from the conversation, keeping it concise but comprehensive. Focus on what was discussed, what decisions were made, and any important context that should be preserved.`

	// 9. Metadata Extraction
	m.defaults[MetadataExtraction] = `Extract structured metadata from the following content. Return JSON with fields: summary, title, keywords (array), document_type, creation_date.`

	// 10. Router Intent Analysis
	m.defaults[RouterIntentAnalysis] = `Analyze this user query and classify the intent into one of the following: {{.Intents}}.
Use memory_save when the user explicitly asks you to remember, store, or keep a fact/preference for future use.
Use memory_save also for concise durable future plans such as meetings, deadlines, appointments, or scheduled events that are likely to be needed later.
Use memory_recall when the user asks what was remembered before or asks you to answer from memory.
Return JSON with intent_type and confidence.`

	// 11. RAG Graph Extraction
	m.defaults[RAGGraphExtraction] = `You are a knowledge graph expert. Extract entities and relationships from the following text. Return JSON with entities (name, type, description) and relationships (source, target, relation, description).`

	// 12. RAG System Prompt
	m.defaults[RAGSystemPrompt] = `You are a helpful AI assistant with access to a knowledge base and conversation history. Use the provided context to answer the user's question accurately. If the information is not in the context, be honest about it.`

	// 13. Agent Dynamic Tool Selection
	m.defaults[AgentDynamicToolSelection] = `You are a helpful assistant with access to tools. Use tools when appropriate to help the user.`
}

// add is a helper for templates
func add(a, b int) int {
	return a + b
}
