package agent

import (
	"context"
	"testing"

	"github.com/liliang-cn/agent-go/v2/pkg/ptc"
)

// The PTC router discovers MCP tools and skills by duck-typing the services it
// is handed. When the method is missing it silently falls back to the snapshot
// taken at Build time, so anything installed mid-conversation is invisible to
// the model — installed, running, unusable. These tests pin the method set,
// because a rename would reintroduce that failure with no compile error.

func TestMCPAdapterSatisfiesRouterToolLister(t *testing.T) {
	type toolLister interface {
		ListToolInfos(ctx context.Context) []ptc.ToolInfo
	}
	var a interface{} = &mcpToolAdapter{}
	if _, ok := a.(toolLister); !ok {
		t.Fatal("mcpToolAdapter must implement ListToolInfos(ctx) so the PTC router reads MCP tools live")
	}
	// Nil-safe: the router calls this on every turn.
	if got := (&mcpToolAdapter{}).ListToolInfos(context.Background()); got != nil {
		t.Errorf("a zero adapter should list nothing, got %v", got)
	}
}

func TestSkillsListerSatisfiesRouterContract(t *testing.T) {
	type skillLister interface {
		ListSkillInfos(ctx context.Context) []ptc.ToolInfo
	}
	type skillRunner interface {
		RunSkill(ctx context.Context, id string, vars map[string]interface{}) (string, error)
	}
	var l interface{} = &skillsToolLister{}
	if _, ok := l.(skillLister); !ok {
		t.Error("skillsToolLister must implement ListSkillInfos(ctx) for live skill discovery")
	}
	if _, ok := l.(skillRunner); !ok {
		t.Error("skillsToolLister must forward RunSkill, or the router cannot execute skills")
	}
	if got := (&skillsToolLister{}).ListSkillInfos(context.Background()); got != nil {
		t.Errorf("a zero lister should list nothing, got %v", got)
	}
	if _, err := (&skillsToolLister{}).RunSkill(context.Background(), "x", nil); err == nil {
		t.Error("running a skill with no service must error rather than panic")
	}
}
