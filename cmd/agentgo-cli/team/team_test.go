package team

import (
	"bytes"
	"testing"
)

func TestParseDelegatedTasks(t *testing.T) {
	isKnown := func(name string) bool {
		switch name {
		case "Captain", "Writer":
			return true
		default:
			return false
		}
	}

	tasks, err := parseDelegatedTasks("@Writer 写一个 hello world", isKnown)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 || tasks[0].AgentName != "Writer" || tasks[0].Instruction != "写一个 hello world" {
		t.Fatalf("unexpected tasks: %+v", tasks)
	}
}

func TestParseDelegatedTasksWithLeadingSharedMentions(t *testing.T) {
	isKnown := func(name string) bool {
		switch name {
		case "Captain", "Writer":
			return true
		default:
			return false
		}
	}

	tasks, err := parseDelegatedTasks("@Captain @Writer 总结当前仓库的 UI 和后端关系，并在 workspace 里写一份说明", isKnown)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %+v", tasks)
	}
	if tasks[0].AgentName != "Captain" || tasks[1].AgentName != "Writer" {
		t.Fatalf("unexpected agent order: %+v", tasks)
	}
	want := "总结当前仓库的 UI 和后端关系，并在 workspace 里写一份说明"
	if tasks[0].Instruction != want || tasks[1].Instruction != want {
		t.Fatalf("unexpected shared instruction: %+v", tasks)
	}
}

func TestParseDelegatedTasksWithoutMentionReturnsNil(t *testing.T) {
	tasks, err := parseDelegatedTasks("hello world", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected no tasks, got %+v", tasks)
	}
}

func TestTeamCommandHasGoSubcommand(t *testing.T) {
	if TeamCmd == nil {
		t.Fatal("expected TeamCmd")
	}
	cmd, _, err := TeamCmd.Find([]string{"go"})
	if err != nil {
		t.Fatalf("expected go subcommand to be found: %v", err)
	}
	if cmd == nil || cmd.Name() != "go" {
		t.Fatalf("unexpected command: %#v", cmd)
	}
}

func TestTeamCommandHasAgentAddSubcommand(t *testing.T) {
	if TeamCmd == nil {
		t.Fatal("expected TeamCmd")
	}
	cmd, _, err := TeamCmd.Find([]string{"agent", "add"})
	if err != nil {
		t.Fatalf("expected agent add subcommand to be found: %v", err)
	}
	if cmd == nil || cmd.Name() != "add" {
		t.Fatalf("unexpected command: %#v", cmd)
	}
}

func TestTeamCommandKeepsMemberAlias(t *testing.T) {
	if TeamCmd == nil {
		t.Fatal("expected TeamCmd")
	}
	cmd, _, err := TeamCmd.Find([]string{"member", "add"})
	if err != nil {
		t.Fatalf("expected member alias to be found: %v", err)
	}
	if cmd == nil || cmd.Name() != "add" {
		t.Fatalf("unexpected command via member alias: %#v", cmd)
	}
}

func TestDecodeInputRuneFromReaderHandlesChineseUTF8(t *testing.T) {
	r, n := decodeInputRuneFromReader(bytes.NewBufferString("\xbd\xa0"), 0xe4)
	if r != '你' {
		t.Fatalf("expected 你, got %q", r)
	}
	if n != 3 {
		t.Fatalf("expected 3-byte rune, got %d", n)
	}
}
