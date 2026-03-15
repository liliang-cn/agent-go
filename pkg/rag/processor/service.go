package processor

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pdf "github.com/dslipak/pdf"
	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/pkg/config"
	"github.com/liliang-cn/agent-go/pkg/domain"
	"github.com/liliang-cn/agent-go/pkg/prompt"
	"github.com/liliang-cn/agent-go/pkg/rag/graphrag"
	"github.com/liliang-cn/agent-go/pkg/store"
)

type Service struct {
	embedder      domain.Embedder
	generator     domain.Generator
	chunker       domain.Chunker
	vectorStore   domain.VectorStore
	documentStore domain.DocumentStore
	config        *config.Config
	llmService    domain.MetadataExtractor
	extractor     *EntityExtractor
	graphStore    domain.GraphStore
	chatStore     domain.ChatStore
	agentgoDB     *store.AgentGoDB
	memoryService domain.MemoryService
	promptManager *prompt.Manager
	graphRAG      *graphrag.Service
}

func New(
	embedder domain.Embedder,
	generator domain.Generator,
	chunker domain.Chunker,
	vectorStore domain.VectorStore,
	documentStore domain.DocumentStore,
	config *config.Config,
	llmService domain.MetadataExtractor,
	memoryService domain.MemoryService,
) *Service {
	s := &Service{
		embedder:      embedder,
		generator:     generator,
		chunker:       chunker,
		vectorStore:   vectorStore,
		documentStore: documentStore,
		config:        config,
		llmService:    llmService,
		memoryService: memoryService,
		promptManager: prompt.NewManager(),
	}

	// Initialize unified AgentGoDB from config if available
	if config != nil {
		if adb, err := store.NewAgentGoDB(config.AgentDBPath()); err == nil {
			s.agentgoDB = adb
		}
	}

	// Initialize entity extractor (only if generator is available)
	if generator != nil {
		s.extractor = NewEntityExtractor(generator)
		s.extractor.SetPromptManager(s.promptManager)
	}

	// Get graph store if available
	if vectorStore != nil {
		s.graphStore = vectorStore.GetGraphStore()
		s.chatStore = vectorStore.GetChatStore()
	}

	return s
}

func (s *Service) SetPromptManager(m *prompt.Manager) {
	s.promptManager = m
	if s.extractor != nil {
		s.extractor.SetPromptManager(m)
	}
}

// initializeTools sets up the tool system

