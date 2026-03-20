package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/prompt"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

// Service implements the MemoryService interface
type Service struct {
	store         domain.MemoryStore
	shadowIndex   domain.MemoryStore // Optional vector-based shadow index
	entityMemory  *EntityMemory
	llm           domain.Generator
	embedder      domain.Embedder
	promptManager *prompt.Manager
	minScore      float64
	maxMemories   int

	// Enhanced memory components
	scorer       *MemoryScorer
	classifier   *QueryClassifier
	noiseFilter  *NoiseFilter
	scopeWeights *ScopeWeightConfig

	// Hybrid search
	enableHybrid bool
	rrfK         float64

	// PageIndex-style navigator for file-based stores (no embedder)
	navigator        *IndexNavigator
	reflectThreshold int // auto-reflect after this many new facts

	mu sync.RWMutex
}

// Config holds configuration for the memory service
type Config struct {
	MinScore    float64 // Minimum relevance score for memory retrieval (default 0.7)
	MaxMemories int     // Maximum memories to inject (default 5)

	// Enhanced features
	ScoringConfig     *ScoringConfig
	ClassifierConfig  *ClassifierConfig
	NoiseFilterConfig *NoiseFilterConfig
	ScopeWeights      *ScopeWeightConfig

	// Hybrid search
	EnableHybrid bool
	RRFK         float64

	// Hindsight: auto-reflect after this many new facts (0 = disabled)
	ReflectThreshold int
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		MinScore:          0.01,
		MaxMemories:       5,
		ScoringConfig:     DefaultScoringConfig(),
		ClassifierConfig:  DefaultClassifierConfig(),
		NoiseFilterConfig: DefaultNoiseFilterConfig(),
		ScopeWeights:      DefaultScopeWeightConfig(),
		EnableHybrid:      false,
		RRFK:              60.0,
		ReflectThreshold:  5,
	}
}

// NewService creates a new memory service
func NewService(
	memStore domain.MemoryStore,
	llm domain.Generator,
	embedder domain.Embedder,
	config *Config,
) *Service {
	if config == nil {
		config = DefaultConfig()
	}

	svc := &Service{
		store:            memStore,
		entityMemory:     NewEntityMemory(memStore, embedder),
		llm:              llm,
		embedder:         embedder,
		promptManager:    prompt.NewManager(),
		minScore:         config.MinScore,
		maxMemories:      config.MaxMemories,
		scorer:           NewMemoryScorer(config.ScoringConfig),
		classifier:       NewQueryClassifier(config.ClassifierConfig),
		scopeWeights:     config.ScopeWeights,
		enableHybrid:     config.EnableHybrid,
		rrfK:             config.RRFK,
		reflectThreshold: config.ReflectThreshold,
	}

	if config.NoiseFilterConfig != nil {
		svc.noiseFilter = NewNoiseFilter(config.NoiseFilterConfig)
	}

	// Wire up IndexNavigator for file-based stores when no embedder is available
	if fileStore, ok := memStore.(*store.FileMemoryStore); ok && llm != nil {
		fileStore.WithLLM(llm)
		svc.navigator = NewIndexNavigator(fileStore, llm)
	}

	return svc
}

// SetPromptManager sets a custom prompt manager
func (s *Service) SetPromptManager(m *prompt.Manager) {
	s.promptManager = m
}

// SetShadowIndex sets the optional vector index for accelerating file-based stores
func (s *Service) SetShadowIndex(idx domain.MemoryStore) {
	s.shadowIndex = idx
}

// RetrieveAndInject searches relevant memories and formats them for LLM context.
// T6a: Routing logic —
//   - embedder available → vector search (+ optional hybrid BM25)
//   - navigator available → IndexNavigator (LLM reads _index/) — used alone or fused via RRF
//   - both → results are RRF-fused for highest recall
//
// Returns: formatted context string, scored memories, navigator reasoning (MemoryLogic), error
func (s *Service) RetrieveAndInject(ctx context.Context, query string, sessionID string) (string, []*domain.MemoryWithScore, error) {
	_, mems, _, err := s.RetrieveAndInjectWithContextAndLogic(ctx, query, domain.MemoryQueryContext{SessionID: sessionID})
	return s.formatMemories(mems), mems, err
}

// RetrieveAndInjectWithLogic is the full retrieval pipeline returning the navigator's
// reasoning string alongside the context and memories.
func (s *Service) RetrieveAndInjectWithLogic(ctx context.Context, query string, sessionID string) (string, []*domain.MemoryWithScore, string, error) {
	return s.RetrieveAndInjectWithContextAndLogic(ctx, query, domain.MemoryQueryContext{SessionID: sessionID})
}

// RetrieveAndInjectWithContext searches relevant memories using an explicit scope chain.
func (s *Service) RetrieveAndInjectWithContext(ctx context.Context, query string, queryContext domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, error) {
	_, mems, _, err := s.RetrieveAndInjectWithContextAndLogic(ctx, query, queryContext)
	return s.formatMemories(mems), mems, err
}

