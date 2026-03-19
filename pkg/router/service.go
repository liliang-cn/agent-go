package router

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/liliang-cn/agent-go/pkg/domain"
)

// Service provides high-level semantic routing with intent-to-tool mapping
type Service struct {
	router        *Router
	embedder      domain.Embedder
	intentToTool  map[string]string // Maps intent names to tool names
	intentAliases map[string]string // Maps aliases to canonical intent names
	intents       []*Intent         // Tracked intents for listing
	mu            sync.RWMutex
}

// NewService creates a new router service
func NewService(embedder domain.Embedder, cfg *Config) (*Service, error) {
	router, err := New(embedder, cfg)
	if err != nil {
		return nil, err
	}

	return &Service{
		router:        router,
		embedder:      embedder,
		intentToTool:  make(map[string]string),
		intentAliases: make(map[string]string),
		intents:       make([]*Intent, 0),
	}, nil
}

// RouteResult extends the basic route result with tool mapping
type ServiceRouteResult struct {
	*RouteResult
	ToolName   string            `json:"tool_name,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

// Route classifies a query and returns the matched intent with tool mapping
func (s *Service) Route(ctx context.Context, query string) (*ServiceRouteResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result, err := s.router.Route(ctx, query)
	if err != nil {
		return nil, err
	}

	serviceResult := &ServiceRouteResult{
		RouteResult: result,
	}

	// Map intent to tool if matched
	if result.Matched {
		if toolName, ok := s.intentToTool[result.IntentName]; ok {
			serviceResult.ToolName = toolName
		}
		// Extract potential parameters from query
		serviceResult.Parameters = s.extractParameters(query, result.IntentName)
	}

	return serviceResult, nil
}

// RegisterIntent registers a new intent with optional tool mapping
func (s *Service) RegisterIntent(intent *Intent, toolName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.router.AddIntent(intent); err != nil {
		return err
	}

	s.intents = append(s.intents, intent)

	// Map to tool if specified
	if toolName != "" {
		s.intentToTool[intent.Name] = toolName
	}

	return nil
}

// RegisterIntentBatch registers multiple intents
func (s *Service) RegisterIntentBatch(intents []*Intent, toolMappings map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.router.AddIntentBatch(intents); err != nil {
		return err
	}

	s.intents = append(s.intents, intents...)

	// Apply tool mappings
	for intentName, toolName := range toolMappings {
		s.intentToTool[intentName] = toolName
	}

	return nil
}

// AddAlias adds an alias for an existing intent
func (s *Service) AddAlias(alias, canonicalIntent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intentAliases[strings.ToLower(alias)] = canonicalIntent
}

// MapIntentToTool maps an intent to a specific tool
func (s *Service) MapIntentToTool(intentName, toolName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intentToTool[intentName] = toolName
}

// GetToolForIntent returns the tool name mapped to an intent
func (s *Service) GetToolForIntent(intentName string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	toolName, ok := s.intentToTool[intentName]
	return toolName, ok
}

// ListIntents returns all registered intents
func (s *Service) ListIntents() []*Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Intent, len(s.intents))
	copy(result, s.intents)
	return result
}

// LoadIntentsFromDir loads intent definitions from a directory
func (s *Service) LoadIntentsFromDir(dir string) error {
	definitions, err := LoadIntentsFromDir(dir)
	if err != nil {
		return err
	}

	for _, def := range definitions {
		intent := &Intent{
			Name:       def.Name,
			Utterances: def.Utterances,
			Metadata:   def.Metadata,
		}
		if intent.Metadata == nil {
			intent.Metadata = make(map[string]string)
		}
		if def.Description != "" {
			intent.Metadata["description"] = def.Description
		}

		if err := s.RegisterIntent(intent, def.ToolMapping); err != nil {
			return fmt.Errorf("failed to register intent %s: %w", def.Name, err)
		}
	}

	return nil
}

// LoadIntentsFromPaths loads intent definitions from multiple paths
func (s *Service) LoadIntentsFromPaths(paths []string) error {
	for _, path := range paths {
		if err := s.LoadIntentsFromDir(path); err != nil {
			return err
		}
	}
	return nil
}

// RemoveIntent removes an intent by name
func (s *Service) RemoveIntent(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.router.RemoveIntent(name); err != nil {
		return err
	}

	// Remove from tracking
	for i, intent := range s.intents {
		if intent.Name == name {
			s.intents = append(s.intents[:i], s.intents[i+1:]...)
			break
		}
	}

	delete(s.intentToTool, name)

	return nil
}

// extractParameters extracts potential parameters from the query based on intent
// This is a simple implementation; can be enhanced with LLM-based extraction
func (s *Service) extractParameters(query, intentName string) map[string]string {
	params := make(map[string]string)

	// Simple keyword-based extraction for common patterns
	queryLower := strings.ToLower(query)

	switch intentName {
	case "file_create", "file_write":
		// Extract file path from quotes or after keywords
		if path := extractAfterKeywords(queryLower, []string{"create", "write", "save to", "file"}); path != "" {
			params["path"] = strings.TrimSpace(path)
		}
	case "file_read":
		if path := extractAfterKeywords(queryLower, []string{"read", "open", "file"}); path != "" {
			params["path"] = strings.TrimSpace(path)
		}
	case "web_search", "search":
		if topic := extractAfterKeywords(queryLower, []string{"search for", "find", "look up", "about"}); topic != "" {
			params["query"] = strings.TrimSpace(topic)
		}
	case "rag_query", "question", "memory_recall":
		params["query"] = strings.TrimSpace(query)
	case "memory_save":
		params["content"] = strings.TrimSpace(query)
	}

	return params
}

// extractAfterKeywords extracts text after specified keywords
func extractAfterKeywords(query string, keywords []string) string {
	for _, kw := range keywords {
		if idx := strings.Index(query, kw); idx != -1 {
			result := query[idx+len(kw):]
			// Remove quotes if present
			result = strings.Trim(result, `"\'`)
			if len(result) > 100 {
				result = result[:100] // Limit length
			}
			return result
		}
	}
	return ""
}