func (s *Service) Ingest(ctx context.Context, req domain.IngestRequest) (domain.IngestResponse, error) {
	if err := s.validateIngestRequest(req); err != nil {
		return domain.IngestResponse{}, err
	}

	content, err := s.extractContent(req)
	if err != nil {
		return domain.IngestResponse{}, err
	}

	if content == "" {
		return domain.IngestResponse{
				Success: false,
				Message: "no content found",
			},
			nil
	}

	// Initialize metadata map if it's nil
	if req.Metadata == nil {
		req.Metadata = make(map[string]interface{})
	}

	// Automatic metadata extraction
	/*
		if s.config.Ingest.MetadataExtraction.Enable && s.llmService != nil {
			log.Println("Enhanced metadata extraction enabled, calling LLM...")
			// Use default model from pool
			extracted, err := s.llmService.ExtractMetadata(ctx, content, "")
			if err != nil {
				log.Printf("Warning: metadata extraction failed, proceeding without it. Error: %v", err)
			} else {
				log.Printf("Successfully extracted metadata: %+v", extracted)
				s.mergeMetadata(req.Metadata, extracted)
			}
		}
	*/

	// Fallback for creation_date
	if _, ok := req.Metadata["creation_date"]; !ok || req.Metadata["creation_date"] == nil || req.Metadata["creation_date"] == "" {
		s.addFileCreationDate(req.FilePath, req.Metadata)
	}

	doc := domain.Document{
		ID:       uuid.New().String(),
		Path:     req.FilePath,
		URL:      req.URL,
		Content:  content, // Storing full content might be redundant, consider trade-offs
		Metadata: req.Metadata,
		Created:  time.Now(),
	}

	chunkOptions := domain.ChunkOptions{
		Size:    req.ChunkSize,
		Overlap: req.Overlap,
		Method:  "sentence",
	}

	if req.ChunkSize <= 0 {
		chunkOptions.Size = 500
	}
	if req.Overlap < 0 {
		chunkOptions.Overlap = 50
	}

	textChunks, err := s.chunker.Split(content, chunkOptions)
	if err != nil {
		return domain.IngestResponse{}, fmt.Errorf("failed to chunk text: %w", err)
	}

	vectors, err := s.embedder.EmbedBatch(ctx, textChunks)
	if err != nil {
		return domain.IngestResponse{}, fmt.Errorf("failed to generate embeddings for chunks: %w", err)
	}

	var chunks []domain.Chunk
	for i, textChunk := range textChunks {
		chunk := domain.Chunk{
			ID:         fmt.Sprintf("%s_%d", doc.ID, i),
			DocumentID: doc.ID,
			Content:    textChunk,
			Vector:     vectors[i],
			Metadata:   doc.Metadata, // Pass down the combined metadata to each chunk
		}
		chunks = append(chunks, chunk)
	}

	// Store document metadata BEFORE vectors (cortexdb v2 needs document to exist for foreign key)
	if err := s.documentStore.Store(ctx, doc); err != nil {
		return domain.IngestResponse{}, fmt.Errorf("failed to store document: %w", err)
	}

	if err := s.vectorStore.Store(ctx, chunks); err != nil {
		return domain.IngestResponse{}, fmt.Errorf("failed to store vectors: %w", err)
	}

	// GraphRAG Extraction using new graphRAG service
	// This runs asynchronously to not block ingestion
	if s.graphRAG != nil && s.config.RAG.Enabled {
		log.Println("[GraphRAG] Starting knowledge graph extraction (async)...")

		go func() {
			if err := s.graphRAG.ProcessDocument(context.Background(), chunks, doc.ID); err != nil {
				log.Printf("[GraphRAG] Error processing document: %v", err)
			} else {
				log.Println("[GraphRAG] Knowledge graph extraction completed")
			}
		}()
	}

	return domain.IngestResponse{
			Success:    true,
			DocumentID: doc.ID,
			ChunkCount: len(chunks),
			Message:    fmt.Sprintf("Successfully ingested %d chunks", len(chunks)),
		},
		nil
}

// IngestBatch ingests multiple documents concurrently
func (s *Service) IngestBatch(ctx context.Context, reqs []domain.IngestRequest) ([]domain.IngestResponse, error) {
	var responses []domain.IngestResponse
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Limit concurrency
	sem := make(chan struct{}, 5)

	for i, req := range reqs {
		wg.Add(1)
		go func(r domain.IngestRequest, idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			resp, err := s.Ingest(ctx, r)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				log.Printf("Failed to ingest item %d (%s): %v", idx, r.FilePath, err)
			} else {
				responses = append(responses, resp)
			}
		}(req, i)
	}

	wg.Wait()
	return responses, nil
}

// mergeMetadata merges the extracted metadata into the request's metadata map.
func (s *Service) mergeMetadata(base map[string]interface{}, extracted *domain.ExtractedMetadata) {
	if extracted.Summary != "" {
		base["summary"] = extracted.Summary
	}
	if len(extracted.Keywords) > 0 {
		base["keywords"] = extracted.Keywords
	}
	if extracted.DocumentType != "" {
		base["document_type"] = extracted.DocumentType
	}
	if extracted.CreationDate != "" {
		base["creation_date"] = extracted.CreationDate
	}
	if extracted.Collection != "" {
		base["collection"] = extracted.Collection
	}

	// Merge enhanced fields
	if len(extracted.TemporalRefs) > 0 {
		base["temporal_refs"] = extracted.TemporalRefs
	}
	if len(extracted.Entities) > 0 {
		base["entities"] = extracted.Entities
	}
	if len(extracted.Events) > 0 {
		base["events"] = extracted.Events
	}
	if len(extracted.CustomMeta) > 0 {
		for k, v := range extracted.CustomMeta {
			base[k] = v
		}
	}
}