// RetrieveAndInjectWithContextAndLogic is the full retrieval pipeline returning the navigator's
// reasoning string alongside the context and memories.
func (s *Service) RetrieveAndInjectWithContextAndLogic(ctx context.Context, query string, queryContext domain.MemoryQueryContext) (string, []*domain.MemoryWithScore, string, error) {
	// 0. Adaptive retrieval - skip if query doesn't need memory
	if s.classifier != nil && !s.classifier.NeedsMemory(query) {
		return "", nil, "", nil
	}

	var allMemories []*domain.MemoryWithScore
	var memoryLogic string

	// 1. Entity Search (if query is not empty)
	if s.entityMemory != nil && query != "" {
		entities, err := s.entityMemory.SearchEntities(ctx, query, 3)
		if err == nil {
			for _, ent := range entities {
				content := fmt.Sprintf("Entity: %s (%s) - %s", ent.Name, ent.Type, ent.Description)
				allMemories = append(allMemories, &domain.MemoryWithScore{
					Memory: &domain.Memory{
						ID:         "ent_" + ent.Name,
						Type:       domain.MemoryTypeFact,
						Content:    content,
						Importance: 1.0,
					},
					Score: 1.0,
				})
			}
		}
	}

	// 2. Vector Search (if embedder available)
	scopes := DefaultScopeChain(queryContext.SessionID, queryContext.AgentID, queryContext.SquadID, queryContext.UserID)
	var vectorResults []*domain.MemoryWithScore
	if s.embedder != nil {
		vector, err := s.embedder.Embed(ctx, query)
		if err == nil {
			vectorResults, _ = s.store.SearchByScope(ctx, vector, scopes.ToSlice(), s.maxMemories*2)

			if s.enableHybrid {
				textMems, _ := s.store.SearchByText(ctx, query, s.maxMemories)
				textMems = filterMemoriesByScopes(textMems, scopes.ToSlice())
				vectorResults = s.rrfFusion(vectorResults, textMems)
			}
		}
	}

	// 3. Navigator Search (PageIndex-style, for file stores) — captures reasoning
	var navResults []*domain.MemoryWithScore
	if s.navigator != nil {
		navResult, err := s.navigator.NavigateWithScopesAndReason(ctx, query, scopes.ToSlice(), s.maxMemories)
		if err == nil && navResult != nil {
			memoryLogic = navResult.Reasoning
			for i, m := range navResult.Memories {
				score := 1.0 - float64(i)*0.05
				navResults = append(navResults, &domain.MemoryWithScore{Memory: m, Score: score})
			}
		}
	}

	// 4. Fuse results
	switch {
	case len(vectorResults) > 0 && len(navResults) > 0:
		// Both available: RRF fusion
		allMemories = append(allMemories, s.rrfFusion(vectorResults, navResults)...)
	case len(vectorResults) > 0:
		allMemories = append(allMemories, vectorResults...)
	case len(navResults) > 0:
		allMemories = append(allMemories, navResults...)
	default:
		// Final fallback: keyword search first, then plain list
		if query != "" {
			textMems, err := s.store.SearchByText(ctx, query, s.maxMemories)
			if err == nil && len(textMems) > 0 {
				textMems = filterMemoriesByScopes(textMems, scopes.ToSlice())
				allMemories = append(allMemories, textMems...)
				break
			}
		}
		mems, _, _ := s.store.List(ctx, s.maxMemories, 0)
		mems = filterPlainMemoriesByScopes(mems, scopes.ToSlice())
		for _, m := range mems {
			allMemories = append(allMemories, &domain.MemoryWithScore{Memory: m, Score: 0.5})
		}
	}

	// 5. Noise filtering
	if s.noiseFilter != nil {
		allMemories = s.noiseFilter.Filter(allMemories)
	}

	// 6. Scoring and ranking
	if s.scorer != nil {
		allMemories = s.scorer.ScoreAll(allMemories)
	} else {
		allMemories = s.mergeAndRank(allMemories)
	}

	// 7. Limit results
	if len(allMemories) > s.maxMemories {
		allMemories = allMemories[:s.maxMemories]
	}

	// Update access count
	for _, m := range allMemories {
		if m.ID != "" && !strings.HasPrefix(m.ID, "ent_") {
			_ = s.store.IncrementAccess(ctx, m.ID)
		}
	}

	if len(allMemories) == 0 {
		return "", nil, memoryLogic, nil
	}

	return s.formatMemories(allMemories), allMemories, memoryLogic, nil
}