// RegisterDefaultIntents registers common intents for RAG/Agent applications
func (s *Service) RegisterDefaultIntents() error {
	defaultIntents := []*Intent{
		// ========== RAG / Knowledge ==========
		{
			Name: "rag_query",
			Utterances: []string{
				"what do you know about", "tell me about", "explain", "describe",
				"how does", "what is", "find information about", "search the knowledge base",
				"query documents", "lookup", "find in database", "retrieve",
			},
			Metadata: map[string]string{"category": "knowledge", "priority": "high"},
		},
		{
			Name: "rag_summary",
			Utterances: []string{
				"summarize", "give me a summary", "brief overview", "in short",
				"what's the main point", "recap", "tldr", "summary of",
			},
			Metadata: map[string]string{"category": "knowledge"},
		},
		{
			Name: "rag_compare",
			Utterances: []string{
				"compare", "difference between", "versus", "vs", "which is better",
				"contrast", "compare and contrast",
			},
			Metadata: map[string]string{"category": "knowledge"},
		},

		// ========== File Operations ==========
		{
			Name: "file_create",
			Utterances: []string{
				"create a file", "write a file", "make a new file", "generate file",
				"save to file", "create", "write", "output to file", "export to",
			},
			Metadata: map[string]string{"category": "filesystem", "priority": "high"},
		},
		{
			Name: "file_read",
			Utterances: []string{
				"read a file", "open file", "show me the file", "display file",
				"what's in the file", "read", "view file", "cat", "display content",
			},
			Metadata: map[string]string{"category": "filesystem", "priority": "high"},
		},
		{
			Name: "file_write",
			Utterances: []string{
				"write to", "save content", "append to", "overwrite",
				"update file", "modify file", "edit file",
			},
			Metadata: map[string]string{"category": "filesystem"},
		},
		{
			Name: "file_delete",
			Utterances: []string{
				"delete file", "remove file", "erase", "unlink",
				"get rid of file", "trash",
			},
			Metadata: map[string]string{"category": "filesystem"},
		},
		{
			Name: "file_move",
			Utterances: []string{
				"move file", "rename file", "relocate", "mv",
			},
			Metadata: map[string]string{"category": "filesystem"},
		},
		{
			Name: "file_copy",
			Utterances: []string{
				"copy file", "duplicate", "make a copy", "cp",
			},
			Metadata: map[string]string{"category": "filesystem"},
		},
		{
			Name: "file_list",
			Utterances: []string{
				"list files", "show files", "ls", "dir", "what files",
				"browse directory", "list directory",
			},
			Metadata: map[string]string{"category": "filesystem"},
		},
		{
			Name: "file_search",
			Utterances: []string{
				"find file", "search for file", "locate file", "where is",
				"find by name", "search files",
			},
			Metadata: map[string]string{"category": "filesystem"},
		},

		// ========== Web / Network ==========
		{
			Name: "memory_save",
			Utterances: []string{
				"remember this", "remember that", "please remember", "save to memory",
				"store this in memory", "keep this in mind", "save this for later",
				"my favorite is", "i prefer", "my preference is",
				"the meeting is tomorrow at", "kickoff is tomorrow at", "deadline is tomorrow",
				"明天下午开会", "明天启动会", "明天下午3点开会", "截止时间是明天",
				"记住这个", "记住这件事", "请记住", "帮我记住", "保存到记忆", "存到记忆",
			},
			Metadata: map[string]string{"category": "memory", "priority": "high"},
		},
		{
			Name: "memory_recall",
			Utterances: []string{
				"what did i ask you to remember", "what was i asked you to remember",
				"what do you remember about", "recall from memory", "from memory",
				"what did i tell you before", "what did i say before",
				"我之前让你记住什么", "我让你记住了什么", "你还记得什么", "从记忆里找",
			},
			Metadata: map[string]string{"category": "memory", "priority": "high"},
		},
		{
			Name: "web_search",
			Utterances: []string{
				"search the web", "search online", "find on internet", "look up",
				"google", "web search", "search for", "find information on",
				"look up online", "check online",
			},
			Metadata: map[string]string{"category": "web", "priority": "high"},
		},
		{
			Name: "web_scrape",
			Utterances: []string{
				"scrape website", "extract from webpage", "fetch page content",
				"get webpage", "download page", "crawl", "extract from url",
			},
			Metadata: map[string]string{"category": "web"},
		},
		{
			Name: "web_fetch",
			Utterances: []string{
				"fetch url", "get from url", "download from", "http get",
				"request url", "call endpoint",
			},
			Metadata: map[string]string{"category": "web"},
		},

		// ========== Code / Development ==========
		{
			Name: "code_generate",
			Utterances: []string{
				"write code", "generate code", "create function", "implement",
				"code for", "how to code", "write a program", "write script",
			},
			Metadata: map[string]string{"category": "code", "priority": "high"},
		},
		{
			Name: "code_review",
			Utterances: []string{
				"review code", "check code", "audit code", "code quality",
				"is this code good", "code feedback",
			},
			Metadata: map[string]string{"category": "code"},
		},
		{
			Name: "code_debug",
			Utterances: []string{
				"debug", "fix bug", "what's wrong", "error in code",
				"troubleshoot", "find issue",
			},
			Metadata: map[string]string{"category": "code"},
		},
		{
			Name: "code_refactor",
			Utterances: []string{
				"refactor", "improve code", "optimize", "clean up code",
				"make code better",
			},
			Metadata: map[string]string{"category": "code"},
		},
		{
			Name: "code_explain",
			Utterances: []string{
				"explain code", "what does this code do", "how this works",
				"walk through code", "understand code",
			},
			Metadata: map[string]string{"category": "code"},
		},
		{
			Name: "code_execute",
			Utterances: []string{
				"run code", "execute", "test this code", "try running",
				"execute script",
			},
			Metadata: map[string]string{"category": "code"},
		},

		// ========== Data / Analysis ==========
		{
			Name: "data_analyze",
			Utterances: []string{
				"analyze data", "data analysis", "examine data",
				"insights from", "data trends",
			},
			Metadata: map[string]string{"category": "analysis", "priority": "high"},
		},
		{
			Name: "data_format",
			Utterances: []string{
				"format data", "convert format", "transform data",
				"reformat", "change format",
			},
			Metadata: map[string]string{"category": "data"},
		},
		{
			Name: "data_extract",
			Utterances: []string{
				"extract", "parse", "pull out", "get data from",
				"extract information",
			},
			Metadata: map[string]string{"category": "data"},
		},
		{
			Name: "calculate",
			Utterances: []string{
				"calculate", "compute", "math", "how much", "count",
				"sum", "average", "total",
			},
			Metadata: map[string]string{"category": "analysis"},
		},

		// ========== System / Command ==========
		{
			Name: "command_execute",
			Utterances: []string{
				"run command", "execute command", "shell command", "terminal",
				"bash", "system command", "run script",
			},
			Metadata: map[string]string{"category": "system"},
		},
		{
			Name: "system_status",
			Utterances: []string{
				"system status", "check system", "disk space", "memory usage",
				"cpu usage", "system info", "health check",
			},
			Metadata: map[string]string{"category": "system"},
		},

		// ========== Translation / Language ==========
		{
			Name: "translate",
			Utterances: []string{
				"translate", "translation", "in chinese", "in english",
				"convert to language", "say in", "translate to",
			},
			Metadata: map[string]string{"category": "language"},
		},

		// ========== Planning / Brainstorming ==========
		{
			Name: "brainstorm",
			Utterances: []string{
				"brainstorm", "ideas for", "suggest", "give me ideas",
				"what are some options", "alternatives",
			},
			Metadata: map[string]string{"category": "creative"},
		},
		{
			Name: "plan",
			Utterances: []string{
				"plan", "how to", "steps for", "strategy for",
				"create plan", "outline",
			},
			Metadata: map[string]string{"category": "planning"},
		},

		// ========== Memory ==========
		{
			Name: "memory_save",
			Utterances: []string{
				"remember", "save to memory", "memorize", "keep in mind",
				"my favorite", "i prefer", "i like", "preference is",
				"don't forget", "store this",
			},
			Metadata: map[string]string{"category": "memory", "priority": "high"},
		},
		{
			Name: "memory_recall",
			Utterances: []string{
				"what is my favorite", "what do i prefer", "what do you remember",
				"recall", "what did i say", "do you remember", "from memory",
				"what are my preferences", "my settings",
				"favorite color", "favorite food", "my favorite",
				"what color do i like", "what food do i like",
				"remember my preference", "what did i tell you",
				"retrieve from memory", "search memory",
			},
			Metadata: map[string]string{"category": "memory", "priority": "high"},
		},

		// ========== General / Chat ==========
		{
			Name: "general_qa",
			Utterances: []string{
				"hello", "hi", "help", "what can you do", "how are you",
				"thank you", "thanks", "bye", "goodbye", "who are you",
				"what's up", "how can you help",
			},
			Metadata: map[string]string{"category": "chat", "priority": "low"},
		},
		{
			Name: "explain",
			Utterances: []string{
				"explain", "why", "how does it work", "tell me more",
				"elaborate", "go into detail", "deep dive",
			},
			Metadata: map[string]string{"category": "general", "priority": "medium"},
		},
		{
			Name: "question",
			Utterances: []string{
				"question", "ask", "i have a question", "curious",
				"wondering", "do you know",
			},
			Metadata: map[string]string{"category": "general"},
		},
	}

	// Tool mappings for intent-to-tool routing
	toolMappings := map[string]string{
		// RAG
		"rag_query":   "rag.search",
		"rag_summary": "rag.search",
		// File
		"file_create": "filesystem.write_file",
		"file_read":   "filesystem.read_file",
		"file_write":  "filesystem.write_file",
		"file_delete": "filesystem.delete",
		"file_move":   "filesystem.move",
		"file_copy":   "filesystem.copy",
		"file_list":   "filesystem.read_directory",
		// Web
		"web_search": "web-search.search",
		"web_scrape": "web-search.scrape",
		"web_fetch":  "web-search.fetch",
		// Memory
		"memory_save":   "memory.save",
		"memory_recall": "memory.recall",
	}

	return s.RegisterIntentBatch(defaultIntents, toolMappings)
}

// Close closes the service
func (s *Service) Close() error {
	return s.router.Close()
}