// addFileCreationDate adds the file's modification time as a fallback creation date.
func (s *Service) addFileCreationDate(filePath string, metadata map[string]interface{}) {
	if filePath == "" {
		return
	}
	fileInfo, err := os.Stat(filePath)
	if err == nil {
		metadata["creation_date"] = fileInfo.ModTime().Format("2006-01-02")
	}
}

func (s *Service) Query(ctx context.Context, req domain.QueryRequest) (domain.QueryResponse, error) {
	start := time.Now()

	if req.Query == "" {
		return domain.QueryResponse{}, fmt.Errorf("%w: empty query", domain.ErrInvalidInput)
	}

	// Retrieve relevant memory if available
	var memoryContext string
	if s.memoryService != nil {
		var retrievedMems []*domain.MemoryWithScore
		memoryContext, retrievedMems, _ = s.memoryService.RetrieveAndInject(
			ctx, req.Query, req.ConversationID,
		)
		// Store memories for potential use in response
		_ = retrievedMems // Will be added to response if needed
	}

	chunks, err := s.hybridSearch(ctx, req)
	if err != nil {
		return domain.QueryResponse{}, err
	}

	if len(chunks) == 0 && memoryContext == "" {
		return domain.QueryResponse{
				Answer:  "很抱歉，我在知识库中找不到相关信息来回答您的问题。",
				Sources: []domain.Chunk{},
				Elapsed: time.Since(start).String(),
			},
			nil
	}

	prompt := s.composePromptWithMemory(chunks, memoryContext, req.Query)

	genOpts := &domain.GenerationOptions{
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	if genOpts.Temperature <= 0 {
		genOpts.Temperature = 0.7
	}
	if genOpts.MaxTokens <= 0 {
		genOpts.MaxTokens = 2000
	}

	answer, err := s.generator.Generate(ctx, prompt, genOpts)
	if err != nil {
		return domain.QueryResponse{}, fmt.Errorf("failed to generate answer: %w", err)
	}

	// Persist to unified agentgo.db if available
	if s.agentgoDB != nil && req.ConversationID != "" {
		go func() {
			// Best effort persistence
			_ = s.agentgoDB.AddMessage(req.ConversationID, "user", req.Query, nil)
			_ = s.agentgoDB.AddMessage(req.ConversationID, "assistant", answer, map[string]interface{}{"type": "rag"})
		}()
	}

	// Clean up internal thinking tags from the answer only if ShowThinking is false
	if !req.ShowThinking {
		answer = s.cleanThinkingTags(answer)
	}

	// Prepare sources based on ShowSources flag
	var sources []domain.Chunk
	if req.ShowSources {
		sources = chunks
	} else {
		sources = []domain.Chunk{}
	}

	return domain.QueryResponse{
			Answer:  answer,
			Sources: sources,
			Elapsed: time.Since(start).String(),
		},
		nil
}

// QueryWithTools processes a query with tool calling support
// QueryWithTools - deprecated, use MCP instead
func (s *Service) QueryWithTools(ctx context.Context, req domain.QueryRequest) (domain.QueryResponse, error) {
	return s.Query(ctx, req)
}

// StreamQueryWithTools processes a streaming query with tool calling support
// StreamQueryWithTools - deprecated, use MCP instead
func (s *Service) StreamQueryWithTools(ctx context.Context, req domain.QueryRequest, callback func(string)) error {
	return s.StreamQuery(ctx, req, callback)
}

// getAvailableTools returns the list of available tools based on allowed list
// getAvailableTools - deprecated
func (s *Service) getAvailableTools(allowedTools []string) []interface{} {
	return nil
}

// composePromptWithMemory builds a prompt with RAG context and memory
func (s *Service) composePromptWithMemory(chunks []domain.Chunk, memoryContext, query string) string {
	if len(chunks) == 0 && memoryContext == "" {
		return fmt.Sprintf(`Please answer the user's question: %s

If you need current information (like time, date, weather, file contents, web data, etc.), use the available tools to get accurate and up-to-date information.`, query)
	}

	var contextParts []string

	// Add memory context first (agent memory)
	if memoryContext != "" {
		contextParts = append(contextParts, memoryContext)
	}

	// Add RAG chunks
	if len(chunks) > 0 {
		for i, chunk := range chunks {
			contextParts = append(contextParts, fmt.Sprintf("[Document %d]\n%s", i+1, chunk.Content))
		}
	}

	context := strings.Join(contextParts, "\n\n")

	return fmt.Sprintf(`Please answer the user's question using the following context AND any available tools when needed.

IMPORTANT INSTRUCTIONS:
1. For questions about current information (time, date, weather, file contents, web data, etc.), always use the appropriate tools to get accurate and up-to-date information.
2. For questions about stored knowledge, use the provided context documents.
3. If both context and tools are relevant, combine information from both sources.

Context:
%s

User Question: %s

Please provide a comprehensive answer using both the context and tools as appropriate.`, context, query)
}

// buildPromptWithContext builds a prompt with RAG context
func (s *Service) buildPromptWithContext(query string, chunks []domain.Chunk) string {
	if len(chunks) == 0 {
		return fmt.Sprintf(`Please answer the user's question: %s

If you need current information (like time, date, weather, file contents, web data, etc.), use the available tools to get accurate and up-to-date information.`, query)
	}

	var contextParts []string
	for i, chunk := range chunks {
		contextParts = append(contextParts, fmt.Sprintf("[Document %d]\n%s", i+1, chunk.Content))
	}

	context := strings.Join(contextParts, "\n\n")

	return fmt.Sprintf(`Please answer the user's question using the following context AND any available tools when needed.

IMPORTANT INSTRUCTIONS:
1. For questions about current information (time, date, weather, file contents, web data, etc.), always use the appropriate tools to get accurate and up-to-date information.
2. For questions about stored knowledge, use the provided context documents.
3. If both context and tools are relevant, combine information from both sources.

Context Documents:
%s

User Question: %s

Please provide a comprehensive answer using both the context documents and tools as appropriate.`, context, query)
}

func (s *Service) StreamQuery(ctx context.Context, req domain.QueryRequest, callback func(string)) error {
	if req.Query == "" {
		return fmt.Errorf("%w: empty query", domain.ErrInvalidInput)
	}

	if callback == nil {
		return fmt.Errorf("%w: nil callback", domain.ErrInvalidInput)
	}

	chunks, err := s.hybridSearch(ctx, req)
	if err != nil {
		return err
	}

	if len(chunks) == 0 {
		callback("很抱歉，我在知识库中找不到相关信息来回答您的问题。")
		return nil
	}

	prompt := composePrompt(chunks, req.Query)

	genOpts := &domain.GenerationOptions{
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	if genOpts.Temperature <= 0 {
		genOpts.Temperature = 0.7
	}
	if genOpts.MaxTokens <= 0 {
		genOpts.MaxTokens = 2000
	}

	var fullAnswer strings.Builder
	wrappedCallback := func(token string) {
		fullAnswer.WriteString(token)
		callback(token)
	}

	err = s.generator.Stream(ctx, prompt, genOpts, s.wrapCallbackForThinking(wrappedCallback, req.ShowThinking))
	if err == nil && s.agentgoDB != nil && req.ConversationID != "" {
		go func() {
			_ = s.agentgoDB.AddMessage(req.ConversationID, "user", req.Query, nil)
			_ = s.agentgoDB.AddMessage(req.ConversationID, "assistant", fullAnswer.String(), map[string]interface{}{"type": "rag_stream"})
		}()
	}
	return err
}

func (s *Service) hybridSearch(ctx context.Context, req domain.QueryRequest) ([]domain.Chunk, error) {
	if req.TopK <= 0 {
		req.TopK = 5
	}

	// Simple vector search only
	queryVector, err := s.embedder.Embed(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}

	// 1. Standard or Advanced Vector Search (Chunks)
	var chunks []domain.Chunk
	if req.RerankStrategy != "" {
		chunks, err = s.vectorStore.SearchWithReranker(ctx, queryVector, req.Query, req.TopK, req.RerankStrategy, req.RerankBoost)
	} else if req.DiversityLambda > 0 {
		chunks, err = s.vectorStore.SearchWithDiversity(ctx, queryVector, req.TopK, req.DiversityLambda)
	} else if len(req.Filters) > 0 {
		chunks, err = s.vectorStore.SearchWithFilters(ctx, queryVector, req.TopK, req.Filters)
	} else {
		chunks, err = s.vectorStore.Search(ctx, queryVector, req.TopK)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to search vectors: %w", err)
	}

	// 2. Graph Search (Entities) - Enrich with Knowledge Graph
	// Only run if GRAPHRAG_ENABLED is set (disabled by default - too slow)
	if s.graphStore != nil && os.Getenv("GRAPHRAG_ENABLED") == "true" {
		var startNodeID string
		// Extract start node from query (if query is substantial)
		if len(req.Query) > 10 {
			// This adds latency but enables graph traversal
			entities, err := s.extractor.Extract(ctx, req.Query)
			if err == nil && len(entities.Entities) > 0 {
				startNodeID = generateEntityID(entities.Entities[0].Name, "")
				log.Printf("GraphRAG: Using start node '%s' (%s)", entities.Entities[0].Name, startNodeID)
			}
		}

		// Perform hybrid search
		graphResults, err := s.graphStore.HybridSearch(ctx, queryVector, startNodeID, 3) // Fetch top 3 entities
		if err == nil {
			for _, res := range graphResults {
				if res.Node != nil {
					name, _ := res.Node.Properties["name"].(string)

					// Create a pseudo-chunk for the entity
					entityChunk := domain.Chunk{
						ID:         "graph_" + res.Node.ID,
						DocumentID: "graph_virtual_doc",
						Content: fmt.Sprintf("[Knowledge Graph Entity]\nName: %s\nType: %s\nDescription: %s",
							name,
							res.Node.NodeType,
							res.Node.Content),
						Score:    res.Score,
						Metadata: res.Node.Properties,
					}
					// Ensure metadata has source
					if entityChunk.Metadata == nil {
						entityChunk.Metadata = make(map[string]interface{})
					}
					entityChunk.Metadata["source"] = "Knowledge Graph"

					chunks = append(chunks, entityChunk)
				}
			}
		} else {
			log.Printf("Graph hybrid search failed: %v", err)
		}
	}

	return s.deduplicateChunks(chunks), nil
}

func (s *Service) ListDocuments(ctx context.Context) ([]domain.Document, error) {
	return s.documentStore.List(ctx)
}

func (s *Service) DeleteDocument(ctx context.Context, documentID string) error {
	if documentID == "" {
		return fmt.Errorf("%w: empty document ID", domain.ErrInvalidInput)
	}

	if err := s.vectorStore.Delete(ctx, documentID); err != nil {
		return fmt.Errorf("failed to delete from vector store: %w", err)
	}

	if err := s.documentStore.Delete(ctx, documentID); err != nil {
		return fmt.Errorf("failed to delete from document store: %w", err)
	}

	return nil
}

func (s *Service) Reset(ctx context.Context) error {
	// Reset vector store (Qdrant)
	if err := s.vectorStore.Reset(ctx); err != nil {
		return fmt.Errorf("failed to reset vector store: %w", err)
	}

	// Reset document store (SQLite) if it has a Reset method
	if s.documentStore != nil {
		if resetter, ok := s.documentStore.(interface{ Reset(context.Context) error }); ok {
			if err := resetter.Reset(ctx); err != nil {
				return fmt.Errorf("failed to reset document store: %w", err)
			}
		}
	}

	return nil
}

func (s *Service) validateIngestRequest(req domain.IngestRequest) error {
	hasContent := req.Content != ""
	hasFilePath := req.FilePath != ""
	hasURL := req.URL != ""

	contentSources := 0
	if hasContent {
		contentSources++
	}
	if hasFilePath {
		contentSources++
	}
	if hasURL {
		contentSources++
	}

	if contentSources == 0 {
		return fmt.Errorf("%w: no content source provided", domain.ErrInvalidInput)
	}

	if contentSources > 1 {
		return fmt.Errorf("%w: multiple content sources provided", domain.ErrInvalidInput)
	}

	return nil
}

func (s *Service) extractContent(req domain.IngestRequest) (string, error) {
	if req.Content != "" {
		return req.Content, nil
	}

	if req.FilePath != "" {
		return s.readFile(req.FilePath)
	}

	if req.URL != "" {
		return "", fmt.Errorf("URL content extraction not yet implemented")
	}

	return "", fmt.Errorf("%w: no content source", domain.ErrInvalidInput)
}

func (s *Service) readFile(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".txt", ".md", ".markdown", ".adoc", ".asciidoc", ".html", ".htm":
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to read file %s: %w", filePath, err)
		}
		return string(data), nil

	case ".pdf":
		r, err := pdf.Open(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to open PDF %s: %w", filePath, err)
		}
		var buf strings.Builder
		for i := 1; i <= r.NumPage(); i++ {
			p := r.Page(i)
			if p.V.IsNull() {
				continue
			}
			text, err := p.GetPlainText(nil)
			if err != nil {
				// Log a warning but continue processing other pages
				log.Printf("Warning: failed to get text from page %d of %s: %v", i, filePath, err)
				continue
			}
			buf.WriteString(text)
			buf.WriteString("\n") // Add a newline between pages
		}
		return buf.String(), nil

	default:
		return "", fmt.Errorf("unsupported file type: %s", ext)
	}
}

