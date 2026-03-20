package chunker

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// DocumentType represents different types of documents
type DocumentType string

const (
	DocTypeCode      DocumentType = "code"
	DocTypeMarkdown  DocumentType = "markdown"
	DocTypePlainText DocumentType = "text"
	DocTypeHTML      DocumentType = "html"
	DocTypeJSON      DocumentType = "json"
	DocTypeXML       DocumentType = "xml"
	DocTypePDF       DocumentType = "pdf"
	DocTypeCSV       DocumentType = "csv"
	DocTypeLog       DocumentType = "log"
	DocTypeLegal     DocumentType = "legal"
	DocTypeMedical   DocumentType = "medical"
	DocTypeAcademic  DocumentType = "academic"
)

// AdaptiveChunker provides intelligent document-type-aware chunking
type AdaptiveChunker struct {
	baseService *Service
}

// NewAdaptiveChunker creates a new adaptive chunker
func NewAdaptiveChunker() *AdaptiveChunker {
	return &AdaptiveChunker{
		baseService: New(),
	}
}

// ChunkDocument chunks a document based on its type and content
func (ac *AdaptiveChunker) ChunkDocument(content string, filePath string, options domain.ChunkOptions) ([]string, error) {
	// Detect document type
	docType := ac.DetectDocumentType(content, filePath)

	// Apply type-specific chunking strategy
	switch docType {
	case DocTypeCode:
		return ac.chunkCode(content, options)
	case DocTypeMarkdown:
		return ac.chunkMarkdown(content, options)
	case DocTypeHTML:
		return ac.chunkHTML(content, options)
	case DocTypeJSON:
		return ac.chunkJSON(content, options)
	case DocTypeCSV:
		return ac.chunkCSV(content, options)
	case DocTypeLog:
		return ac.chunkLog(content, options)
	case DocTypeLegal:
		return ac.chunkLegal(content, options)
	case DocTypeMedical:
		return ac.chunkMedical(content, options)
	case DocTypeAcademic:
		return ac.chunkAcademic(content, options)
	default:
		// Fallback to standard sentence-based chunking
		return ac.baseService.Split(content, options)
	}
}

// DetectDocumentType detects the type of document based on content and file extension
func (ac *AdaptiveChunker) DetectDocumentType(content string, filePath string) DocumentType {
	// Check file extension first
	if filePath != "" {
		ext := strings.ToLower(filepath.Ext(filePath))
		switch ext {
		case ".go", ".py", ".js", ".ts", ".java", ".cpp", ".c", ".rs", ".rb", ".php":
			return DocTypeCode
		case ".md", ".markdown":
			return DocTypeMarkdown
		case ".html", ".htm":
			return DocTypeHTML
		case ".json":
			return DocTypeJSON
		case ".xml":
			return DocTypeXML
		case ".pdf":
			return DocTypePDF
		case ".csv":
			return DocTypeCSV
		case ".log":
			return DocTypeLog
		}
	}

	// Content-based detection
	if ac.isCode(content) {
		return DocTypeCode
	}
	if ac.isMarkdown(content) {
		return DocTypeMarkdown
	}
	if ac.isHTML(content) {
		return DocTypeHTML
	}
	if ac.isJSON(content) {
		return DocTypeJSON
	}
	if ac.isLegalDocument(content) {
		return DocTypeLegal
	}
	if ac.isMedicalDocument(content) {
		return DocTypeMedical
	}
	if ac.isAcademicDocument(content) {
		return DocTypeAcademic
	}

	return DocTypePlainText
}

// chunkCode chunks source code intelligently
func (ac *AdaptiveChunker) chunkCode(content string, options domain.ChunkOptions) ([]string, error) {
	var chunks []string

	// Split by functions/methods
	functionPattern := regexp.MustCompile(`(?m)^(func|def|function|class|interface|struct)\s+\w+`)
	matches := functionPattern.FindAllStringIndex(content, -1)

	if len(matches) > 0 {
		for i := 0; i < len(matches); i++ {
			start := matches[i][0]
			end := len(content)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}

			chunk := strings.TrimSpace(content[start:end])
			if len(chunk) > options.Size && options.Size > 0 {
				// If chunk is too large, split it further
				subChunks, _ := ac.baseService.Split(chunk, options)
				chunks = append(chunks, subChunks...)
			} else if chunk != "" {
				chunks = append(chunks, chunk)
			}
		}
	} else {
		// Fallback to line-based chunking for code
		lines := strings.Split(content, "\n")
		return ac.combineLineChunks(lines, options), nil
	}

	return chunks, nil
}

