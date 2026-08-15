package agent

import (
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/config"
	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

// verdictGenerator is a Generator that also reports a native web-search
// verdict, standing in for the pool.
type verdictGenerator struct {
	domain.Generator
	supported, known bool
}

func (g *verdictGenerator) NativeWebSearchVerdict() (bool, bool) { return g.supported, g.known }

func autoModeService(gen domain.Generator) *Service {
	return &Service{
		cfg: &config.Config{Tooling: config.ToolingConfig{
			WebSearch: config.WebSearchConfig{Mode: "auto"},
		}},
		llmService: gen,
	}
}

func TestAutoResolvesByProviderVerdict(t *testing.T) {
	cases := []struct {
		name           string
		gen            domain.Generator
		wantSurface    domain.WebSearchMode
		wantGeneration domain.WebSearchMode
		wantHideMCP    bool
	}{
		{
			// No verdict yet: both routes stay available and the options keep
			// being sent so evidence can accumulate.
			name:           "unknown stays auto",
			gen:            &verdictGenerator{},
			wantSurface:    domain.WebSearchModeAuto,
			wantGeneration: domain.WebSearchModeAuto,
			wantHideMCP:    false,
		},
		{
			// Proven native: the MCP search tools are redundant, but the
			// generation mode stays auto so the pool's strip-and-retry safety
			// net stays armed.
			name:           "supported surfaces native",
			gen:            &verdictGenerator{supported: true, known: true},
			wantSurface:    domain.WebSearchModeNative,
			wantGeneration: domain.WebSearchModeAuto,
			wantHideMCP:    true,
		},
		{
			// Proven unsupported: stop sending doomed parameters, keep the
			// MCP route.
			name:           "unsupported surfaces mcp",
			gen:            &verdictGenerator{supported: false, known: true},
			wantSurface:    domain.WebSearchModeMCP,
			wantGeneration: domain.WebSearchModeMCP,
			wantHideMCP:    false,
		},
		{
			// A generator that cannot report at all behaves like unknown.
			name:           "non-reporter stays auto",
			gen:            nil,
			wantSurface:    domain.WebSearchModeAuto,
			wantGeneration: domain.WebSearchModeAuto,
			wantHideMCP:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := autoModeService(tc.gen)
			if got := svc.webSearchSurfaceMode(); got != tc.wantSurface {
				t.Errorf("surface mode = %v, want %v", got, tc.wantSurface)
			}
			if got := svc.webSearchGenerationMode(); got != tc.wantGeneration {
				t.Errorf("generation mode = %v, want %v", got, tc.wantGeneration)
			}
			if got := svc.shouldHideMCPWebSearchTools(); got != tc.wantHideMCP {
				t.Errorf("hide MCP search tools = %v, want %v", got, tc.wantHideMCP)
			}
		})
	}
}

func TestAutoPromptNoteFollowsVerdict(t *testing.T) {
	native := autoModeService(&verdictGenerator{supported: true, known: true}).buildWebSearchPromptNote(nil)
	if !strings.Contains(native, "native web search capability") || strings.Contains(native, "fallback") {
		t.Fatalf("proven-native note should read as native, got %q", native)
	}
	unknown := autoModeService(&verdictGenerator{}).buildWebSearchPromptNote(nil)
	if !strings.Contains(unknown, "fallback") {
		t.Fatalf("unknown-verdict note should keep the fallback wording, got %q", unknown)
	}
}

// Explicit modes are configuration, not hypotheses: no verdict may override
// them.
func TestExplicitModesIgnoreVerdict(t *testing.T) {
	for _, mode := range []string{"native", "mcp", "off"} {
		svc := autoModeService(&verdictGenerator{supported: mode != "native", known: true})
		svc.cfg.Tooling.WebSearch.Mode = mode
		want := domain.NormalizeWebSearchMode(domain.WebSearchMode(mode))
		if got := svc.webSearchSurfaceMode(); got != want {
			t.Errorf("mode %s: surface = %v, want %v", mode, got, want)
		}
		if got := svc.webSearchGenerationMode(); got != want {
			t.Errorf("mode %s: generation = %v, want %v", mode, got, want)
		}
	}
}
