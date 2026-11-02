package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/domain"
)

func spillTestService(t *testing.T, limit int) *Service {
	t.Helper()
	svc := &Service{toolOutputLimit: limit, toolRegistry: NewToolRegistry(), spillDir: filepath.Join(t.TempDir(), "spill")}
	svc.registerSpillTool()
	t.Cleanup(svc.closeSpill)
	return svc
}

func spillRound(t *testing.T, svc *Service, tool string, res interface{}) (string, ToolOutputTruncation, bool) {
	t.Helper()
	call := domain.ToolCall{ID: "c1", Type: "function", Function: domain.FunctionCall{Name: tool}}
	msgs, cuts := svc.appendToolRoundToMessages(nil, "", &domain.GenerationResult{ToolCalls: []domain.ToolCall{call}},
		[]ToolExecutionResult{{ToolCallID: "c1", ToolName: tool, Result: res}})
	if len(cuts) == 0 {
		return msgs[1].Content, ToolOutputTruncation{}, false
	}
	return msgs[1].Content, cuts[0], true
}

func readSpill(t *testing.T, svc *Service, args map[string]interface{}) map[string]interface{} {
	t.Helper()
	return svc.spillRead(args)
}

func TestSpillOverLimitGivesPreviewAndID(t *testing.T) {
	big := numberedText(5000, "payload")
	tests := []struct {
		name     string
		res      interface{}
		spillOff bool
		spilled  bool
	}{
		{"text spills", big, false, true},
		{"disabled falls back to lossy cut", big, true, false},
		{"under limit untouched", "small", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := spillTestService(t, 3000)
			svc.spillDisabled = tc.spillOff
			msg, cut, cutOK := spillRound(t, svc, "any", tc.res)
			if tc.name == "under limit untouched" {
				if cutOK || msg != "small" {
					t.Fatalf("small result changed: %q", msg)
				}
				return
			}
			if !cutOK || cut.Spilled != tc.spilled {
				t.Fatalf("cut = %+v", cut)
			}
			if len(msg) > 3000 {
				t.Fatalf("preview is %d bytes, limit 3000", len(msg))
			}
			if tc.spilled {
				ids := spillIDsIn(msg)
				if len(ids) != 1 || ids[0] != cut.SpillID {
					t.Fatalf("ids in preview = %v, cut id %q", ids, cut.SpillID)
				}
				if !strings.Contains(msg, "1\t") && !strings.Contains(msg, "payload") {
					t.Fatal("no head in preview")
				}
				if !strings.Contains(msg, spillToolName) || !strings.Contains(msg, "lines") {
					t.Fatalf("preview lacks hint/sizes: %s", msg)
				}
				if cut.TotalLines < 5000 {
					t.Fatalf("TotalLines = %d", cut.TotalLines)
				}
			} else if len(spillIDsIn(msg)) != 0 {
				t.Fatal("a lossy cut mentioned a spill id")
			}
		})
	}
}

func TestSpillStructuredResultStaysJSON(t *testing.T) {
	svc := spillTestService(t, 4000)
	res := map[string]interface{}{"ok": true, "data": map[string]interface{}{"content": numberedText(3000, "x")}}
	msg, cut, ok := spillRound(t, svc, "fs_read", res)
	if !ok || !cut.Spilled || !cut.StructureKept {
		t.Fatalf("cut = %+v", cut)
	}
	if !strings.HasPrefix(msg, "{") || !strings.Contains(msg, cut.SpillID) {
		t.Fatalf("not a JSON preview with id: %.200s", msg)
	}
}