// deduplicateChunks removes duplicate chunks by content to avoid confusion
func (s *Service) deduplicateChunks(chunks []domain.Chunk) []domain.Chunk {
	seen := make(map[string]bool)
	result := make([]domain.Chunk, 0, len(chunks))

	for _, chunk := range chunks {
		// Use content as the key for deduplication
		if !seen[chunk.Content] {
			seen[chunk.Content] = true
			result = append(result, chunk)
		}
	}

	return result
}

// cleanThinkingTags removes internal thinking tags from LLM responses
func (s *Service) cleanThinkingTags(answer string) string {
	// Remove <think>...</think> blocks and their contents
	re := strings.NewReplacer("<think>", "", "</think>", "")
	cleaned := re.Replace(answer)

	// Also handle the case where thinking tags might span multiple lines
	if strings.Contains(cleaned, "<think") || strings.Contains(cleaned, "</think") {
		// Use regex for more complex cases
		lines := strings.Split(cleaned, "\n")
		var filtered []string
		inThinking := false

		for _, line := range lines {
			if strings.Contains(line, "<think") {
				inThinking = true
				continue
			}
			if strings.Contains(line, "</think") {
				inThinking = false
				continue
			}
			if !inThinking {
				filtered = append(filtered, line)
			}
		}
		cleaned = strings.Join(filtered, "\n")
	}

	// Trim any extra whitespace
	return strings.TrimSpace(cleaned)
}

