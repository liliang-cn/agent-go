package agent

import (
	"os"
	"path/filepath"
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
		// Failures: planning verb mid-sentence (the real-world stall seen with
		// the PPT agent — "let me" / "now let me" not at the line start).
		{"english_let_me_midsentence", "Great data! Let me check the pptx skill and discover available agents for PPT creation.", true},
		{"english_now_let_me", "Good, I have Dell stock data. Now let me use the skill_html-ppt skill to create the presentation.", true},
		{"english_im_going_to", "Got the numbers. I'm going to assemble the slides now.", true},
		// Passes: substantive answers
		{"english_done", "Read the README. It documents three CLI subcommands: build, test, deploy.", false},
		// Passes: the polite "let me know" closing is not a stall.
		{"english_let_me_know", "The three subcommands are build, test, deploy. Let me know if you want details.", false},
		// Passes: acknowledgment confirmations are not stalls.
		{"chinese_remember_ack", "我会记住这件事。", false},
		{"english_remember_ack", "Got it. I'll remember that for next time.", false},
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

func TestExtractGoalFilePaths(t *testing.T) {
	cases := []struct {
		name string
		goal string
		want []string
	}{
		{"absolute", "保存到 /Users/me/.agentgo/workspace/dell.html 完成", []string{"/Users/me/.agentgo/workspace/dell.html"}},
		{"home", "save it to ~/out/report.pdf please", []string{"~/out/report.pdf"}},
		{"cjk_no_space", "保存到/tmp/a.html", []string{"/tmp/a.html"}},
		{"url_excluded", "summarize https://example.com/report.html and write a doc", nil},
		{"source_path_excluded", "explain pkg/agent/runtime.go for me", nil},
		{"relative_excluded", "write to ./out/x.html", nil},
		{"none", "just write a ppt about dell", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractGoalFilePaths(tc.goal)
			if len(got) != len(tc.want) {
				t.Fatalf("goal %q → %v, want %v", tc.goal, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("goal %q → %v, want %v", tc.goal, got, tc.want)
				}
			}
		})
	}
}

func TestFileTaskMustWrite(t *testing.T) {
	lint := FileTaskMustWrite()
	write := []string{"mcp_websearch_search", "fs_write"}
	readonly := []string{"fs_list", "fs_read"}

	// Real files on disk for the artifact-verification (result, not attempt) cases.
	dir := t.TempDir()
	existing := filepath.Join(dir, "deck.html")
	if err := os.WriteFile(existing, []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	emptyFile := filepath.Join(dir, "empty.html")
	if err := os.WriteFile(emptyFile, nil, 0o644); err != nil {
		t.Fatalf("seed empty: %v", err)
	}
	missing := filepath.Join(dir, "missing.html")

	cases := []struct {
		name    string
		goal    string
		tools   []string
		wantErr bool
	}{
		// Artifact verification: explicit path in the goal → the file must exist.
		{"path_missing_even_with_write", "生成 HTML 保存到 " + missing, write, true},   // write "called" but truncated → no file → reject
		{"path_empty_file", "Save the deck to " + emptyFile, write, true},         // exists but empty → reject
		{"path_exists_nonempty", "Save the deck to " + existing, readonly, false}, // file is really there → pass
		// No explicit path → fall back to "was a write tool used?".
		{"zh_ppt_no_write", "dell的股价，写一个ppt", readonly, true},
		{"en_create_html_no_write", "Create an HTML slide deck about Dell stock.", readonly, true},
		{"zh_ppt_with_write", "dell的股价，写一个ppt", write, false},
		{"en_create_with_write", "Create an HTML slide deck about Dell stock.", write, false},
		// Passes: not a file-output task.
		{"zh_read_summarize", "读取 README 并总结要点", readonly, false},
		{"en_summarize_pdf", "Summarize the findings in report.pdf for me.", readonly, false},
		{"zh_write_function", "帮我写个排序函数", nil, false},
		{"empty_goal", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := lint.Check("All done.", LintContext{Goal: tc.goal, ToolCalls: tc.tools})
			if tc.wantErr && ok {
				t.Fatalf("expected lint to fail for goal %q tools %v, but it passed", tc.goal, tc.tools)
			}
			if !tc.wantErr && !ok {
				t.Fatalf("expected lint to pass for goal %q tools %v, but failed: %s", tc.goal, tc.tools, reason)
			}
		})
	}
}

// TestBuildAutoRegistersNoPlanningOnlyFinish pins the lib-level guarantee:
// every service built through the framework's Builder (the UI's agentService,
// every TeamManager agent — both go through builder.build()) gets the global
// no_planning_only_finish lint, so no agent can finish on a planning narration
// without the runtime rejecting + re-prompting.
func TestBuildAutoRegistersNoPlanningOnlyFinish(t *testing.T) {
	svc, err := New("plain-agent").
		WithPTC(false).
		WithConfig(testAgentConfig(t.TempDir())).
		WithLLM(&streamMemorySaveTestLLM{}).
		Build()
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	defer svc.Close()

	names := svc.OutputLints().Names("AnyAgent")
	if !containsString(names, "no_planning_only_finish") {
		t.Fatalf("Build() should auto-register no_planning_only_finish, got %v", names)
	}
	// Idempotent: an explicit RegisterDefaultOutputLints must not duplicate it.
	RegisterDefaultOutputLints(svc)
	count := 0
	for _, n := range svc.OutputLints().Names("AnyAgent") {
		if n == "no_planning_only_finish" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("no_planning_only_finish should be registered exactly once, got %d", count)
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
