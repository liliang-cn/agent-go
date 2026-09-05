package agent

import (
	"context"
	"fmt"
	"strings"
)

// The agent's own hands on background work.
//
// Everything the agent can do is a tool, so this is what "start something and
// come back to it" looks like: three of them. A model that has these can
// answer now and finish later, which is the difference between an assistant
// that stalls for four minutes on a crawl and one that says "I have started
// it" and picks it up in the next turn.
//
// They are not registered by default. A background task is a whole run —
// its own budget, its own spend — and an agent that can start them without
// its author deciding so is an agent that can spend money in a loop. Turn
// them on with WithBackgroundTasks().

// RegisterBackgroundTaskTools registers background_start, background_check
// and background_cancel on a service. No-op if svc is nil.
func RegisterBackgroundTaskTools(svc *Service) {
	if svc == nil {
		return
	}
	has := func(name string) bool {
		return svc.toolRegistry != nil && svc.toolRegistry.Has(name)
	}
	// Starting one changes state — it spends money and may write files — so
	// it is destructive and must not be interrupted half-way.
	startMeta := ToolMetadata{Destructive: true, InterruptBehavior: InterruptBehaviorBlock}
	readMeta := ToolMetadata{ReadOnly: true, ConcurrencySafe: true, InterruptBehavior: InterruptBehaviorCancel}

	if !has("background_start") {
		svc.AddToolWithMetadata(
			"background_start",
			"Start a piece of work in the background and get an id back immediately, "+
				"instead of making the user wait. Use it for anything that will take "+
				"minutes rather than seconds — a long search, a build, a report over a lot "+
				"of data. Tell the user you have started it, then check it with "+
				"background_check in a later turn. Do not use it for something you can "+
				"just do now: a background task is a whole separate run.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"goal": map[string]interface{}{
						"type":        "string",
						"description": "What the background task should do, stated in full. It runs in a fresh session and cannot see this conversation, so include everything it needs.",
					},
					"label": map[string]interface{}{
						"type":        "string",
						"description": "A short name for it, so you can tell several apart later.",
					},
				},
				"required": []string{"goal"},
			},
			func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				goal := strings.TrimSpace(toolArgString(args, "goal"))
				if goal == "" {
					return toolErr("goal is required"), nil
				}
				task, err := svc.StartBackgroundTask(ctx, goal, WithBackgroundLabel(toolArgString(args, "label")))
				if err != nil {
					return toolErr(err.Error()), nil
				}
				return toolOK(map[string]interface{}{
					"id":     task.ID,
					"label":  task.Label,
					"status": string(task.Status),
					"note":   "started; check it with background_check, do not wait for it here",
				}), nil
			},
			startMeta,
		)
	}

	if !has("background_check") {
		svc.AddToolWithMetadata(
			"background_check",
			"Look at background work. With an id, report that one task and its result "+
				"if it has finished; with no id, list them all. A task that is still "+
				"running has no result yet — say so and check again later rather than "+
				"waiting.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "The task to look at. Omit to list every task.",
					},
				},
			},
			func(_ context.Context, args map[string]interface{}) (interface{}, error) {
				if id := strings.TrimSpace(toolArgString(args, "id")); id != "" {
					task, ok := svc.BackgroundTask(id)
					if !ok {
						return toolErr("no background task with id " + id), nil
					}
					return toolOK(backgroundTaskPayload(task, svc.runStatusPtr(task.RunID))), nil
				}
				tasks := svc.BackgroundTasks()
				// One pass over the run registry rather than a lookup per
				// task: the lock is the one every starting run also wants.
				live := svc.liveRunStatuses()
				payload := make([]map[string]interface{}, 0, len(tasks))
				for _, t := range tasks {
					var run *RunStatus
					if r, ok := live[t.RunID]; ok && t.RunID != "" {
						run = &r
					}
					payload = append(payload, backgroundTaskPayload(t, run))
				}
				return toolOK(map[string]interface{}{"tasks": payload, "count": len(payload)}), nil
			},
			readMeta,
		)
	}

	if !has("background_cancel") {
		svc.AddToolWithMetadata(
			"background_cancel",
			"Stop a background task that is still running. A task that already finished is not an error.",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string", "description": "The task to stop."},
				},
				"required": []string{"id"},
			},
			func(_ context.Context, args map[string]interface{}) (interface{}, error) {
				id := strings.TrimSpace(toolArgString(args, "id"))
				if id == "" {
					return toolErr("id is required"), nil
				}
				return toolOK(map[string]interface{}{"id": id, "stopped": svc.CancelBackgroundTask(id)}), nil
			},
			startMeta,
		)
	}
}

// backgroundTaskPayload renders a task for the model.
//
// The result is included only once the task is done. A partial answer from a
// run still in flight is the single most misleading thing this tool could
// return: a model that reads one reports it to the user as the answer.
func backgroundTaskPayload(t *BackgroundTask, run *RunStatus) map[string]interface{} {
	if t == nil {
		return nil
	}
	out := map[string]interface{}{
		"id":          t.ID,
		"status":      string(t.Status),
		"goal":        t.Goal,
		"running_for": t.Duration().Round(1e9).String(),
	}
	if t.Label != "" {
		out["label"] = t.Label
	}
	if !t.Status.Done() {
		// Progress, never a partial answer. The distinction is the whole
		// point: a model handed half an answer reports it as the answer, but
		// a model told only "still running" cannot tell work from a wedge,
		// and those call for opposite decisions — keep waiting, or give up
		// and do it itself.
		out["note"] = "still running; there is no result yet"
		out["progress"] = backgroundProgressPayload(run)
		return out
	}
	if t.Result != "" {
		out["result"] = t.Result
	}
	if t.Err != "" {
		out["error"] = t.Err
	}
	if t.StopReason != "" {
		out["stop_reason"] = string(t.StopReason)
	}
	out["finished_after"] = fmt.Sprintf("%s", t.Duration().Round(1e9))
	return out
}

// backgroundProgressPayload describes how far a running task has got. A task
// recorded but not yet in the loop is "starting" — reporting round 0 of 0
// would read as a run that has done nothing, which is a different thing.
func backgroundProgressPayload(run *RunStatus) map[string]interface{} {
	if run == nil || !run.Reported {
		return map[string]interface{}{"stage": "starting"}
	}
	progress := map[string]interface{}{
		"stage": run.Stage,
		"round": run.Round,
	}
	if run.MaxRounds > 0 {
		progress["max_rounds"] = run.MaxRounds
	}
	if run.ToolCalls > 0 {
		progress["tool_calls"] = run.ToolCalls
	}
	return progress
}
