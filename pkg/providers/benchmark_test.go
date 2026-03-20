package providers

import (
	"context"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/domain"
)

// Test data structures for benchmarks
type BenchmarkPerson struct {
	Name        string   `json:"name"`
	Age         int      `json:"age"`
	City        string   `json:"city"`
	Occupation  string   `json:"occupation"`
	Hobbies     []string `json:"hobbies"`
	IsMarried   bool     `json:"is_married"`
	Email       string   `json:"email"`
	Phone       string   `json:"phone"`
	Description string   `json:"description"`
}

type BenchmarkCompany struct {
	Name        string             `json:"name"`
	Founded     int                `json:"founded"`
	Industry    string             `json:"industry"`
	Employees   []BenchmarkPerson  `json:"employees"`
	Revenue     float64            `json:"revenue"`
	Locations   []string           `json:"locations"`
	IsPublic    bool               `json:"is_public"`
	Website     string             `json:"website"`
	Description string             `json:"description"`
	Products    []BenchmarkProduct `json:"products"`
}

type BenchmarkProduct struct {
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
	InStock     bool    `json:"in_stock"`
	Description string  `json:"description"`
	Rating      float64 `json:"rating"`
	Reviews     int     `json:"reviews"`
}

// Simple schema for basic benchmarks
var personSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"name":        map[string]interface{}{"type": "string"},
		"age":         map[string]interface{}{"type": "integer"},
		"city":        map[string]interface{}{"type": "string"},
		"occupation":  map[string]interface{}{"type": "string"},
		"hobbies":     map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"is_married":  map[string]interface{}{"type": "boolean"},
		"email":       map[string]interface{}{"type": "string"},
		"phone":       map[string]interface{}{"type": "string"},
		"description": map[string]interface{}{"type": "string"},
	},
	"required": []string{"name", "age", "city", "occupation"},
}

// Complex nested schema

// Benchmark OpenAI-compatible structured output with simple data
func BenchmarkOpenAIStructuredSimple(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	config := &domain.OpenAIProviderConfig{
		BaseProviderConfig: domain.BaseProviderConfig{
			Type:    domain.ProviderOpenAI,
			Timeout: 30 * time.Second,
		},
		BaseURL:  "http://localhost:1234",
		APIKey:   "test-key",
		LLMModel: "qwen/qwen3-4b",
	}

	factory := NewFactory()
	provider, err := factory.CreateLLMProvider(context.Background(), config)
	if err != nil {
		b.Fatalf("Failed to create OpenAI provider: %v", err)
	}

	ctx := context.Background()
	opts := &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   500,
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var person BenchmarkPerson
			_, err := provider.GenerateStructured(
				ctx,
				"Generate information for a software developer named Alice, 28 years old, from San Francisco",
				&person,
				opts,
			)
			if err != nil {
				b.Errorf("GenerateStructured failed: %v", err)
			}
		}
	})
}

// Benchmark OpenAI with complex nested data
func BenchmarkOpenAIStructuredComplex(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	config := &domain.OpenAIProviderConfig{
		BaseProviderConfig: domain.BaseProviderConfig{
			Type:    domain.ProviderOpenAI,
			Timeout: 60 * time.Second,
		},
		BaseURL:  "http://localhost:1234",
		APIKey:   "test-key",
		LLMModel: "qwen/qwen3-4b",
	}

	factory := NewFactory()
	provider, err := factory.CreateLLMProvider(context.Background(), config)
	if err != nil {
		b.Fatalf("Failed to create OpenAI provider: %v", err)
	}

	ctx := context.Background()
	opts := &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   2000,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var company BenchmarkCompany
		_, err := provider.GenerateStructured(
			ctx,
			"Generate information for a tech company founded in 2010 with 5 employees and 3 products",
			&company,
			opts,
		)
		if err != nil {
			b.Errorf("GenerateStructured failed: %v", err)
		}
	}
}

// Benchmark Ollama-compatible structured output
func BenchmarkOllamaStructuredSimple(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	config := &domain.OpenAIProviderConfig{
		BaseProviderConfig: domain.BaseProviderConfig{
			Type:    domain.ProviderOpenAI,
			Timeout: 30 * time.Second,
		},
		BaseURL:  "http://localhost:11434",
		APIKey:   "ollama",
		LLMModel: "qwen3",
	}

	factory := NewFactory()
	provider, err := factory.CreateLLMProvider(context.Background(), config)
	if err != nil {
		b.Fatalf("Failed to create Ollama-compatible provider: %v", err)
	}

	ctx := context.Background()
	opts := &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   500,
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := provider.GenerateStructured(
				ctx,
				"Generate information for a software developer named Bob, 30 years old, from Seattle",
				personSchema,
				opts,
			)
			if err != nil {
				b.Errorf("GenerateStructured failed: %v", err)
			}
		}
	})
}

// Benchmark comparing traditional Generate vs GenerateStructured
func BenchmarkLMStudioTraditionalVsStructured(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	config := &domain.OpenAIProviderConfig{
		BaseProviderConfig: domain.BaseProviderConfig{
			Type:    domain.ProviderOpenAI,
			Timeout: 30 * time.Second,
		},
		BaseURL:  "http://localhost:1234",
		APIKey:   "test-key",
		LLMModel: "qwen/qwen3-4b",
	}

	factory := NewFactory()
	provider, err := factory.CreateLLMProvider(context.Background(), config)
	if err != nil {
		b.Fatalf("Failed to create LMStudio provider: %v", err)
	}

	ctx := context.Background()
	opts := &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   500,
	}

	b.Run("Traditional", func(b *testing.B) {
		prompt := `Generate JSON for a person with these fields: name, age, city, occupation, hobbies (array), is_married (boolean).
Example: {"name": "Alice", "age": 28, "city": "San Francisco", "occupation": "developer", "hobbies": ["coding", "reading"], "is_married": true}`

		for i := 0; i < b.N; i++ {
			_, err := provider.Generate(ctx, prompt, opts)
			if err != nil {
				b.Errorf("Generate failed: %v", err)
			}
		}
	})

	b.Run("Structured", func(b *testing.B) {
		prompt := "Generate information for a software developer"

		for i := 0; i < b.N; i++ {
			var person BenchmarkPerson
			_, err := provider.GenerateStructured(ctx, prompt, &person, opts)
			if err != nil {
				b.Errorf("GenerateStructured failed: %v", err)
			}
		}
	})
}

// Benchmark memory allocation
func BenchmarkStructuredOutputMemory(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping benchmark in short mode")
	}

	config := &domain.OpenAIProviderConfig{
		BaseProviderConfig: domain.BaseProviderConfig{
			Type:    domain.ProviderOpenAI,
			Timeout: 30 * time.Second,
		},
		BaseURL:  "http://localhost:1234",
		APIKey:   "test-key",
		LLMModel: "qwen/qwen3-4b",
	}

	factory := NewFactory()
	provider, err := factory.CreateLLMProvider(context.Background(), config)
	if err != nil {
		b.Fatalf("Failed to create LMStudio provider: %v", err)
	}

	ctx := context.Background()
	opts := &domain.GenerationOptions{
		Temperature: 0.1,
		MaxTokens:   500,
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var person BenchmarkPerson
		result, err := provider.GenerateStructured(
			ctx,
			"Generate information for a developer named Charlie",
			&person,
			opts,
		)
		if err != nil {
			b.Errorf("GenerateStructured failed: %v", err)
		}
		_ = result // Prevent optimization
	}
}