// chunkMarkdown chunks Markdown documents by headers and sections
func (ac *AdaptiveChunker) chunkMarkdown(content string, options domain.ChunkOptions) ([]string, error) {
	var chunks []string

	// Split by headers
	headerPattern := regexp.MustCompile(`(?m)^#{1,6}\s+.*$`)
	lines := strings.Split(content, "\n")

	var currentChunk strings.Builder
	var currentSize int

	for _, line := range lines {
		if headerPattern.MatchString(line) && currentChunk.Len() > 0 {
			// Start a new chunk at headers
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		lineSize := len(line)
		if options.Size > 0 && currentSize+lineSize > options.Size && currentChunk.Len() > 0 {
			// Current chunk is full
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
		currentSize += lineSize + 1
	}

	// Add remaining content
	if currentChunk.Len() > 0 {
		chunk := strings.TrimSpace(currentChunk.String())
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	return chunks, nil
}

// chunkHTML chunks HTML documents by semantic elements
func (ac *AdaptiveChunker) chunkHTML(content string, options domain.ChunkOptions) ([]string, error) {
	// Remove HTML tags for chunking (simple approach)
	tagPattern := regexp.MustCompile(`<[^>]+>`)
	textContent := tagPattern.ReplaceAllString(content, " ")

	// Use paragraph-based chunking for HTML content
	return ac.baseService.splitByParagraph(textContent, options)
}

// chunkJSON chunks JSON documents preserving structure
func (ac *AdaptiveChunker) chunkJSON(content string, options domain.ChunkOptions) ([]string, error) {
	// For JSON, try to preserve object boundaries
	var chunks []string

	// Simple approach: split by top-level objects
	if strings.HasPrefix(strings.TrimSpace(content), "[") {
		// Array of objects
		objectPattern := regexp.MustCompile(`\{[^{}]*\}`)
		matches := objectPattern.FindAllString(content, -1)

		for _, match := range matches {
			if len(match) <= options.Size || options.Size == 0 {
				chunks = append(chunks, match)
			} else {
				// Object too large, need to split
				subChunks, _ := ac.baseService.Split(match, options)
				chunks = append(chunks, subChunks...)
			}
		}
	} else {
		// Single object or complex structure
		return ac.baseService.Split(content, options)
	}

	if len(chunks) == 0 {
		return ac.baseService.Split(content, options)
	}

	return chunks, nil
}

// chunkCSV chunks CSV files by rows
func (ac *AdaptiveChunker) chunkCSV(content string, options domain.ChunkOptions) ([]string, error) {
	lines := strings.Split(content, "\n")

	if len(lines) == 0 {
		return []string{}, nil
	}

	// Keep header in each chunk
	header := lines[0]
	var chunks []string
	var currentChunk strings.Builder
	currentChunk.WriteString(header)
	currentChunk.WriteString("\n")
	currentSize := len(header)

	for i := 1; i < len(lines); i++ {
		line := lines[i]
		lineSize := len(line)

		if options.Size > 0 && currentSize+lineSize > options.Size && currentChunk.Len() > len(header)+1 {
			chunks = append(chunks, currentChunk.String())
			currentChunk.Reset()
			currentChunk.WriteString(header)
			currentChunk.WriteString("\n")
			currentSize = len(header)
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
		currentSize += lineSize + 1
	}

	if currentChunk.Len() > len(header)+1 {
		chunks = append(chunks, currentChunk.String())
	}

	return chunks, nil
}

// chunkLog chunks log files by timestamp or log entry
func (ac *AdaptiveChunker) chunkLog(content string, options domain.ChunkOptions) ([]string, error) {
	// Common log patterns
	timestampPattern := regexp.MustCompile(`(?m)^\d{4}[-/]\d{2}[-/]\d{2}|\[\d{4}[-/]\d{2}[-/]\d{2}`)
	lines := strings.Split(content, "\n")

	var chunks []string
	var currentChunk strings.Builder
	currentSize := 0

	for _, line := range lines {
		isNewEntry := timestampPattern.MatchString(line)
		lineSize := len(line)

		if isNewEntry && currentChunk.Len() > 0 &&
			(options.Size == 0 || currentSize+lineSize > options.Size) {
			// Start new chunk at log entry boundary
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
		currentSize += lineSize + 1
	}

	// Add remaining content
	if currentChunk.Len() > 0 {
		chunk := strings.TrimSpace(currentChunk.String())
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	return chunks, nil
}

// chunkLegal chunks legal documents preserving sections and clauses
func (ac *AdaptiveChunker) chunkLegal(content string, options domain.ChunkOptions) ([]string, error) {
	// Legal documents often have numbered sections
	sectionPattern := regexp.MustCompile(`(?m)^(\d+\.|\([a-z]\)|\([ivx]+\)|Article\s+\d+|Section\s+\d+)`)
	lines := strings.Split(content, "\n")

	var chunks []string
	var currentChunk strings.Builder
	currentSize := 0

	for _, line := range lines {
		isNewSection := sectionPattern.MatchString(line)
		lineSize := len(line)

		if isNewSection && currentChunk.Len() > 0 {
			// Preserve section boundaries
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		if options.Size > 0 && currentSize+lineSize > options.Size && currentChunk.Len() > 0 {
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
		currentSize += lineSize + 1
	}

	// Add remaining content
	if currentChunk.Len() > 0 {
		chunk := strings.TrimSpace(currentChunk.String())
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	return chunks, nil
}

// chunkMedical chunks medical documents preserving clinical structure
func (ac *AdaptiveChunker) chunkMedical(content string, options domain.ChunkOptions) ([]string, error) {
	// Medical documents often have specific sections
	sectionKeywords := []string{
		"CHIEF COMPLAINT", "HISTORY OF PRESENT ILLNESS", "PAST MEDICAL HISTORY",
		"MEDICATIONS", "ALLERGIES", "PHYSICAL EXAMINATION", "ASSESSMENT",
		"PLAN", "DIAGNOSIS", "TREATMENT", "PROGNOSIS", "FINDINGS",
	}

	// Create pattern for medical sections
	patternStr := "(?i)(?m)^(" + strings.Join(sectionKeywords, "|") + ")"
	sectionPattern := regexp.MustCompile(patternStr)

	// Use similar logic as legal documents
	lines := strings.Split(content, "\n")
	var chunks []string
	var currentChunk strings.Builder
	currentSize := 0

	for _, line := range lines {
		isNewSection := sectionPattern.MatchString(line)
		lineSize := len(line)

		if isNewSection && currentChunk.Len() > 0 {
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		if options.Size > 0 && currentSize+lineSize > options.Size && currentChunk.Len() > 0 {
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
		currentSize += lineSize + 1
	}

	if currentChunk.Len() > 0 {
		chunk := strings.TrimSpace(currentChunk.String())
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	return chunks, nil
}

// chunkAcademic chunks academic papers preserving structure
func (ac *AdaptiveChunker) chunkAcademic(content string, options domain.ChunkOptions) ([]string, error) {
	// Academic papers have standard sections
	sectionKeywords := []string{
		"ABSTRACT", "INTRODUCTION", "METHODOLOGY", "METHODS", "RESULTS",
		"DISCUSSION", "CONCLUSION", "REFERENCES", "BIBLIOGRAPHY",
		"LITERATURE REVIEW", "BACKGROUND", "RELATED WORK",
	}

	patternStr := "(?i)(?m)^(" + strings.Join(sectionKeywords, "|") + ")"
	sectionPattern := regexp.MustCompile(patternStr)

	lines := strings.Split(content, "\n")
	var chunks []string
	var currentChunk strings.Builder
	currentSize := 0

	for _, line := range lines {
		isNewSection := sectionPattern.MatchString(line)
		lineSize := len(line)

		if isNewSection && currentChunk.Len() > 0 {
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		if options.Size > 0 && currentSize+lineSize > options.Size && currentChunk.Len() > 0 {
			chunk := strings.TrimSpace(currentChunk.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			currentChunk.Reset()
			currentSize = 0
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
		currentSize += lineSize + 1
	}

	if currentChunk.Len() > 0 {
		chunk := strings.TrimSpace(currentChunk.String())
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	return chunks, nil
}

// Helper methods for content detection

func (ac *AdaptiveChunker) isCode(content string) bool {
	codePatterns := []string{
		`func\s+\w+\s*\(`, `def\s+\w+\s*\(`, `class\s+\w+`,
		`import\s+\w+`, `package\s+\w+`, `#include\s*<`,
		`if\s*\(.*\)\s*{`, `for\s*\(.*\)\s*{`,
	}

	for _, pattern := range codePatterns {
		if matched, _ := regexp.MatchString(pattern, content); matched {
			return true
		}
	}
	return false
}

func (ac *AdaptiveChunker) isMarkdown(content string) bool {
	mdPatterns := []string{
		`(?m)^#{1,6}\s+`,   // Headers
		`(?m)^\*\s+|\-\s+`, // Lists
		`\[.*\]\(.*\)`,     // Links
		`!\[.*\]\(.*\)`,    // Images
		`\*\*.*\*\*`,       // Bold
		"```",              // Code blocks
	}

	matchCount := 0
	for _, pattern := range mdPatterns {
		if matched, _ := regexp.MatchString(pattern, content); matched {
			matchCount++
		}
	}
	return matchCount >= 2
}

func (ac *AdaptiveChunker) isHTML(content string) bool {
	htmlPattern := regexp.MustCompile(`<(html|head|body|div|p|a|img|script|style)[^>]*>`)
	return htmlPattern.MatchString(content)
}

func (ac *AdaptiveChunker) isJSON(content string) bool {
	trimmed := strings.TrimSpace(content)
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

func (ac *AdaptiveChunker) isLegalDocument(content string) bool {
	legalKeywords := []string{
		"WHEREAS", "THEREFORE", "HEREIN", "THEREOF", "HEREBY",
		"AGREEMENT", "CONTRACT", "CLAUSE", "ARTICLE", "SECTION",
		"PLAINTIFF", "DEFENDANT", "COURT",
	}

	upperContent := strings.ToUpper(content)
	matchCount := 0
	for _, keyword := range legalKeywords {
		if strings.Contains(upperContent, keyword) {
			matchCount++
		}
	}
	return matchCount >= 3
}

func (ac *AdaptiveChunker) isMedicalDocument(content string) bool {
	medicalKeywords := []string{
		"PATIENT", "DIAGNOSIS", "TREATMENT", "MEDICATION",
		"SYMPTOMS", "EXAMINATION", "HISTORY", "ALLERGY",
		"PRESCRIPTION", "DOSAGE", "mg", "ml",
	}

	upperContent := strings.ToUpper(content)
	matchCount := 0
	for _, keyword := range medicalKeywords {
		if strings.Contains(upperContent, keyword) {
			matchCount++
		}
	}
	return matchCount >= 4
}

func (ac *AdaptiveChunker) isAcademicDocument(content string) bool {
	academicKeywords := []string{
		"ABSTRACT", "INTRODUCTION", "METHODOLOGY", "RESULTS",
		"CONCLUSION", "REFERENCES", "et al", "doi:",
		"HYPOTHESIS", "RESEARCH", "STUDY",
	}

	upperContent := strings.ToUpper(content)
	matchCount := 0
	for _, keyword := range academicKeywords {
		if strings.Contains(upperContent, keyword) {
			matchCount++
		}
	}
	return matchCount >= 3
}

// Helper method to combine line-based chunks
func (ac *AdaptiveChunker) combineLineChunks(lines []string, options domain.ChunkOptions) []string {
	var chunks []string
	var currentChunk strings.Builder
	currentSize := 0

	for _, line := range lines {
		lineSize := len(line)

		if options.Size > 0 && currentSize+lineSize > options.Size && currentChunk.Len() > 0 {
			chunks = append(chunks, currentChunk.String())
			currentChunk.Reset()
			currentSize = 0

			// Add overlap if specified
			if options.Overlap > 0 && len(chunks) > 0 {
				// Take last few lines as overlap
				overlapSize := 0
				for i := len(lines) - 1; i >= 0 && overlapSize < options.Overlap; i-- {
					currentChunk.WriteString(lines[i])
					currentChunk.WriteString("\n")
					overlapSize += len(lines[i]) + 1
				}
				currentSize = overlapSize
			}
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
		currentSize += lineSize + 1
	}

	if currentChunk.Len() > 0 {
		chunks = append(chunks, currentChunk.String())
	}

	return chunks
}
