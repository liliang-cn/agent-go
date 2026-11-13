package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func rankDef(name, desc string) domain.ToolDefinition {
	return domain.ToolDefinition{Type: "function", Function: domain.ToolFunction{Name: name, Description: desc}}
}

// keywordMatches is what the keyword search matched: any tool with any word.
func keywordMatches(all []domain.ToolDefinition, keywords []string) []domain.ToolDefinition {
	var out []domain.ToolDefinition
	for _, t := range all {
		text := strings.ToLower(t.Function.Name + " " + t.Function.Description)
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

func TestRankToolMatchesLongQueryKeepsFewAndBest(t *testing.T) {
	var all []domain.ToolDefinition
	for i := 0; i < 60; i++ {
		all = append(all, rankDef(fmt.Sprintf("tool_%02d", i), "Use this to list the items in the store and return them"))
	}
	all = append(all,
		rankDef("render_mermaid", "Render a Mermaid diagram to an SVG image"),
		rankDef("make_chart", "Draw a chart from rows of data and return the image"),
	)
	query := "please use the tool to make a mermaid diagram of the flow and return it as an image"
	kws := strings.Fields(query)
	matches := keywordMatches(all, kws)
	if len(matches) < 60 {
		t.Fatalf("setup: keyword search should match nearly all, got %d", len(matches))
	}
	got := rankToolMatches(matches, kws, len(all), maxToolSearchMatches)
	if len(got) == 0 || len(got) > maxToolSearchMatches {
		t.Fatalf("want 1..%d results, got %d", maxToolSearchMatches, len(got))
	}
	names := getToolNames(got)
	if len(got) != 2 || !strings.Contains(fmt.Sprint(names), "render_mermaid") {
		t.Fatalf("want render_mermaid and make_chart only, got %v", names)
	}
	for _, g := range got {
		if strings.HasPrefix(g.Function.Name, "tool_") {
			t.Fatalf("a tool that matched only common words was kept: %s", g.Function.Name)
		}
	}
}

func TestRankToolMatchesCJKSubstring(t *testing.T) {
	all := []domain.ToolDefinition{
		rankDef("list_events", "列出日程安排"),
		rankDef("create_alarm", "创建闹钟"),
		rankDef("list_tasks", "列出待办"),
	}
	kws := []string{"日程"}
	got := rankToolMatches(keywordMatches(all, kws), kws, len(all), maxToolSearchMatches)
	if len(got) != 1 || got[0].Function.Name != "list_events" {
		t.Fatalf("want list_events, got %v", getToolNames(got))
	}
}

func TestRankToolMatchesOnlyCommonWordsStillAnswers(t *testing.T) {
	// A query of nothing but common words is all the model gave: it gets
	// the best few rather than nothing.
	var all []domain.ToolDefinition
	for i := 0; i < 20; i++ {
		all = append(all, rankDef(fmt.Sprintf("store_%02d", i), "store the item"))
	}
	kws := []string{"store"}
	got := rankToolMatches(keywordMatches(all, kws), kws, len(all), maxToolSearchMatches)
	if len(got) != maxToolSearchMatches {
		t.Fatalf("want %d, got %d", maxToolSearchMatches, len(got))
	}
}
