package chunker

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

type Service struct{}

func New() *Service {
	return &Service{}
}

func (s *Service) Split(text string, options domain.ChunkOptions) ([]string, error) {
	if text == "" {
		return []string{}, nil
	}

	switch options.Method {
	case "sentence":
		return s.splitBySentence(text, options)
	case "paragraph":
		return s.splitByParagraph(text, options)
	case "token":
		return s.splitByToken(text, options)
	default:
		return nil, fmt.Errorf("%w: unknown method %s", domain.ErrChunkingFailed, options.Method)
	}
}

func (s *Service) splitBySentence(text string, options domain.ChunkOptions) ([]string, error) {
	sentences := s.splitIntoSentences(text)
	return s.combineChunks(sentences, options), nil
}

func (s *Service) splitByParagraph(text string, options domain.ChunkOptions) ([]string, error) {
	paragraphs := s.splitIntoParagraphs(text)
	var sentences []string
	for _, para := range paragraphs {
		sentences = append(sentences, s.splitIntoSentences(para)...)
	}
	return s.combineChunks(sentences, options), nil
}

func (s *Service) splitByToken(text string, options domain.ChunkOptions) ([]string, error) {
	words := s.splitIntoWords(text)
	return s.combineWordChunks(words, options), nil
}

func (s *Service) splitIntoSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{}
	}

	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		current.WriteRune(r)

		if s.isSentenceEnd(r) {
			// For sentence ending punctuation, check if this is actually end of sentence
			nextCharExists := i+1 < len(runes)

			// End sentence if:
			// 1. It's the last character, OR
			// 2. Next character is whitespace, OR
			// 3. Next character is uppercase (for English), OR
			// 4. Next character is another sentence ending punctuation, OR
			// 5. For Chinese/CJK, any following character (no space needed)
			isEnd := false
			if !nextCharExists {
				isEnd = true
			} else {
				nextChar := runes[i+1]
				if unicode.IsSpace(nextChar) || unicode.IsUpper(nextChar) || s.isSentenceEnd(nextChar) {
					isEnd = true
				} else {
					// For CJK characters, consider it sentence end unless next char is punctuation
					if s.isCJK(r) || s.isCJK(nextChar) {
						isEnd = !unicode.IsPunct(nextChar) || s.isSentenceEnd(nextChar)
					}
				}
			}

			if isEnd {
				sentence := strings.TrimSpace(current.String())
				if sentence != "" {
					sentences = append(sentences, sentence)
				}
				current.Reset()

				// Skip whitespace after sentence end
				for i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
					i++
				}
			}
		}
	}

	if current.Len() > 0 {
		sentence := strings.TrimSpace(current.String())
		if sentence != "" {
			sentences = append(sentences, sentence)
		}
	}

	return sentences
}

func (s *Service) splitIntoParagraphs(text string) []string {
	paragraphs := strings.Split(text, "\n\n")
	var result []string
	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para != "" {
			result = append(result, para)
		}
	}
	return result
}

func (s *Service) splitIntoWords(text string) []string {
	fields := strings.Fields(text)
	var words []string
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			words = append(words, field)
		}
	}
	return words
}

func (s *Service) combineChunks(sentences []string, options domain.ChunkOptions) []string {
	if len(sentences) == 0 {
		return []string{}
	}

	var chunks []string
	var currentChunk strings.Builder
	var currentLength int

	for _, sentence := range sentences {
		sentenceLength := len([]rune(sentence))

		// Check if adding this sentence would exceed the chunk size
		spaceNeeded := 0
		if currentChunk.Len() > 0 {
			spaceNeeded = 1 // for the space between sentences
		}

		if currentLength+spaceNeeded+sentenceLength > options.Size && currentChunk.Len() > 0 {
			// Current chunk is full, save it and start a new one
			chunks = append(chunks, strings.TrimSpace(currentChunk.String()))

			// Start new chunk with overlap if specified
			overlapText := s.getOverlapText(currentChunk.String(), options.Overlap)
			currentChunk.Reset()
			if overlapText != "" {
				currentChunk.WriteString(overlapText)
				currentLength = len([]rune(overlapText))

				// Add space before new sentence if we have overlap
				if currentLength > 0 {
					currentChunk.WriteString(" ")
					currentLength++
				}
			} else {
				currentLength = 0
			}
		} else if currentChunk.Len() > 0 {
			// Add space before new sentence
			currentChunk.WriteString(" ")
			currentLength++
		}

		// Add the current sentence
		currentChunk.WriteString(sentence)
		currentLength += sentenceLength
	}

	// Add the final chunk if it has content
	if currentChunk.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(currentChunk.String()))
	}

	return chunks
}

func (s *Service) combineWordChunks(words []string, options domain.ChunkOptions) []string {
	if len(words) == 0 {
		return []string{}
	}

	var chunks []string
	wordsPerChunk := options.Size / 5
	if wordsPerChunk < 1 {
		wordsPerChunk = 1
	}

	overlapWords := options.Overlap / 5
	if overlapWords < 0 {
		overlapWords = 0
	}

	for i := 0; i < len(words); {
		end := i + wordsPerChunk
		if end > len(words) {
			end = len(words)
		}

		chunk := strings.Join(words[i:end], " ")
		chunks = append(chunks, chunk)

		// Prevent infinite loop: ensure we always advance
		nextStart := end - overlapWords
		if nextStart <= i {
			nextStart = i + 1
		}
		i = nextStart
	}

	return chunks
}

func (s *Service) getOverlapText(text string, overlapSize int) string {
	if overlapSize <= 0 {
		return ""
	}

	runes := []rune(text)
	if len(runes) <= overlapSize {
		return text
	}

	overlapRunes := runes[len(runes)-overlapSize:]
	overlapText := string(overlapRunes)

	words := strings.Fields(overlapText)
	if len(words) > 1 {
		return strings.Join(words[1:], " ")
	}

	return overlapText
}

func (s *Service) isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？'
}

func (s *Service) isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Extension B
		(r >= 0x2A700 && r <= 0x2B73F) || // CJK Extension C
		(r >= 0x2B740 && r <= 0x2B81F) || // CJK Extension D
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // Halfwidth and Fullwidth Forms
}