// StoreIfWorthwhile decides what to store based on task completion.
// T6b: After storing new facts, checks if ReflectThreshold is reached and triggers async Reflect.
func (s *Service) StoreIfWorthwhile(ctx context.Context, req *domain.MemoryStoreRequest) error {
	if s.llm == nil {
		return nil
	}

	// 1. Triviality check: Don't bother LLM for very short/common greetings
	cleanGoal := strings.ToLower(strings.TrimSpace(req.TaskGoal))
	if len(cleanGoal) < 5 || cleanGoal == "hi" || cleanGoal == "hello" || cleanGoal == "hey" {
		return nil
	}
	if shouldSkipAutoStoreForTaskGoal(req.TaskGoal) {
		return nil
	}

	prompt := s.buildSummaryPrompt(req)
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"should_store": map[string]interface{}{"type": "boolean"},
			"memories": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"type":             map[string]interface{}{"type": "string", "enum": []string{"fact", "skill", "pattern", "context", "preference"}},
						"scope":            map[string]interface{}{"type": "string", "enum": []string{"session", "agent", "squad", "global"}},
						"promote_to":       map[string]interface{}{"type": "string", "enum": []string{"session", "agent", "squad", "global"}},
						"content":          map[string]interface{}{"type": "string"},
						"importance":       map[string]interface{}{"type": "number"},
						"scope_reason":     map[string]interface{}{"type": "string"},
						"promotion_reason": map[string]interface{}{"type": "string"},
					},
					"required": []string{"type", "content", "importance"},
				},
			},
		},
		"required": []string{"should_store", "memories"},
	}

	result, err := s.llm.GenerateStructured(ctx, prompt, schema, &domain.GenerationOptions{Temperature: 0.1})
	var summary *domain.MemorySummaryResult
	if err == nil && result.Raw != "" && result.Valid {
		summary, err = normalizeMemorySummary(result.Raw)
		if err != nil {
			summary = nil
		}
	}

	if summary == nil || !summary.ShouldStore || len(summary.Memories) == 0 {
		summary = heuristicMemorySummary(req)
	}

	if summary == nil || !summary.ShouldStore || len(summary.Memories) == 0 {
		return nil
	}

	factScopeCounts := make(map[string]int)
	for _, item := range summary.Memories {
		baseScope, initialScope, finalScope, placementMeta := resolveMemoryPlacement(req, item)
		_ = baseScope
		_ = initialScope
		mem := &domain.Memory{
			ID:         uuid.New().String(),
			SessionID:  ToBankID(finalScope),
			ScopeType:  finalScope.Type,
			ScopeID:    finalScope.ID,
			Type:       item.Type,
			Content:    item.Content,
			Keywords:   mergeUniqueStrings(item.Tags.Strings(), item.Entities.Strings()),
			Tags:       item.Tags.Strings(),
			Importance: item.Importance,
			SourceType: domain.MemorySourceInferred, // stored by agent inference
			CreatedAt:  time.Now(),
			Metadata: mergeMetadataMaps(
				map[string]interface{}{
					"entities": item.Entities.Strings(),
					"source":   "store_if_worthwhile",
				},
				placementMeta,
			),
		}
		_ = s.Add(ctx, mem)
		if item.Type == domain.MemoryTypeFact {
			factScopeCounts[mem.SessionID]++
		}
	}

	// Invalidate navigator cache since new memories were stored
	if s.navigator != nil {
		s.navigator.InvalidateCache()
	}

	// T6b: Async Reflect trigger — count facts for this session, fire if threshold reached
	if s.reflectThreshold > 0 {
		for bankID, count := range factScopeCounts {
			if count > 0 {
				go s.maybeReflect(bankID)
			}
		}
	}

	return nil
}

// maybeReflect counts active facts for sessionID and triggers Reflect if threshold is met.
// Only runs for FileMemoryStore (which has LLM-driven Reflect); other stores handle this internally.
// Runs in a goroutine; errors are silently swallowed.
func (s *Service) maybeReflect(sessionID string) {
	fileStore, ok := s.store.(*store.FileMemoryStore)
	if !ok {
		return
	}

	ctx := context.Background()
	facts, _, err := fileStore.List(ctx, 1000, 0)
	if err != nil {
		return
	}

	count := 0
	for _, m := range facts {
		if m.Type == domain.MemoryTypeFact &&
			(sessionID == "" || m.SessionID == sessionID) &&
			!store.IsStale(m) {
			count++
		}
	}

	if count >= s.reflectThreshold {
		_, _ = fileStore.Reflect(ctx, sessionID)
		// Invalidate cache again after reflection produced new observations
		if s.navigator != nil {
			s.navigator.InvalidateCache()
		}
	}
}