// wrapCallbackForThinking wraps the callback to filter thinking tags in streaming mode
func (s *Service) wrapCallbackForThinking(callback func(string), showThinking bool) func(string) {
	if showThinking {
		// If showing thinking, just pass through
		return callback
	}

	// If not showing thinking, filter out thinking content
	var buffer strings.Builder
	inThinking := false

	return func(token string) {
		buffer.WriteString(token)
		content := buffer.String()

		// Process complete thinking blocks
		for {
			if !inThinking {
				// Look for start of thinking block
				if idx := strings.Index(content, "<think>"); idx != -1 {
					// Send content before thinking block
					if idx > 0 {
						callback(content[:idx])
					}
					inThinking = true
					content = content[idx+7:] // Skip "<think>"
					buffer.Reset()
					buffer.WriteString(content)
					continue
				} else {
					// No thinking block start, send everything
					if content != "" {
						callback(content)
						buffer.Reset()
					}
					break
				}
			} else {
				// Look for end of thinking block
				if idx := strings.Index(content, "</think>"); idx != -1 {
					inThinking = false
					content = content[idx+8:] // Skip "</think>"
					buffer.Reset()
					buffer.WriteString(content)
					continue
				} else {
					// Still in thinking block, don't send anything
					break
				}
			}
		}
	}
}

