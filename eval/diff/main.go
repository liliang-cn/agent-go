// Command diff compares two eval result files and exits non-zero when the
// newer one is worse where it is gated. It is a development tool for this
// repository, not library API:
//
//	make eval-diff A=eval/results/old.json B=eval/results/new.json
//	go run ./eval/diff -a old.json -b new.json
//
// Exit status: 0 no gate tripped, 1 the pass rate dropped, 2 bad arguments or
// unreadable input.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/liliang-cn/agent-go/v3/eval/runner"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("eval-diff", flag.ContinueOnError)
	a := fs.String("a", "", "old results JSON (baseline)")
	b := fs.String("b", "", "new results JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *a == "" || *b == "" {
		fmt.Fprintln(os.Stderr, "usage: eval-diff -a <old.json> -b <new.json>")
		return 2
	}
	oldFile, err := runner.LoadResultsFile(*a)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	newFile, err := runner.LoadResultsFile(*b)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Printf("A: %s (%s)\nB: %s (%s)\n\n", *a, oldFile.Timestamp, *b, newFile.Timestamp)
	report := runner.Diff(oldFile, newFile)
	fmt.Print(runner.FormatDiff(report))
	if report.Failed() {
		return 1
	}
	return 0
}