func (s *Service) Add(ctx context.Context, memory *domain.Memory) error {
	if memory.ID == "" {
		memory.ID = uuid.New().String()
	}
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = time.Now()
	}

	// Always generate embedding if possible
	if len(memory.Vector) == 0 && s.embedder != nil {
		vec, _ := s.embedder.Embed(ctx, memory.Content)
		memory.Vector = vec
	}

	// 1. Write to Primary Store (The Truth)
	err := s.store.Store(ctx, memory)
	if err != nil {
		return err
	}

	// 2. Write to Shadow Index (The Accelerator)
	if s.shadowIndex != nil {
		// Ensure it has vector before indexing
		if len(memory.Vector) > 0 {
			_ = s.shadowIndex.Store(ctx, memory)
		}
	}

	return nil
}

func (s *Service) Update(ctx context.Context, id string, newInfo string) error {
	if s.llm == nil {
		return fmt.Errorf("LLM required")
	}

	oldMem, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}

	prompt := fmt.Sprintf("Existing Memory:\n%s\n\nNew Info: %s\n\nMerge and provide updated content.", oldMem.Content, newInfo)
	updated, err := s.llm.Generate(ctx, prompt, nil)
	if err != nil {
		return err
	}

	oldMem.Content = strings.TrimSpace(updated)
	oldMem.UpdatedAt = time.Now()

	return s.store.Update(ctx, oldMem)
}

func (s *Service) Search(ctx context.Context, query string, topK int) ([]*domain.MemoryWithScore, error) {
	if topK <= 0 {
		topK = 10
	}

	// 1. Choose searching backend
	searchStore := s.store
	if s.shadowIndex != nil {
		searchStore = s.shadowIndex // Use vector-capable store for searching
	}

	// 2. Perform search
	if s.embedder == nil {
		mems, _, _ := s.store.List(ctx, topK, 0)
		var res []*domain.MemoryWithScore
		for _, m := range mems {
			res = append(res, &domain.MemoryWithScore{Memory: m, Score: 0.5})
		}
		return res, nil
	}

	vec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	results, err := searchStore.Search(ctx, vec, topK, s.minScore)
	if err != nil {
		return nil, err
	}

	// 3. Truth Retrieval: For each found ID, fetch fresh content from Primary Store
	// This ensures that even if vector index is slightly stale, we get the human-edited content.
	var finalResults []*domain.MemoryWithScore
	for _, res := range results {
		if fresh, err := s.store.Get(ctx, res.ID); err == nil {
			res.Memory = fresh
			finalResults = append(finalResults, res)
		} else if s.shadowIndex == nil {
			// If we are NOT in hybrid mode, keep the result even if Get fails (unlikely)
			finalResults = append(finalResults, res)
		}
		// In hybrid mode, if Get fails, it means the file is missing, so we skip it.
	}

	return finalResults, nil
}