// GetToolRegistry returns the tool registry if tools are enabled
// GetToolRegistry - deprecated
func (s *Service) GetToolRegistry() interface{} {
	return nil
}

// GetToolExecutor returns the tool executor if tools are enabled
// GetToolExecutor - deprecated
func (s *Service) GetToolExecutor() interface{} {
	return nil
}

// RegisterMCPTools registers MCP tools with the processor
// RegisterMCPTools - deprecated
func (s *Service) RegisterMCPTools(mcpService interface{}) error {
	return fmt.Errorf("tools have been removed - use MCP servers directly")
}

// generateEntityID creates a deterministic ID for an entity
func generateEntityID(name, entityType string) string {
	// Normalize name
	normalized := strings.ToLower(strings.TrimSpace(name))
	// Create seed string
	// If type is empty, we just hash the name to ensure we can link to it
	// even if we don't know the type in a relationship context
	var seed string
	if entityType == "" {
		seed = normalized
	} else {
		seed = fmt.Sprintf("%s:%s", normalized, strings.ToLower(strings.TrimSpace(entityType)))
	}
	// Generate UUID
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String()
}

func (s *Service) StartChat(ctx context.Context, metadata map[string]interface{}) (*domain.ChatSession, error) {
	if s.chatStore == nil {
		return nil, fmt.Errorf("chat store not initialized")
	}

	session := &domain.ChatSession{
		ID:        uuid.New().String(),
		Metadata:  metadata,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := s.chatStore.CreateSession(ctx, session); err != nil {
		return nil, err
	}

	return session, nil
}

func (s *Service) Chat(ctx context.Context, sessionID string, message string, opts *domain.QueryRequest) (*domain.QueryResponse, error) {
	if s.chatStore == nil {
		return nil, fmt.Errorf("chat store not initialized")
	}

	// 1. Embed user message for semantic search
	msgVector, err := s.embedder.Embed(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("failed to embed message: %w", err)
	}

	// 2. Store user message
	userMsg := &domain.ChatMessage{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      "user",
		Content:   message,
		Vector:    msgVector,
		CreatedAt: time.Now(),
	}
	if err := s.chatStore.AddMessage(ctx, userMsg); err != nil {
		return nil, fmt.Errorf("failed to store user message: %w", err)
	}

	// 3. Retrieve relevant history (Recent + Semantic)
	// Recent history for immediate context
	recentHistory, err := s.chatStore.GetSessionHistory(ctx, sessionID, 10)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent history: %w", err)
	}

	// Semantic search for long-term memory recall
	relevantHistory, err := s.chatStore.SearchChatHistory(ctx, msgVector, sessionID, 5)
	if err != nil {
		log.Printf("Warning: failed to search chat history: %v", err)
	}

	// 4. Perform Hybrid RAG Search (Documents + Graph)
	ragChunks, err := s.hybridSearch(ctx, *opts)
	if err != nil {
		return nil, fmt.Errorf("hybrid search failed: %w", err)
	}

	// 5. Compose Prompt
	prompt := s.buildChatPrompt(message, recentHistory, relevantHistory, ragChunks)

	// 6. Generate Response
	genOpts := &domain.GenerationOptions{
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
	}
	answer, err := s.generator.Generate(ctx, prompt, genOpts)
	if err != nil {
		return nil, fmt.Errorf("generation failed: %w", err)
	}

	// Clean thinking tags if needed
	if !opts.ShowThinking {
		answer = s.cleanThinkingTags(answer)
	}

	// 7. Store Assistant Message
	ansVector, _ := s.embedder.Embed(ctx, answer) // Ignore error, best effort

	asstMsg := &domain.ChatMessage{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      "assistant",
		Content:   answer,
		Vector:    ansVector,
		CreatedAt: time.Now(),
	}
	if err := s.chatStore.AddMessage(ctx, asstMsg); err != nil {
		log.Printf("Warning: failed to store assistant message: %v", err)
	}

	return &domain.QueryResponse{
		Answer:  answer,
		Sources: ragChunks,
	}, nil
}

