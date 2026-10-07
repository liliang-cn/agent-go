package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAgent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "reviewer.md")
	os.WriteFile(p, []byte("---\ndescription: reviews\ntools: [fs_read, bash]\nmax_turns: 4\nparallel: true\n---\n\nYou review.\n"), 0o644)
	spec, err := parseAgent(p, "kit")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "kit:reviewer" || spec.Instructions != "You review." || spec.MaxTurns != 4 ||
		!spec.Parallel || len(spec.Tools) != 2 {
		t.Fatalf("got %+v", spec)
	}
}
