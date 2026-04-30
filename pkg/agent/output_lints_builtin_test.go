package agent

import (
	"testing"
)

func TestDispatcherNoBounceBack(t *testing.T) {
	lint := DispatcherNoBounceBack()
	cases := []struct {
		name    string
		text    string
		wantErr bool
	}{
		// English bounce-back phrasings
		{"english_will_route", "I will route this to Operator.", true},
		{"english_let_me_dispatch", "Let me dispatch this request to the right agent.", true},
		{"english_routing_to", "Routing the task to Operator now.", true},
		{"english_handoff", "I'll hand off the task to the responder.", true},
		// Chinese bounce-back phrasings
		{"chinese_let_x_handle", "我会让 Operator 处理这个任务。", true},
		{"chinese_will_be_done_by", "接下来由 Operator 来完成。", true},
		{"chinese_dispatch_to", "我来转交给 Archivist 处理。", true},
		// Genuine completion answers (must pass)
		{"english_concrete_answer", "Done. The file has been written to /tmp/out.txt.", false},
		{"chinese_concrete_answer", "已完成：文件已写入 /tmp/out.txt。", false},
		{"empty", "", false},
		// Past-tense narration is fine — it describes what happened, not what will happen
		{"past_tense_routed", "The task was routed to Operator and completed.", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := lint.Check(tc.text, LintContext{AgentName: "Dispatcher"})
			if tc.wantErr && ok {
				t.Fatalf("expected lint to fail for %q, but it passed", tc.text)
			}
			if !tc.wantErr && !ok {
				t.Fatalf("expected lint to pass for %q, but failed: %s", tc.text, reason)
			}
		})
	}
}

func TestArchivistNoRelativeTime(t *testing.T) {
	lint := ArchivistNoRelativeTime()
	cases := []struct {
		name    string
		text    string
		wantErr bool
	}{
		// Failures: relative time without absolute date
		{"chinese_tomorrow_only", "已记下：明天和老王吃饭。", true},
		{"chinese_next_week", "已记下：下周一开会。", true},
		{"english_tomorrow_only", "Stored: meeting tomorrow at 6pm.", true},
		{"english_next_monday", "Stored: dentist appointment next Monday.", true},
		{"english_in_two_hours", "Stored: call back in 2 hours.", true},
		// Passes: relative phrase paired with absolute date is fine
		{"chinese_resolved", "已记下：明天（2026-04-30）18:00 与老王吃饭。", false},
		{"english_resolved", "Stored: meeting tomorrow (2026-04-30) at 6pm.", false},
		{"chinese_zh_date_format", "已记下：明天即 2026年4月30日 18:00 吃饭。", false},
		{"english_named_month", "Stored: meeting tomorrow, May 1, 2026 at 6pm.", false},
		// Passes: no time references at all
		{"plain_fact", "已记下：用户喜欢喝咖啡。", false},
		{"empty", "", false},
		// Passes: absolute-only date without relative reference
		{"absolute_only", "已记下：2026-05-01 18:00 与老王吃饭。", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := lint.Check(tc.text, LintContext{AgentName: "Archivist"})
			if tc.wantErr && ok {
				t.Fatalf("expected lint to fail for %q, but it passed", tc.text)
			}
			if !tc.wantErr && !ok {
				t.Fatalf("expected lint to pass for %q, but failed: %s", tc.text, reason)
			}
		})
	}
}

func TestNoPlanningOnlyFinish(t *testing.T) {
	lint := NoPlanningOnlyFinish()
	cases := []struct {
		name    string
		text    string
		wantErr bool
	}{
		// Failures: planning-only endings
		{"english_next_steps", "Next steps: read the README and summarize it.", true},
		{"english_will_do", "I will read the README and summarize it.", true},
		{"english_let_me", "Let me check the file structure first.", true},
		{"chinese_will_do", "我会去读一下 README 然后总结。", true},
		{"chinese_next_step", "接下来我会读取文件并总结要点。", true},
		// Passes: substantive answers
		{"english_done", "Read the README. It documents three CLI subcommands: build, test, deploy.", false},
		{"chinese_done", "已读取 README。它包含三个 CLI 子命令：build、test、deploy。", false},
		{"empty", "", false},
		// Passes: long answers that legitimately contain future-tense in the middle
		{
			name: "long_answer_with_will_in_middle",
			text: "Here are my findings on the current architecture, broken into three parts. " +
				"First, the runtime kernel is consolidated around a shared state machine. " +
				"Second, tools have a metadata model with ReadOnly / Destructive / ConcurrencySafe. " +
				"Third, PTC runs in a Goja sandbox. I will note that the streaming layer was recently refactored. " +
				"In summary, the framework already exhibits harness-engineering traits, but the lint registry was " +
				"only added in the most recent change. The next concrete piece worth doing is wiring the registry " +
				"into the dispatcher path so that bounce-back narration is rejected before it reaches the user, " +
				"as documented in PLAN.md. The complete diagram of how this fits together is shown above.",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := lint.Check(tc.text, LintContext{AgentName: "Operator"})
			if tc.wantErr && ok {
				t.Fatalf("expected lint to fail for %q, but it passed", tc.text)
			}
			if !tc.wantErr && !ok {
				t.Fatalf("expected lint to pass for %q, but failed: %s", tc.text, reason)
			}
		})
	}
}

func TestRegisterDefaultOutputLintsWiresAllThree(t *testing.T) {
	svc, err := New("default-lints").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&streamMemorySaveTestLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	RegisterDefaultOutputLints(svc)
	reg := svc.OutputLints()

	dispatcherNames := reg.Names(BuiltInDispatcherAgentName)
	if !containsString(dispatcherNames, "no_planning_only_finish") {
		t.Fatalf("Dispatcher should see global no_planning_only_finish, got %v", dispatcherNames)
	}
	if !containsString(dispatcherNames, "dispatcher_no_bounce_back") {
		t.Fatalf("Dispatcher should see dispatcher_no_bounce_back, got %v", dispatcherNames)
	}

	archivistNames := reg.Names("Archivist")
	if !containsString(archivistNames, "archivist_no_relative_time") {
		t.Fatalf("Archivist should see archivist_no_relative_time, got %v", archivistNames)
	}

	otherNames := reg.Names("Operator")
	if containsString(otherNames, "dispatcher_no_bounce_back") {
		t.Fatalf("Operator should NOT see dispatcher-specific lint, got %v", otherNames)
	}
	if containsString(otherNames, "archivist_no_relative_time") {
		t.Fatalf("Operator should NOT see archivist-specific lint, got %v", otherNames)
	}
	if !containsString(otherNames, "no_planning_only_finish") {
		t.Fatalf("Operator should see global no_planning_only_finish, got %v", otherNames)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