func (s *Service) buildChatPrompt(query string, recent []*domain.ChatMessage, relevant []*domain.ChatMessage, chunks []domain.Chunk) string {
	var sb strings.Builder

	systemMsg, err := s.promptManager.Render(prompt.RAGSystemPrompt, nil)
	if err != nil {
		systemMsg = "You are a helpful AI assistant with access to a knowledge base and conversation history."
	}

	sb.WriteString(systemMsg + "\n")
	sb.WriteString("Use the following context to answer the user's question.\n\n")

	// Knowledge Base Context
	if len(chunks) > 0 {
		sb.WriteString("### Knowledge Base Context:\n")
		for i, chunk := range chunks {
			sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, chunk.Content))
		}
		sb.WriteString("\n")
	}

	// Relevant Past Conversation (Recall)
	seenIDs := make(map[string]bool)
	for _, msg := range recent {
		seenIDs[msg.ID] = true
	}

	hasRelevant := false
	for _, msg := range relevant {
		if !seenIDs[msg.ID] && msg.Role != "system" {
			if !hasRelevant {
				sb.WriteString("### Relevant Past Conversation:\n")
				hasRelevant = true
			}
			sb.WriteString(fmt.Sprintf("%s: %s\n", strings.Title(msg.Role), msg.Content))
		}
	}
	if hasRelevant {
		sb.WriteString("\n")
	}

	// Recent Conversation History
	sb.WriteString("### Recent Conversation:\n")
	for _, msg := range recent {
		if msg.Role != "system" {
			sb.WriteString(fmt.Sprintf("%s: %s\n", strings.Title(msg.Role), msg.Content))
		}
	}

	// If the most recent message isn't the query (unlikely given logic flow), append query
	// But since we store before fetch, it should be there.
	// Safety check:
	if len(recent) == 0 || recent[len(recent)-1].Content != query {
		sb.WriteString(fmt.Sprintf("User: %s\n", query))
	}

	sb.WriteString("\nAssistant:")

	return sb.String()
}

// composePrompt creates a RAG prompt from document chunks and user query
func composePrompt(chunks []domain.Chunk, query string) string {
	if len(chunks) == 0 {
		return fmt.Sprintf("Please answer the following question:\n\n%s", query)
	}

	var promptBuilder strings.Builder

	promptBuilder.WriteString("Based on the following document content, please answer the user's question. If the documents do not contain relevant information, please indicate that you cannot find an answer from the provided documents.\n\n")
	promptBuilder.WriteString("Document Content:\n")

	for i, chunk := range chunks {
		promptBuilder.WriteString(fmt.Sprintf("[Document Fragment %d]\n%s\n\n", i+1, chunk.Content))
	}

	promptBuilder.WriteString(fmt.Sprintf("User Question: %s\n\n", query))
	promptBuilder.WriteString("Please provide a detailed and accurate answer based on the document content:")

	return promptBuilder.String()
}
