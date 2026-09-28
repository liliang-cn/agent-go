package main

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExitCodes(t *testing.T) {
	dir := t.TempDir()
	good := write(t, dir, "good.json", `{"results":[{"scenario":"a","runs":1,"pass":true,"pass_count":1},{"scenario":"b","runs":1,"pass":true,"pass_count":1}]}`)
	bad := write(t, dir, "bad.json", `{"results":[{"scenario":"a","runs":1,"pass":true,"pass_count":1},{"scenario":"b","runs":1,"pass":false,"fail_count":1}]}`)
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"same", []string{"-a", good, "-b", good}, 0},
		{"improved", []string{"-a", bad, "-b", good}, 0},
		{"degraded", []string{"-a", good, "-b", bad}, 1},
		{"missing arg", []string{"-a", good}, 2},
		{"unreadable", []string{"-a", good, "-b", filepath.Join(dir, "nope.json")}, 2},
	}
	for _, c := range cases {
		if got := run(c.args); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
}
