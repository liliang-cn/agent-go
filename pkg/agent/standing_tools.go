package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The three tools a wake uses. They find their responsibility through the
// run's task id — a wake's task id is the responsibility's id — so a call
// from any other run is refused rather than guessed at.

const (
	standingNotifyTool = "standing_notify"
	standingNoteTool   = "standing_note"
	standingWakeTool   = "standing_wake_me"
)

func (st *Standing) registerTools() {
	reg := st.svc.GetToolRegistry()
	if reg != nil && reg.Has(standingNotifyTool) {
		return // a second Standing over the same service shares them
	}
	st.svc.AddToolWithMetadata(standingNotifyTool,
		"Tell the person something they should know, now. Use it for what the responsibility says warrants attention, and not for routine status: a message about nothing teaches the person to ignore you.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"message": map[string]interface{}{"type": "string"}},
			"required":   []string{"message"},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			it, w, err := st.callerItem(ctx)
			if err != nil {
				return nil, err
			}
			msg := strings.TrimSpace(toolArgString(args, "message"))
			if msg == "" {
				return nil, errors.New("message is empty")
			}
			st.mu.Lock()
			w.Notified++
			st.mu.Unlock()
			st.notify(Notification{ResponsibilityID: it, WakeID: w.ID, Kind: NotifyMessage, Message: msg, At: st.clock()})
			return "delivered", nil
		},
		agent_ToolMetadataFor(standingNotifyTool),
	)
	st.svc.AddToolWithMetadata(standingNoteTool,
		"Replace your notes for the next wake. Write what it must know and would otherwise have to rediscover: what you checked, what you found, what you decided, what is pending. Keep what still matters from the notes you were given.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"notes": map[string]interface{}{"type": "string"}},
			"required":   []string{"notes"},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			id, _, err := st.callerItem(ctx)
			if err != nil {
				return nil, err
			}
			notes := strings.TrimSpace(toolArgString(args, "notes"))
			if err := st.update(ctx, id, func(it *standingItem) { it.r.Notes = notes }); err != nil {
				return nil, err
			}
			return "noted", nil
		},
		agent_ToolMetadataFor(standingNoteTool),
	)
	st.svc.AddToolWithMetadata(standingWakeTool,
		"Say when to wake you again. Give after_seconds (from now) or at (RFC 3339). A fixed schedule or an event may wake you sooner; this is the latest you want to wait.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"after_seconds": map[string]interface{}{"type": "integer", "minimum": 1},
				"at":            map[string]interface{}{"type": "string", "description": "RFC 3339 timestamp"},
			},
		},
		func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			id, _, err := st.callerItem(ctx)
			if err != nil {
				return nil, err
			}
			var when time.Time
			if s := strings.TrimSpace(toolArgString(args, "at")); s != "" {
				t, perr := time.Parse(time.RFC3339, s)
				if perr != nil {
					return nil, fmt.Errorf("at is not RFC 3339: %v", perr)
				}
				when = t
			} else if v, ok := args["after_seconds"].(float64); ok && v > 0 {
				when = st.clock().Add(time.Duration(v) * time.Second)
			} else {
				return nil, errors.New("give after_seconds or at")
			}
			if !when.After(st.clock()) {
				return nil, errors.New("that time has passed")
			}
			if err := st.update(ctx, id, func(it *standingItem) { it.r.NextWake = when }); err != nil {
				return nil, err
			}
			st.poke()
			return fmt.Sprintf("will wake at %s", when.Format(time.RFC3339)), nil
		},
		agent_ToolMetadataFor(standingWakeTool),
	)
}

// agent_ToolMetadataFor is the metadata of the standing tools: none is
// destructive, and none reads anything, so they are not ReadOnly either —
// a scan gets them by name, not by that flag.
func agent_ToolMetadataFor(string) ToolMetadata {
	return ToolMetadata{InterruptBehavior: InterruptBehaviorCancel}
}

// callerItem resolves the responsibility a tool call belongs to.
func (st *Standing) callerItem(ctx context.Context) (string, *Wake, error) {
	id := currentTaskID(getCurrentSession(ctx))
	st.mu.Lock()
	defer st.mu.Unlock()
	it, ok := st.items[id]
	if !ok || it.running == nil {
		return "", nil, errors.New("this tool belongs to a standing responsibility's wake; this run is not one")
	}
	return id, it.running, nil
}

// readOnlyTools is the allowlist of a scan: every registered tool that
// declared itself ReadOnly, and the standing tools. Tools that did not
// declare — and MCP tools, which live outside the registry — are left out,
// because a scan that can change something is not a scan.
func (st *Standing) readOnlyTools() []string {
	out := []string{standingNotifyTool, standingNoteTool, standingWakeTool}
	reg := st.svc.GetToolRegistry()
	if reg == nil {
		return out
	}
	for _, name := range reg.Names() {
		if reg.MetadataOf(name).ReadOnly {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
