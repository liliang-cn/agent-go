package memory

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

var (
	timeExpressionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(今天|明天|后天|今晚|明早|明晚|这周(?:[一二三四五六日天])?|本周(?:[一二三四五六日天])?|下周(?:[一二三四五六日天])?|周末|周[一二三四五六日天])(?:上午|下午|晚上|早上|中午)?(?:[0-9０-９]{1,2}[:：][0-9０-９]{2})?`),
		regexp.MustCompile(`(上午|下午|晚上|早上|中午)(?:[0-9０-９]{1,2}[:：][0-9０-９]{2})?`),
		regexp.MustCompile(`([0-9０-９]{1,2}[:：][0-9０-９]{2})`),
	}
	leadingSubjectPattern = regexp.MustCompile(`^([\p{Han}A-Za-z][\p{Han}A-Za-z0-9_-]{0,7}?)(?:要|会|去|跟|是|开|参加|处理|放假|上课|吃饭|开会|春游|旅行|旅游|提醒)`)
	companionPattern      = regexp.MustCompile(`(?:和|跟)([\p{Han}A-Za-z][\p{Han}A-Za-z0-9_-]{0,7})`)
	organizerWithPattern  = regexp.MustCompile(`(?:跟着|由)([\p{Han}A-Za-z][\p{Han}A-Za-z0-9_-]{0,7})`)
	timePrefixTrimPattern = regexp.MustCompile(`^(今天|明天|后天|今晚|明早|明晚|这周(?:[一二三四五六日天])?|本周(?:[一二三四五六日天])?|下周(?:[一二三四五六日天])?|周末|周[一二三四五六日天])(?:上午|下午|晚上|早上|中午)?(?:[0-9０-９]{1,2}[:：][0-9０-９]{2})?`)
)

var structuredEventStopWords = map[string]struct{}{
	"今天": {}, "明天": {}, "后天": {}, "今晚": {}, "明早": {}, "明晚": {},
	"这周": {}, "本周": {}, "下周": {}, "周末": {}, "周一": {}, "周二": {}, "周三": {}, "周四": {}, "周五": {}, "周六": {}, "周日": {}, "周天": {},
	"上午": {}, "下午": {}, "晚上": {}, "早上": {}, "中午": {},
	"我": {}, "我们": {}, "自己": {}, "用户": {}, "事情": {}, "安排": {}, "计划": {},
	"要": {}, "会": {}, "去": {}, "跟": {}, "是": {}, "开": {}, "参加": {}, "处理": {}, "放假": {}, "上课": {}, "吃饭": {},
}

func enrichStructuredMemory(memory *domain.Memory) {
	if memory == nil || strings.TrimSpace(memory.Content) == "" {
		return
	}

	event := storedOrDerivedEventMetadata(memory)
	if event == nil {
		return
	}

	memory.Metadata = domain.SetMemoryEventMetadata(memory.Metadata, event)
	memory.Metadata["structured_kind"] = "event"
	memory.Metadata["profiles"] = mergeUniqueStrings(
		event.SubjectProfiles,
		event.ParticipantProfiles,
		event.OrganizerProfiles,
	)
	memory.Keywords = mergeUniqueStrings(
		memory.Keywords,
		event.SubjectProfiles,
		event.ParticipantProfiles,
		event.OrganizerProfiles,
	)
	memory.Tags = mergeUniqueStrings(
		memory.Tags,
		[]string{"event", event.EventType, string(event.RelevanceToUser)},
	)
}

func storedOrDerivedEventMetadata(memory *domain.Memory) *domain.MemoryEventMetadata {
	if memory == nil {
		return nil
	}
	if event, ok := domain.GetMemoryEventMetadata(memory.Metadata); ok {
		return event
	}
	return deriveEventMetadata(memory.Content)
}

func deriveEventMetadata(text string) *domain.MemoryEventMetadata {
	text = normalizeStructuredText(text)
	if text == "" || !looksLikeStructuredEvent(text) {
		return nil
	}

	subjects := extractSubjectProfiles(text)
	participants := extractParticipantProfiles(text)
	organizers := extractOrganizerProfiles(text)
	correctionHints := extractCorrectionHints(text)

	meta := &domain.MemoryEventMetadata{
		Kind:                "event",
		EventType:           classifyEventType(text),
		TimeExpression:      extractTimeExpression(text),
		SubjectProfiles:     subjects,
		ParticipantProfiles: participants,
		OrganizerProfiles:   organizers,
		Correction:          len(correctionHints) > 0,
		CorrectionHints:     correctionHints,
	}

	applyUserInvolvement(text, meta)

	if meta.EventType == "" && meta.TimeExpression == "" &&
		len(meta.SubjectProfiles) == 0 && len(meta.ParticipantProfiles) == 0 &&
		len(meta.OrganizerProfiles) == 0 && !meta.Correction {
		return nil
	}

	return meta
}

func looksLikeStructuredEvent(text string) bool {
	lower := strings.ToLower(text)
	if containsAny(lower, []string{
		"安排", "计划", "日程", "会议", "开会", "启动会", "例会", "吃饭", "春游", "放假",
		"dashboard", "处理", "todo", "meeting", "appointment", "schedule", "plan",
	}) && containsAny(lower, []string{
		"今天", "明天", "后天", "这周", "本周", "下周", "周", "上午", "下午", "晚上", "早上",
		"today", "tomorrow", "week", "morning", "afternoon", "evening",
	}) {
		return true
	}
	if containsAny(lower, []string{
		"不用我", "我不用", "不需要我", "无需我", "不是我", "我不参加", "跟着学校", "跟学校",
	}) {
		return true
	}
	if len(extractSubjectProfiles(text)) > 0 && containsAny(lower, []string{
		"去", "会", "要", "参加", "放假", "吃饭", "开", "处理", "春游",
	}) {
		return true
	}
	return false
}

func extractTimeExpression(text string) string {
	for _, pattern := range timeExpressionPatterns {
		if match := pattern.FindString(text); strings.TrimSpace(match) != "" {
			return strings.TrimSpace(match)
		}
	}
	return ""
}

func extractSubjectProfiles(text string) []string {
	cleaned := strings.TrimSpace(timePrefixTrimPattern.ReplaceAllString(text, ""))
	cleaned = strings.TrimLeft(cleaned, " ，,：:")
	if hasStructuredVerbPrefix(cleaned) {
		return nil
	}

	match := leadingSubjectPattern.FindStringSubmatch(cleaned)
	if len(match) != 2 {
		return nil
	}

	candidate := normalizeProfileCandidate(match[1])
	if candidate == "" {
		return nil
	}
	return []string{candidate}
}

func extractParticipantProfiles(text string) []string {
	var participants []string
	for _, match := range companionPattern.FindAllStringSubmatch(text, -1) {
		if len(match) != 2 {
			continue
		}
		candidate := normalizeProfileCandidate(match[1])
		if candidate == "" {
			continue
		}
		participants = append(participants, candidate)
	}
	return dedupeStrings(participants)
}

func extractOrganizerProfiles(text string) []string {
	var organizers []string
	if strings.Contains(text, "学校") {
		organizers = append(organizers, "学校")
	}
	for _, match := range organizerWithPattern.FindAllStringSubmatch(text, -1) {
		if len(match) != 2 {
			continue
		}
		candidate := normalizeProfileCandidate(match[1])
		if candidate == "" {
			continue
		}
		organizers = append(organizers, candidate)
	}
	return dedupeStrings(organizers)
}

func extractCorrectionHints(text string) []string {
	var hints []string
	for _, marker := range []string{
		"不用我", "我不用", "不需要我", "无需我", "不是我", "我不参加", "我不用参与", "跟着学校", "跟学校",
	} {
		if strings.Contains(text, marker) {
			hints = append(hints, marker)
		}
	}
	return hints
}

func classifyEventType(text string) string {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, []string{"会议", "开会", "启动会", "例会", "meeting"}):
		return "meeting"
	case containsAny(lower, []string{"春游", "旅游", "旅行", "trip", "travel"}):
		return "trip"
	case containsAny(lower, []string{"放假", "holiday", "vacation"}):
		return "holiday"
	case containsAny(lower, []string{"吃饭", "聚餐", "dinner", "lunch"}):
		return "meal"
	case containsAny(lower, []string{"dashboard", "处理", "完成", "修", "写", "做"}):
		return "task"
	case containsAny(lower, []string{"安排", "计划", "日程", "plan", "schedule"}):
		return "plan"
	default:
		return ""
	}
}

func applyUserInvolvement(text string, event *domain.MemoryEventMetadata) {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, []string{
		"不用我", "我不用", "不需要我", "无需我", "不是我", "我不参加", "我不用参与",
	}):
		event.RequiresUser = false
		event.UserRole = domain.MemoryUserRoleNotInvolved
		event.RelevanceToUser = domain.MemoryEventRelevanceIndirect
	case containsAny(lower, []string{
		"我要", "我得", "我需要", "我会", "我去", "我和", "我跟", "需要我", "帮我", "my ", "i need", "i will",
	}):
		event.RequiresUser = true
		event.UserRole = domain.MemoryUserRoleParticipant
		event.RelevanceToUser = domain.MemoryEventRelevanceDirect
	case len(event.SubjectProfiles) > 0:
		event.RequiresUser = false
		event.UserRole = domain.MemoryUserRoleNotInvolved
		event.RelevanceToUser = domain.MemoryEventRelevanceIndirect
	default:
		event.RequiresUser = true
		event.UserRole = domain.MemoryUserRoleOwner
		event.RelevanceToUser = domain.MemoryEventRelevanceDirect
	}
}

// FilterMemoriesForQuery applies relation-aware schedule filtering to retrieved memories.
func FilterMemoriesForQuery(query string, memories []*domain.MemoryWithScore) []*domain.MemoryWithScore {
	if len(memories) == 0 {
		return memories
	}

	mode, targetProfile := detectRecallFilterMode(query, memories)
	if mode == recallFilterModeNone {
		return memories
	}

	filtered := make([]*domain.MemoryWithScore, 0, len(memories))
	for _, memory := range memories {
		if memory == nil || memory.Memory == nil {
			continue
		}

		event := storedOrDerivedEventMetadata(memory.Memory)
		switch mode {
		case recallFilterModeTargetProfile:
			if targetProfile == "" {
				continue
			}
			if event != nil && eventHasProfile(event, targetProfile) {
				filtered = append(filtered, memory)
				continue
			}
			if strings.Contains(strings.ToLower(memory.Content), strings.ToLower(targetProfile)) {
				filtered = append(filtered, memory)
			}
		case recallFilterModePersonalSchedule:
			if event == nil {
				filtered = append(filtered, memory)
				continue
			}
			if event.RequiresUser ||
				event.UserRole == domain.MemoryUserRoleOwner ||
				event.UserRole == domain.MemoryUserRoleParticipant ||
				event.RelevanceToUser == domain.MemoryEventRelevanceDirect {
				filtered = append(filtered, memory)
			}
		case recallFilterModeHouseholdSchedule:
			filtered = append(filtered, memory)
		}
	}

	return filtered
}

type recallFilterMode int

const (
	recallFilterModeNone recallFilterMode = iota
	recallFilterModePersonalSchedule
	recallFilterModeHouseholdSchedule
	recallFilterModeTargetProfile
)

func detectRecallFilterMode(query string, memories []*domain.MemoryWithScore) (recallFilterMode, string) {
	lower := strings.ToLower(strings.TrimSpace(query))
	if lower == "" {
		return recallFilterModeNone, ""
	}
	if !containsAny(lower, []string{
		"安排", "计划", "日程", "会议", "待办", "todo", "schedule", "plan", "agenda",
	}) {
		return recallFilterModeNone, ""
	}
	if containsAny(lower, []string{"家里", "家中", "family", "household", "我们家"}) {
		return recallFilterModeHouseholdSchedule, ""
	}

	profile := detectTargetProfile(query, memories)
	if profile != "" && !isUserSelfReference(profile) {
		return recallFilterModeTargetProfile, profile
	}
	if containsAny(lower, []string{"我", "我的", "my ", " me ", "i "}) {
		return recallFilterModePersonalSchedule, ""
	}

	return recallFilterModeNone, ""
}

func detectTargetProfile(query string, memories []*domain.MemoryWithScore) string {
	lowerQuery := strings.ToLower(query)
	profiles := collectKnownProfiles(memories)
	for _, profile := range profiles {
		if strings.Contains(lowerQuery, strings.ToLower(profile)) {
			return profile
		}
	}
	return ""
}

func collectKnownProfiles(memories []*domain.MemoryWithScore) []string {
	var profiles []string
	for _, memory := range memories {
		if memory == nil || memory.Memory == nil {
			continue
		}
		event := storedOrDerivedEventMetadata(memory.Memory)
		if event == nil {
			continue
		}
		profiles = append(profiles, event.SubjectProfiles...)
		profiles = append(profiles, event.ParticipantProfiles...)
		profiles = append(profiles, event.OrganizerProfiles...)
	}
	profiles = dedupeStrings(profiles)
	slices.SortFunc(profiles, func(a, b string) int {
		switch {
		case len(a) > len(b):
			return -1
		case len(a) < len(b):
			return 1
		default:
			return strings.Compare(a, b)
		}
	})
	return profiles
}

func eventHasProfile(event *domain.MemoryEventMetadata, profile string) bool {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if event == nil || profile == "" {
		return false
	}
	for _, group := range [][]string{event.SubjectProfiles, event.ParticipantProfiles, event.OrganizerProfiles} {
		for _, candidate := range group {
			if strings.EqualFold(strings.TrimSpace(candidate), profile) {
				return true
			}
		}
	}
	return false
}

func normalizeStructuredText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "\"'")
	return text
}

func normalizeProfileCandidate(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	candidate = strings.Trim(candidate, "，,。；;：:\"'()（）[]【】")
	candidate = strings.TrimLeft(candidate, "和跟")
	if candidate == "" || isUserSelfReference(candidate) {
		return ""
	}
	if _, blocked := structuredEventStopWords[candidate]; blocked {
		return ""
	}
	return candidate
}

func isUserSelfReference(candidate string) bool {
	switch strings.ToLower(strings.TrimSpace(candidate)) {
	case "我", "我们", "自己", "me", "myself", "i":
		return true
	default:
		return false
	}
}

func containsAny(text string, hints []string) bool {
	for _, hint := range hints {
		if hint != "" && strings.Contains(text, hint) {
			return true
		}
	}
	return false
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func hasStructuredVerbPrefix(text string) bool {
	for _, prefix := range []string{"要", "会", "去", "跟", "是", "开", "参加", "处理", "放假", "上课", "吃饭", "春游", "旅行", "旅游", "提醒"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func (s *Service) applyStructuredCorrections(ctx context.Context, memory *domain.Memory) {
	if s == nil || memory == nil || s.store == nil {
		return
	}

	correction := storedOrDerivedEventMetadata(memory)
	if correction == nil || !correction.Correction {
		return
	}

	query := buildCorrectionSearchQuery(memory, correction)
	if strings.TrimSpace(query) == "" {
		return
	}

	candidates, err := s.store.SearchByText(ctx, query, max(s.maxMemories*4, 12))
	if err != nil {
		return
	}

	for _, candidate := range candidates {
		if candidate == nil || candidate.Memory == nil || candidate.ID == memory.ID {
			continue
		}
		if !sameMemoryScope(candidate.Memory, memory) {
			continue
		}

		existing := storedOrDerivedEventMetadata(candidate.Memory)
		if !shouldApplyCorrection(candidate.Memory, existing, memory, correction) {
			continue
		}

		patched := *candidate.Memory
		patched.Metadata = domain.SetMemoryEventMetadata(patched.Metadata, mergeCorrection(existing, correction, memory.ID))
		patched.UpdatedAt = memory.CreatedAt

		if err := s.store.Update(ctx, &patched); err != nil {
			continue
		}
		if s.shadowIndex != nil && s.shadowIndex != s.store {
			_ = s.shadowIndex.Update(ctx, &patched)
		}
	}
}

func buildCorrectionSearchQuery(memory *domain.Memory, correction *domain.MemoryEventMetadata) string {
	if memory == nil || correction == nil {
		return ""
	}
	return strings.TrimSpace(memory.Content)
}

func shouldApplyCorrection(candidate *domain.Memory, existing *domain.MemoryEventMetadata, correctionMemory *domain.Memory, correction *domain.MemoryEventMetadata) bool {
	if candidate == nil || correctionMemory == nil || correction == nil {
		return false
	}
	if existing == nil {
		existing = deriveEventMetadata(candidate.Content)
	}
	if existing == nil {
		return false
	}
	if correction.EventType != "" && existing.EventType != "" && correction.EventType != existing.EventType {
		return false
	}
	if len(correction.SubjectProfiles) > 0 &&
		!shareAnyProfile(existing.SubjectProfiles, correction.SubjectProfiles) &&
		!shareAnyProfile(existing.ParticipantProfiles, correction.SubjectProfiles) {
		return false
	}
	if correction.TimeExpression != "" && existing.TimeExpression != "" && correction.TimeExpression != existing.TimeExpression {
		return false
	}
	return true
}

func mergeCorrection(existing *domain.MemoryEventMetadata, correction *domain.MemoryEventMetadata, correctionMemoryID string) *domain.MemoryEventMetadata {
	if existing == nil {
		existing = &domain.MemoryEventMetadata{Kind: "event"}
	}

	merged := *existing
	merged.Kind = "event"
	merged.OrganizerProfiles = dedupeStrings(append(append([]string(nil), existing.OrganizerProfiles...), correction.OrganizerProfiles...))
	merged.ParticipantProfiles = dedupeStrings(append(append([]string(nil), existing.ParticipantProfiles...), correction.ParticipantProfiles...))
	merged.SubjectProfiles = dedupeStrings(append(append([]string(nil), existing.SubjectProfiles...), correction.SubjectProfiles...))
	if correction.EventType != "" {
		merged.EventType = correction.EventType
	}
	if correction.TimeExpression != "" {
		merged.TimeExpression = correction.TimeExpression
	}
	if correction.UserRole != "" {
		merged.UserRole = correction.UserRole
	}
	if correction.RelevanceToUser != "" {
		merged.RelevanceToUser = correction.RelevanceToUser
	}
	merged.RequiresUser = correction.RequiresUser
	merged.UpdatedByMemoryID = correctionMemoryID
	merged.Correction = false
	merged.CorrectionHints = dedupeStrings(append(append([]string(nil), existing.CorrectionHints...), correction.CorrectionHints...))
	return &merged
}

func sameMemoryScope(left *domain.Memory, right *domain.Memory) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.TrimSpace(string(left.ScopeType)) == strings.TrimSpace(string(right.ScopeType)) &&
		strings.TrimSpace(left.ScopeID) == strings.TrimSpace(right.ScopeID) &&
		strings.TrimSpace(left.SessionID) == strings.TrimSpace(right.SessionID)
}

func shareAnyProfile(left []string, right []string) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(left))
	for _, item := range left {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		set[item] = struct{}{}
	}
	for _, item := range right {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if _, ok := set[item]; ok {
			return true
		}
	}
	return false
}
