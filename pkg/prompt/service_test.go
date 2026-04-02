package prompt

import (
	"context"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestPromptManager(t *testing.T) {
	m := NewManager()

	// 1. Test default registration and getting
	m.RegisterDefault("test.hello", "Hello {{.Name}}")
	assert.Equal(t, "Hello {{.Name}}", m.Get("test.hello"))

	// 2. Test rendering
	rendered, err := m.Render("test.hello", map[string]string{"Name": "AgentGo"})
	assert.NoError(t, err)
	assert.Equal(t, "Hello AgentGo", rendered)

	// 3. Test override
	m.SetPrompt("test.hello", "Hi {{.Name}}, how are you?")
	assert.Equal(t, "Hi {{.Name}}, how are you?", m.Get("test.hello"))

	rendered, err = m.Render("test.hello", map[string]string{"Name": "Li"})
	assert.NoError(t, err)
	assert.Equal(t, "Hi Li, how are you?", rendered)

	// 4. Test missing key
	_, err = m.Render("missing.key", nil)
	assert.Error(t, err)
}

func TestDefaultPromptsExist(t *testing.T) {
	m := NewManager()

	// Ensure core prompts are loaded by default
	assert.NotEmpty(t, m.Get(PlannerIntentRecognition))
	assert.NotEmpty(t, m.Get(PlannerSystemPrompt))
	assert.NotEmpty(t, m.Get(AgentVerification))
	assert.NotEmpty(t, m.Get(AgentSystemPrompt))
}

func TestPromptManager_ResolveSections(t *testing.T) {
	m := NewManager()
	m.RegisterSection("alpha", func(ctx context.Context, data interface{}) (Section, error) {
		return Section{Name: "alpha", Content: "A"}, nil
	})
	m.RegisterSection("beta", func(ctx context.Context, data interface{}) (Section, error) {
		return Section{Name: "beta", Content: "B", Dynamic: true}, nil
	})

	sections, err := m.ResolveSections(context.Background(), []string{"alpha", "beta"}, nil)
	assert.NoError(t, err)
	if assert.Len(t, sections, 2) {
		assert.Equal(t, "alpha", sections[0].Name)
		assert.Equal(t, "A", sections[0].Content)
		assert.False(t, sections[0].Dynamic)
		assert.Equal(t, "beta", sections[1].Name)
		assert.Equal(t, "B", sections[1].Content)
		assert.True(t, sections[1].Dynamic)
	}
}