func TestSpillReadPagesAndGreps(t *testing.T) {
	svc := spillTestService(t, 3000)
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		if i%250 == 0 {
			sb.WriteString("NEEDLE here\n")
		} else {
			sb.WriteString("line filler text\n")
		}
	}
	_, cut, _ := spillRound(t, svc, "any", sb.String())
	id := cut.SpillID

	tests := []struct {
		name string
		args map[string]interface{}
		want []string
		not  []string
		key  string
	}{
		{"offset", map[string]interface{}{"id": id, "offset": 250, "limit": 3}, []string{"   251\tNEEDLE here", "   253\t"}, []string{"   254\t"}, "next_offset"},
		{"grep", map[string]interface{}{"id": id, "grep": "needle", "ignore_case": true}, []string{"     1\tNEEDLE", "   251\tNEEDLE", "   751\tNEEDLE"}, []string{"filler"}, "matches"},
		{"grep max", map[string]interface{}{"id": id, "grep": "NEEDLE", "max_matches": 2}, []string{"2 of 4 matching"}, nil, "matches"},
		{"bytes", map[string]interface{}{"id": id, "unit": "bytes", "offset": 0, "limit": 12}, []string{"NEEDLE here\n"}, []string{"line filler"}, "next_offset"},
		{"unknown id", map[string]interface{}{"id": "spill_00000000"}, nil, nil, "error"},
		{"missing id", map[string]interface{}{}, nil, nil, "error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := readSpill(t, svc, tc.args)
			if tc.key == "error" {
				if out["ok"] != false {
					t.Fatalf("want error, got %v", out)
				}
				return
			}
			data := out["data"].(map[string]interface{})
			content := data["content"].(string)
			for _, w := range tc.want {
				if !strings.Contains(content, w) {
					t.Errorf("content lacks %q:\n%s", w, content)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(content, n) {
					t.Errorf("content has %q:\n%s", n, content)
				}
			}
			if _, ok := data[tc.key]; !ok {
				t.Errorf("missing %q in %v", tc.key, data)
			}
		})
	}
}

func TestSpillReadResultIsBoundedAndNeverRespills(t *testing.T) {
	svc := spillTestService(t, 3000)
	_, cut, _ := spillRound(t, svc, "any", strings.Repeat("a long line of text ", 20000)+"\n"+numberedText(9000, "z"))
	out := readSpill(t, svc, map[string]interface{}{"id": cut.SpillID, "limit": 100000})
	data := out["data"].(map[string]interface{})
	if n := len(data["content"].(string)); n > spillReadBudget+spillMaxLineLen+200 {
		t.Fatalf("read page is %d bytes", n)
	}
	// Even through a tiny service cap, tool_output_read results are not spilled again.
	msg, _, cutOK := spillRound(t, svc, spillToolName, out)
	if cutOK && strings.Contains(msg, "saved as") {
		t.Fatal("tool_output_read result was spilled again")
	}
}

func TestSpillIDSurvivesCompactionRendering(t *testing.T) {
	svc := spillTestService(t, 30000)
	msg, cut, _ := spillRound(t, svc, "any", numberedText(20000, "q"))
	// Put the id past the point where the summary renderer cuts.
	tool := domain.Message{Role: "tool", Content: strings.Repeat("x", 2500) + msg}
	rendered := renderMessagesForSummary([]domain.Message{tool})
	if !strings.Contains(rendered, cut.SpillID) {
		t.Fatalf("compaction transcript lost %s", cut.SpillID)
	}
	if out := readSpill(t, svc, map[string]interface{}{"id": cut.SpillID, "limit": 1}); out["ok"] != true {
		t.Fatalf("id no longer resolves: %v", out)
	}
}

func TestSpillStoreEvictionTTLAndCleanup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	now := time.Now()
	st := newSpillStore(dir, 100, time.Hour)
	st.now = func() time.Time { return now }

	a, err := st.put(strings.Repeat("a", 60))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	b, err := st.put(strings.Repeat("b", 60)) // over the 100 byte bound: a is evicted
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.get(a); err == nil {
		t.Error("oldest entry survived the size bound")
	}
	if _, err := os.Stat(st.path(a)); !os.IsNotExist(err) {
		t.Error("evicted file still on disk")
	}
	if _, err := st.put(strings.Repeat("c", 500)); err == nil {
		t.Error("an entry bigger than the whole store was accepted")
	}
	if _, _, err := st.get(b); err != nil {
		t.Errorf("fresh entry unreadable: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := st.get(b); err == nil {
		t.Error("expired entry still readable")
	}
	c, _ := st.put("keep")
	st.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("close left the directory behind (%s)", c)
	}
}

func TestSpillToolRegisteredByBuilder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tweak func(*Builder) *Builder
		want  bool
	}{
		{"default", func(b *Builder) *Builder { return b }, true},
		{"spill off", func(b *Builder) *Builder { return b.WithToolOutputSpill(false) }, false},
		{"cap off", func(b *Builder) *Builder { return b.WithToolOutputLimit(-1) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := New("spill").WithConfig(testAgentConfig(t.TempDir())).WithLLM(&constraintLLM{replies: []string{"ok"}})
			svc, err := tc.tweak(b).Build()
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			if has := svc.toolRegistry.Has(spillToolName); has != tc.want {
				t.Errorf("%s registered = %v, want %v", spillToolName, has, tc.want)
			}
			if svc.spillEnabled() != tc.want {
				t.Errorf("spillEnabled = %v", svc.spillEnabled())
			}
		})
	}
}