func (s *Service) Get(ctx context.Context, id string) (*domain.Memory, error) {
	return s.store.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]*domain.Memory, int, error) {
	return s.store.List(ctx, limit, offset)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

func (s *Service) ConfigureBank(ctx context.Context, sessionID string, config *domain.MemoryBankConfig) error {
	return s.store.ConfigureBank(ctx, sessionID, config)
}

func (s *Service) Reflect(ctx context.Context, sessionID string) (string, error) {
	return s.store.Reflect(ctx, sessionID)
}

func (s *Service) AddMentalModel(ctx context.Context, model *domain.MentalModel) error {
	return s.store.AddMentalModel(ctx, model)
}

// Helpers

func (s *Service) mergeAndRank(memories []*domain.MemoryWithScore) []*domain.MemoryWithScore {
	seen := make(map[string]bool)
	unique := make([]*domain.MemoryWithScore, 0)
	for _, m := range memories {
		if !seen[m.ID] {
			seen[m.ID] = true
			unique = append(unique, m)
		}
	}
	for i := 0; i < len(unique)-1; i++ {
		for j := i + 1; j < len(unique); j++ {
			if unique[i].Score < unique[j].Score {
				unique[i], unique[j] = unique[j], unique[i]
			}
		}
	}
	return unique
}

// rrfFusion performs Reciprocal Rank Fusion for hybrid search
// RRF score = sum(1 / (k + rank)) for each list
func (s *Service) rrfFusion(vector, text []*domain.MemoryWithScore) []*domain.MemoryWithScore {
	k := s.rrfK
	if k <= 0 {
		k = 60.0
	}

	// Calculate RRF scores
	scores := make(map[string]float64)
	memories := make(map[string]*domain.MemoryWithScore)

	// Vector results
	for rank, m := range vector {
		if m == nil || m.Memory == nil {
			continue
		}
		id := m.ID
		if id == "" {
			id = m.Content // Fallback to content as ID
		}
		scores[id] += 1.0 / (k + float64(rank+1))
		if _, exists := memories[id]; !exists {
			memories[id] = m
		}
	}

	// Text results
	for rank, m := range text {
		if m == nil || m.Memory == nil {
			continue
		}
		id := m.ID
		if id == "" {
			id = m.Content
		}
		scores[id] += 1.0 / (k + float64(rank+1))
		if _, exists := memories[id]; !exists {
			memories[id] = m
		}
	}

	// Build result list
	var results []*domain.MemoryWithScore
	for id, score := range scores {
		if m, exists := memories[id]; exists {
			// Create copy to avoid modifying original
			result := &domain.MemoryWithScore{
				Memory: m.Memory,
				Score:  score,
			}
			results = append(results, result)
		}
	}

	// Sort by RRF score
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[i].Score < results[j].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}

func (s *Service) formatMemories(memories []*domain.MemoryWithScore) string {
	if len(memories) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Relevant Memory\n\n")
	for i, m := range memories {
		sb.WriteString(fmt.Sprintf("[%d] [%s]: %s\n\n", i+1, m.Type, m.Content))
	}
	return sb.String()
}

func filterMemoriesByScopes(memories []*domain.MemoryWithScore, scopes []domain.MemoryScope) []*domain.MemoryWithScore {
	if len(scopes) == 0 || len(memories) == 0 {
		return memories
	}

	filtered := make([]*domain.MemoryWithScore, 0, len(memories))
	for _, memory := range memories {
		if memory == nil || memory.Memory == nil {
			continue
		}
		if memoryMatchesAnyScope(memory.Memory, scopes) {
			filtered = append(filtered, memory)
		}
	}
	return filtered
}

func filterPlainMemoriesByScopes(memories []*domain.Memory, scopes []domain.MemoryScope) []*domain.Memory {
	if len(scopes) == 0 || len(memories) == 0 {
		return memories
	}

	filtered := make([]*domain.Memory, 0, len(memories))
	for _, memory := range memories {
		if memory == nil {
			continue
		}
		if memoryMatchesAnyScope(memory, scopes) {
			filtered = append(filtered, memory)
		}
	}
	return filtered
}

func memoryMatchesAnyScope(memory *domain.Memory, scopes []domain.MemoryScope) bool {
	if memory == nil {
		return false
	}

	memScope := ParseBankID(memory.SessionID)
	if normalized := normalizeScopeType(memory.ScopeType); normalized != domain.MemoryScopeGlobal || strings.TrimSpace(memory.ScopeID) != "" {
		memScope = domain.MemoryScope{Type: normalized, ID: strings.TrimSpace(memory.ScopeID)}
	}

	for _, scope := range scopes {
		normalized := domain.MemoryScope{Type: normalizeScopeType(scope.Type), ID: strings.TrimSpace(scope.ID)}
		if normalized.Type == memScope.Type && normalized.ID == memScope.ID {
			return true
		}
	}
	return false
}

func (s *Service) buildSummaryPrompt(req *domain.MemoryStoreRequest) string {
	return fmt.Sprintf(`SYSTEM: You are a background background memory process. ANALYZE the following interaction and EXTRACT new facts or user preferences. RETURN VALID JSON ONLY.

Runtime scope context:
- session_id: %s
- agent_id: %s
- squad_id: %s
- user_id: %s

Scope rules:
- Use "session" for transient context tied to the current conversation only.
- Use "agent" for reusable preferences, skills, and patterns this agent should remember across sessions.
- Use "squad" only when the memory is clearly shared task context, cross-agent coordination data, or squad-wide facts.
- Use "global" only for stable cross-squad/system knowledge.
- The "scope" field should describe the natural owning scope for the memory item.
- The optional "promote_to" field should only be used when the memory should be promoted to a higher shared scope than its natural owner.
- Only promote to "squad" when the content is explicitly shared, cross-agent, process-level, or coordination-critical.
- Only promote to "global" when the content is clearly stable and useful across squads or the whole system.
- Include "scope_reason" when scope selection is non-obvious.
- Include "promotion_reason" when you set "promote_to".

Interaction:
Goal: %s
Result: %s

Task: Extract potential memories according to the schema.`, req.SessionID, req.AgentID, req.SquadID, req.UserID, req.TaskGoal, req.TaskResult)
}

func defaultMemoryScope(req *domain.MemoryStoreRequest, item domain.MemoryItem) domain.MemoryScope {
	switch item.Type {
	case domain.MemoryTypeContext:
		if req.SessionID != "" {
			return SessionScope(req.SessionID)
		}
	case domain.MemoryTypePreference, domain.MemoryTypeSkill, domain.MemoryTypePattern:
		if req.AgentID != "" {
			return AgentScope(req.AgentID)
		}
		if req.SessionID != "" {
			return SessionScope(req.SessionID)
		}
	case domain.MemoryTypeFact:
		if isSharedSquadMemory(req, item) && req.SquadID != "" {
			return SquadScope(req.SquadID)
		}
		if req.SessionID != "" {
			return SessionScope(req.SessionID)
		}
	}

	if req.AgentID != "" {
		return AgentScope(req.AgentID)
	}
	if req.SquadID != "" {
		return SquadScope(req.SquadID)
	}
	if req.UserID != "" {
		return UserScope(req.UserID)
	}
	if req.SessionID != "" {
		return SessionScope(req.SessionID)
	}
	return GlobalScope()
}

func resolveMemoryPlacement(req *domain.MemoryStoreRequest, item domain.MemoryItem) (domain.MemoryScope, domain.MemoryScope, domain.MemoryScope, map[string]interface{}) {
	baseScope := defaultMemoryScope(req, item)
	initialScope := baseScope
	if scope := resolveExplicitScopeHint(req, item.Scope); scope.Type != "" {
		initialScope = scope
	}

	finalScope := initialScope
	if promoteScope := resolveExplicitScopeHint(req, item.PromoteTo); promoteScope.Type != "" {
		if scopeHierarchyLevel(promoteScope.Type) > scopeHierarchyLevel(initialScope.Type) {
			finalScope = promoteScope
		}
	}

	meta := map[string]interface{}{
		"default_scope":  ScopeString(baseScope),
		"resolved_scope": ScopeString(finalScope),
	}
	if strings.TrimSpace(item.ScopeReason) != "" {
		meta["scope_reason"] = strings.TrimSpace(item.ScopeReason)
	}
	if strings.TrimSpace(item.PromotionReason) != "" {
		meta["promotion_reason"] = strings.TrimSpace(item.PromotionReason)
	}
	if scopeHierarchyLevel(finalScope.Type) > scopeHierarchyLevel(initialScope.Type) {
		meta["promoted"] = true
		meta["promoted_from_scope"] = ScopeString(initialScope)
		meta["promoted_to_scope"] = ScopeString(finalScope)
	}
	return baseScope, initialScope, finalScope, meta
}

func resolveExplicitScopeHint(req *domain.MemoryStoreRequest, hint domain.MemoryScopeType) domain.MemoryScope {
	if strings.TrimSpace(string(hint)) == "" {
		return domain.MemoryScope{}
	}

	switch normalizeScopeType(hint) {
	case domain.MemoryScopeSession:
		if req.SessionID != "" {
			return SessionScope(req.SessionID)
		}
	case domain.MemoryScopeAgent:
		if req.AgentID != "" {
			return AgentScope(req.AgentID)
		}
	case domain.MemoryScopeSquad:
		if req.SquadID != "" {
			return SquadScope(req.SquadID)
		}
	case domain.MemoryScopeUser:
		if req.UserID != "" {
			return UserScope(req.UserID)
		}
	case domain.MemoryScopeGlobal:
		return GlobalScope()
	}
	return domain.MemoryScope{}
}

func isSharedSquadMemory(req *domain.MemoryStoreRequest, item domain.MemoryItem) bool {
	if req == nil || strings.TrimSpace(req.SquadID) == "" {
		return false
	}
	if req.Metadata != nil {
		if shared, ok := req.Metadata["shared"].(bool); ok && shared {
			return true
		}
		if scope, ok := req.Metadata["memory_scope"].(string); ok && strings.EqualFold(strings.TrimSpace(scope), "squad") {
			return true
		}
	}

	text := strings.ToLower(strings.Join(append([]string{item.Content}, append(item.Tags.Strings(), item.Entities.Strings()...)...), " "))
	keywords := []string{
		"shared", "cross-agent", "cross agent", "team", "squad", "captain", "specialist", "handoff", "roster",
		"shared context", "coordination", "workspace", "repository", "api contract", "deployment", "schema",
		"共享", "跨agent", "跨 agent", "团队", "小队", "协作", "交接",
	}
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

func mergeUniqueStrings(parts ...[]string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, group := range parts {
		for _, item := range group {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func heuristicMemorySummary(req *domain.MemoryStoreRequest) *domain.MemorySummaryResult {
	if req == nil {
		return nil
	}

	content := strings.TrimSpace(req.TaskGoal)
	if !looksLikeHeuristicMemoryStatement(content) {
		return nil
	}

	item := domain.MemoryItem{
		Type:        heuristicMemoryType(content),
		Content:     content,
		Importance:  heuristicMemoryImportance(content),
		ScopeReason: "Heuristic extraction from a direct declarative dialogue turn.",
	}

	return &domain.MemorySummaryResult{
		ShouldStore: true,
		Memories:    []domain.MemoryItem{item},
		Reasoning:   "Stored via deterministic dialogue heuristic fallback.",
	}
}

func shouldSkipAutoStoreForTaskGoal(goal string) bool {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return true
	}
	if looksLikeExplicitMemorySave(goal) {
		return false
	}
	return looksLikeInformationSeekingQuestion(goal)
}

func looksLikeExplicitMemorySave(goal string) bool {
	lower := strings.ToLower(strings.TrimSpace(goal))
	if lower == "" {
		return false
	}
	prefixes := []string{
		"remember:",
		"save to memory",
		"please remember",
		"remember that",
		"store this in memory",
		"keep this in mind",
		"记住:",
		"记住：",
		"请记住",
		"帮我记住",
		"保存到记忆",
		"存到记忆",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func looksLikeInformationSeekingQuestion(goal string) bool {
	trimmed := strings.TrimSpace(goal)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.ContainsAny(trimmed, "?\n\r\t") || strings.Contains(trimmed, "？") {
		return true
	}
	prefixes := []string{
		"what ", "which ", "who ", "where ", "when ", "why ", "how ",
		"can you", "could you", "would you", "will you",
		"tell me", "explain", "describe", "list ", "show ", "find ", "search ", "compare ",
		"什么", "哪个", "谁", "哪里", "什么时候", "为什么", "怎么",
		"告诉我", "解释", "描述", "列出", "展示", "查找", "搜索", "比较",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func looksLikeHeuristicMemoryStatement(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}

	lower := strings.ToLower(text)
	if len([]rune(text)) < 8 || len([]rune(text)) > 240 {
		return false
	}
	if strings.ContainsAny(text, "?\n\r\t") || strings.ContainsAny(text, "？") {
		return false
	}

	commandPrefixes := []string{
		"remember:", "save to memory", "what ", "why ", "how ", "who ", "when ", "where ",
		"can you", "could you", "please ", "list ", "show ", "find ", "search ", "explain ",
		"tell me", "help ", "summarize ", "compare ", "write ", "generate ",
		"请", "帮我", "告诉我", "解释", "总结", "比较", "搜索", "查一下", "找一下", "列出",
	}
	for _, prefix := range commandPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}

	acknowledgements := []string{
		"ok", "okay", "thanks", "thank you", "got it", "understood",
		"好的", "谢谢", "收到", "明白了",
	}
	for _, token := range acknowledgements {
		if lower == token {
			return false
		}
	}

	return true
}

func heuristicMemoryType(text string) domain.MemoryType {
	lower := strings.ToLower(text)

	preferenceKeywords := []string{
		"prefer", "prefers", "like", "likes", "love", "loves", "favorite", "enjoy", "enjoys",
		"dislike", "dislikes", "hate", "hates",
		"喜欢", "偏好", "更喜欢", "最喜欢", "不喜欢", "讨厌",
	}
	for _, keyword := range preferenceKeywords {
		if strings.Contains(lower, keyword) {
			return domain.MemoryTypePreference
		}
	}

	patternKeywords := []string{
		"always", "usually", "typically", "tends to", "often", "generally",
		"总是", "通常", "经常", "往往",
	}
	for _, keyword := range patternKeywords {
		if strings.Contains(lower, keyword) {
			return domain.MemoryTypePattern
		}
	}

	skillKeywords := []string{
		"skilled at", "good at", "experienced in", "can build", "can write", "can implement",
		"擅长", "会", "能够", "熟悉",
	}
	for _, keyword := range skillKeywords {
		if strings.Contains(lower, keyword) {
			return domain.MemoryTypeSkill
		}
	}

	contextKeywords := []string{
		"currently", "right now", "at the moment", "working on", "in this session",
		"目前", "现在", "正在", "这次对话", "当前",
	}
	for _, keyword := range contextKeywords {
		if strings.Contains(lower, keyword) {
			return domain.MemoryTypeContext
		}
	}

	return domain.MemoryTypeFact
}

func heuristicMemoryImportance(text string) float64 {
	switch heuristicMemoryType(text) {
	case domain.MemoryTypePreference:
		return 0.9
	case domain.MemoryTypeSkill:
		return 0.85
	case domain.MemoryTypePattern:
		return 0.8
	case domain.MemoryTypeContext:
		return 0.7
	default:
		return 0.75
	}
}

func scopeHierarchyLevel(scopeType domain.MemoryScopeType) int {
	switch normalizeScopeType(scopeType) {
	case domain.MemoryScopeSession:
		return 1
	case domain.MemoryScopeAgent:
		return 2
	case domain.MemoryScopeSquad:
		return 3
	case domain.MemoryScopeUser:
		return 4
	case domain.MemoryScopeGlobal:
		return 5
	default:
		return 0
	}
}

// normalizeMemorySummary parses the LLM response into MemorySummaryResult,
// handling common field-name variations (kind→type, summary→content, confidence→importance).
// If "should_store" is absent but memories are present, it infers should_store=true.
func normalizeMemorySummary(raw string) (*domain.MemorySummaryResult, error) {
	// First try direct unmarshal (strict schema match)
	var summary domain.MemorySummaryResult
	if err := json.Unmarshal([]byte(raw), &summary); err == nil {
		if len(summary.Memories) > 0 && !summary.ShouldStore {
			summary.ShouldStore = true
		}
		// Verify items actually have content (LLM may use non-standard field names)
		hasContent := false
		for _, m := range summary.Memories {
			if strings.TrimSpace(m.Content) != "" {
				hasContent = true
				break
			}
		}
		if summary.ShouldStore && hasContent {
			return &summary, nil
		}
	}

	// Fallback: parse as generic JSON and normalize field names
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}

	// Extract should_store
	shouldStore := false
	if v, ok := generic["should_store"]; ok {
		_ = json.Unmarshal(v, &shouldStore)
	}

	// Extract memories array from "memories" or root-level array
	var rawItems []json.RawMessage
	if v, ok := generic["memories"]; ok {
		_ = json.Unmarshal(v, &rawItems)
	}
	if len(rawItems) == 0 {
		// Try parsing the entire raw as an array
		_ = json.Unmarshal([]byte(raw), &rawItems)
	}

	var memories []domain.MemoryItem
	validTypes := map[string]domain.MemoryType{
		"fact": domain.MemoryTypeFact, "skill": domain.MemoryTypeSkill,
		"pattern": domain.MemoryTypePattern, "context": domain.MemoryTypeContext,
		"preference": domain.MemoryTypePreference, "observation": domain.MemoryTypeObservation,
		"profile": domain.MemoryTypeFact,
	}

	for _, itemRaw := range rawItems {
		var loose map[string]interface{}
		if err := json.Unmarshal(itemRaw, &loose); err != nil {
			continue
		}

		item := domain.MemoryItem{
			Type:       domain.MemoryType(looseString(loose, "type", "kind", "memory_type")),
			Content:    looseString(loose, "content", "summary", "text", "value"),
			Scope:      domain.MemoryScopeType(looseString(loose, "scope")),
			PromoteTo:  domain.MemoryScopeType(looseString(loose, "promote_to")),
			Importance: looseFloat(loose, "importance", "confidence", "score"),
		}

		// Handle LLM variant: {"fact": "content...", "scope": "agent"}
		// where the memory type is used as the key holding the content string.
		if item.Content == "" {
			for key, val := range loose {
				if _, isType := validTypes[key]; isType {
					if s, ok := val.(string); ok && s != "" {
						item.Content = s
						item.Type = validTypes[key]
						break
					}
				}
			}
		}

		// Skip items with no content
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		// Default importance
		if item.Importance <= 0 {
			item.Importance = 0.7
		}
		// Normalize type: map non-standard types to closest match
		if _, ok := validTypes[string(item.Type)]; !ok {
			item.Type = domain.MemoryTypeFact
		}

		memories = append(memories, item)
	}

	if len(memories) > 0 {
		shouldStore = true
	}

	return &domain.MemorySummaryResult{
		ShouldStore: shouldStore,
		Memories:    memories,
	}, nil
}

// looseString extracts a string value from a map, trying multiple key names.
func looseString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// looseFloat extracts a float value from a map, trying multiple key names.
func looseFloat(m map[string]interface{}, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case int:
				return float64(n)
			}
		}
	}
	return 0
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func mergeMetadataMaps(parts ...map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for _, part := range parts {
		for key, value := range part {
			result[key] = value
		}
	}
	return result
}

// MemoryEvolutionNode represents one step in a memory's evolution path.
type MemoryEvolutionNode struct {
	Memory     *domain.Memory         `json:"memory"`
	Children   []*MemoryEvolutionNode `json:"children,omitempty"`    // memories that supersede this one
	EvidenceOf *domain.Memory         `json:"evidence_of,omitempty"` // observation this is evidence for
}

// GetEvolution returns the full evolution graph rooted at the given memoryID.
// It traces both forward (SupersededBy chain) and backward (EvidenceIDs) to show
// how a raw Fact evolved through Reflect() into an Observation.
func (s *Service) GetEvolution(ctx context.Context, memoryID string) (*MemoryEvolutionNode, error) {
	root, err := s.store.Get(ctx, memoryID)
	if err != nil {
		return nil, fmt.Errorf("memory %s not found: %w", memoryID, err)
	}

	node := &MemoryEvolutionNode{Memory: root}

	// Forward: find any memory that supersedes this one
	if root.SupersededBy != "" {
		child, err := s.GetEvolution(ctx, root.SupersededBy)
		if err == nil {
			node.Children = []*MemoryEvolutionNode{child}
		}
	}

	// Backward: if this is an observation, find which observation (if any) it is evidence for
	// by scanning all observations for EvidenceIDs containing this ID
	if root.Type != domain.MemoryTypeObservation {
		all, _, err := s.store.List(ctx, 1000, 0)
		if err == nil {
			for _, m := range all {
				if m.Type == domain.MemoryTypeObservation {
					for _, eid := range m.EvidenceIDs {
						if eid == memoryID {
							node.EvidenceOf = m
							break
						}
					}
				}
				if node.EvidenceOf != nil {
					break
				}
			}
		}
	}

	return node, nil
}
