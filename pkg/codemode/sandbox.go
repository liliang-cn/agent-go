package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
)

// ProgramFile is the file name a program runs under. Stack traces and line
// numbers in reports refer to it.
const ProgramFile = "program.js"

// programPrefix wraps the program in a function so a top-level `return` is
// legal and the program's declarations stay out of the global scope. It sits
// on the program's first line so line numbers are unchanged.
const programPrefix = "(function(){"

// ErrorKind says what stopped a program.
type ErrorKind string

const (
	ErrSyntax  ErrorKind = "syntax"  // the program did not parse
	ErrRuntime ErrorKind = "runtime" // it threw (including a failed assert or a builder argument check)
	ErrTimeout ErrorKind = "timeout" // it ran past the time limit or the run was cancelled
	ErrLimit   ErrorKind = "limit"   // it hit a size or depth cap
	ErrExport  ErrorKind = "export"  // it ran, but the artifact could not be read out
	ErrMode    ErrorKind = "mode"    // the mode itself is broken (prelude failed); not the program's fault
)

// ProgramError is why a program failed to produce an artifact.
type ProgramError struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
	Line    int       `json:"line,omitempty"`
	Column  int       `json:"column,omitempty"`
}

func (e *ProgramError) Error() string {
	if e == nil {
		return ""
	}
	if e.Line > 0 {
		return fmt.Sprintf("%s error at line %d: %s", e.Kind, e.Line, e.Message)
	}
	return fmt.Sprintf("%s error: %s", e.Kind, e.Message)
}

// Result is one run of one program.
type Result struct {
	// Attempt is the 1-based attempt number inside a Session; 0 for Run.
	Attempt int    `json:"attempt,omitempty"`
	Code    string `json:"-"`
	// Artifact is the built result as JSON, after Finalize. Nil when the
	// program failed before producing one.
	Artifact json.RawMessage `json:"artifact,omitempty"`
	// Error is set when the program did not run to completion.
	Error  *ProgramError `json:"error,omitempty"`
	Issues []Issue       `json:"issues,omitempty"`
	Stats  []Stat        `json:"stats,omitempty"`
	Logs   []string      `json:"logs,omitempty"`
	Render *Render       `json:"render,omitempty"`
	// Duration is how long the program ran (not counting validation).
	Duration time.Duration `json:"duration"`
}

// OK reports whether the program ran, produced an artifact and no validator
// found an error.
func (r *Result) OK() bool {
	return r != nil && r.Error == nil && len(r.Artifact) > 0 && r.ErrorCount() == 0
}

// ErrorCount counts error-severity issues, plus one for a program error.
func (r *Result) ErrorCount() int {
	if r == nil {
		return 0
	}
	n := 0
	if r.Error != nil {
		n++
	}
	for _, is := range r.Issues {
		if is.Severity == SeverityError {
			n++
		}
	}
	return n
}

// WarningCount counts warning-severity issues.
func (r *Result) WarningCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, is := range r.Issues {
		if is.Severity == SeverityWarning {
			n++
		}
	}
	return n
}

// Run executes one program against a mode and validates what it built. It
// needs no model; it is what a test, or a host replaying stored code, calls.
func Run(ctx context.Context, m *Mode, code string) *Result {
	res := &Result{Code: code}
	if m == nil {
		res.Error = &ProgramError{Kind: ErrMode, Message: "no mode"}
		return res
	}
	lim := m.Limits.withDefaults()
	if len(code) > lim.MaxCodeBytes {
		res.Error = &ProgramError{Kind: ErrLimit, Message: fmt.Sprintf("program is %d bytes; the limit is %d. Use loops and functions instead of listing every part", len(code), lim.MaxCodeBytes)}
		return res
	}
	if strings.TrimSpace(code) == "" {
		res.Error = &ProgramError{Kind: ErrSyntax, Message: "the program is empty"}
		return res
	}

	started := time.Now()
	raw, perr := execute(ctx, m, code, lim, &res.Logs)
	res.Duration = time.Since(started)
	if perr != nil {
		res.Error = perr
		return res
	}
	if len(raw) > lim.MaxArtifactBytes {
		res.Error = &ProgramError{Kind: ErrLimit, Message: fmt.Sprintf("the built result is %d bytes of JSON; the limit is %d", len(raw), lim.MaxArtifactBytes)}
		return res
	}

	for _, v := range m.Validators {
		if v == nil {
			continue
		}
		c := v(ctx, raw)
		res.Issues = append(res.Issues, c.Issues...)
		res.Stats = append(res.Stats, c.Stats...)
	}
	final := raw
	if m.Finalize != nil {
		f, err := m.Finalize(raw)
		if err != nil {
			res.Error = &ProgramError{Kind: ErrExport, Message: "finalize: " + err.Error()}
			return res
		}
		final = f
	}
	res.Artifact = final
	if m.Render != nil && res.ErrorCount() == 0 {
		if r, err := m.Render(ctx, final); err != nil {
			res.Render = &Render{Notes: "render failed: " + err.Error()}
		} else {
			res.Render = r
		}
	}
	return res
}

// execute runs prelude + program + export in a fresh VM and returns the
// artifact JSON.
func execute(ctx context.Context, m *Mode, code string, lim Limits, logs *[]string) (out json.RawMessage, perr *ProgramError) {
	prog, err := goja.Compile(ProgramFile, programPrefix+code+"\n})();", false)
	if err != nil {
		return nil, syntaxError(err)
	}
	var prelude *goja.Program
	if strings.TrimSpace(m.Prelude) != "" {
		prelude, err = goja.Compile(m.Name+"-api.js", m.Prelude, false)
		if err != nil {
			return nil, &ProgramError{Kind: ErrMode, Message: "prelude does not compile: " + err.Error()}
		}
	}

	vm := goja.New()
	vm.SetMaxCallStackSize(lim.MaxStackDepth)
	// A program that uses Math.random() builds the same thing every run, so
	// the model's revisions are the only thing that changes the result.
	rng := rand.New(rand.NewSource(1))
	vm.SetRandSource(rng.Float64)

	// Interrupt on the time limit or a cancelled run. Interrupt is the one
	// goja call that is safe from another goroutine.
	runCtx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-runCtx.Done():
			vm.Interrupt(runCtx.Err())
		case <-stop:
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			out, perr = nil, &ProgramError{Kind: ErrRuntime, Message: fmt.Sprintf("program crashed the sandbox: %v", r)}
		}
	}()

	installBuiltins(vm, lim, logs)
	for k, v := range m.Globals {
		if err := vm.Set(k, v); err != nil {
			return nil, &ProgramError{Kind: ErrMode, Message: fmt.Sprintf("global %q: %v", k, err)}
		}
	}
	if prelude != nil {
		if _, err := vm.RunProgram(prelude); err != nil {
			return nil, &ProgramError{Kind: ErrMode, Message: "prelude failed: " + err.Error()}
		}
	}
	if _, err := vm.RunProgram(prog); err != nil {
		return nil, runtimeError(err, ctx)
	}
	export := strings.TrimSpace(m.Export)
	if export == "" {
		return nil, &ProgramError{Kind: ErrMode, Message: "mode has no Export expression"}
	}
	v, err := vm.RunString("JSON.stringify(" + export + ")")
	if err != nil {
		if pe := runtimeError(err, ctx); pe.Kind == ErrTimeout {
			return nil, pe
		}
		return nil, &ProgramError{Kind: ErrExport, Message: err.Error()}
	}
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, &ProgramError{Kind: ErrExport, Message: "the program built nothing"}
	}
	s := v.String()
	if !json.Valid([]byte(s)) {
		return nil, &ProgramError{Kind: ErrExport, Message: "the built result is not valid JSON"}
	}
	return json.RawMessage(s), nil
}

// installBuiltins binds the only non-builder globals a program gets.
func installBuiltins(vm *goja.Runtime, lim Limits, logs *[]string) {
	dropped := 0
	logf := func(prefix string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			if len(*logs) >= lim.MaxLogLines {
				dropped++
				if dropped == 1 {
					*logs = append(*logs, fmt.Sprintf("(further output dropped; only %d lines are kept)", lim.MaxLogLines))
				}
				return goja.Undefined()
			}
			parts := make([]string, 0, len(call.Arguments))
			for _, a := range call.Arguments {
				parts = append(parts, formatLogValue(vm, a))
			}
			line := prefix + strings.Join(parts, " ")
			if len(line) > 300 {
				line = line[:300] + "…"
			}
			*logs = append(*logs, line)
			return goja.Undefined()
		}
	}
	console := vm.NewObject()
	_ = console.Set("log", logf(""))
	_ = console.Set("info", logf(""))
	_ = console.Set("warn", logf("warn: "))
	_ = console.Set("error", logf("error: "))
	_ = vm.Set("console", console)
	_ = vm.Set("assert", func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).ToBoolean() {
			return goja.Undefined()
		}
		msg := "assertion failed"
		if a := call.Argument(1); !goja.IsUndefined(a) {
			msg = "assertion failed: " + a.String()
		}
		panic(vm.NewGoError(errors.New(msg)))
	})
}

func formatLogValue(vm *goja.Runtime, v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if _, ok := v.Export().(string); ok {
		return v.String()
	}
	if obj, ok := v.(*goja.Object); ok && obj.ClassName() != "Function" {
		if js, ok := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify")); ok {
			if s, err := js(goja.Undefined(), v); err == nil && !goja.IsUndefined(s) {
				return s.String()
			}
		}
	}
	return v.String()
}

var linePosRe = regexp.MustCompile(`Line (\d+):(\d+)`)

func syntaxError(err error) *ProgramError {
	pe := &ProgramError{Kind: ErrSyntax, Message: err.Error()}
	var list parser.ErrorList
	var one *parser.Error
	switch {
	case errors.As(err, &list) && len(list) > 0:
		pe.Message, pe.Line, pe.Column = list[0].Message, list[0].Position.Line, list[0].Position.Column
	case errors.As(err, &one):
		pe.Message, pe.Line, pe.Column = one.Message, one.Position.Line, one.Position.Column
	default:
		if m := linePosRe.FindStringSubmatch(err.Error()); m != nil {
			pe.Line, _ = strconv.Atoi(m[1])
			pe.Column, _ = strconv.Atoi(m[2])
		}
	}
	if pe.Line == 1 && pe.Column > len(programPrefix) {
		pe.Column -= len(programPrefix)
	}
	return pe
}

func runtimeError(err error, ctx context.Context) *ProgramError {
	var intr *goja.InterruptedError
	if errors.As(err, &intr) {
		msg := "the program ran past the time limit and was stopped (an endless loop?)"
		if ctx.Err() != nil {
			msg = "the run was cancelled"
		}
		pe := &ProgramError{Kind: ErrTimeout, Message: msg}
		pe.Line, pe.Column = programPosition(intr.Stack())
		return pe
	}
	var so *goja.StackOverflowError
	if errors.As(err, &so) {
		pe := &ProgramError{Kind: ErrLimit, Message: "call stack too deep (runaway recursion?)"}
		pe.Line, pe.Column = programPosition(so.Stack())
		return pe
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		pe := &ProgramError{Kind: ErrRuntime, Message: exceptionMessage(ex)}
		pe.Line, pe.Column = programPosition(ex.Stack())
		if strings.Contains(pe.Message, "is not defined") && (strings.Contains(pe.Message, "require") || strings.Contains(pe.Message, "fetch") || strings.Contains(pe.Message, "setTimeout")) {
			pe.Message += " (the sandbox has no modules, network or timers)"
		}
		return pe
	}
	return &ProgramError{Kind: ErrRuntime, Message: err.Error()}
}

// exceptionMessage is the thrown value's message without goja's stack suffix.
func exceptionMessage(ex *goja.Exception) string {
	v := ex.Value()
	if v == nil {
		return ex.Error()
	}
	if obj, ok := v.(*goja.Object); ok {
		name, msg := obj.Get("name"), obj.Get("message")
		if msg != nil && !goja.IsUndefined(msg) {
			if name != nil && !goja.IsUndefined(name) && name.String() != "Error" && name.String() != "GoError" {
				return name.String() + ": " + msg.String()
			}
			return msg.String()
		}
	}
	return v.String()
}

// programPosition finds the innermost stack frame in the program itself, so a
// builder that rejects an argument points at the program line that passed it.
func programPosition(frames []goja.StackFrame) (line, col int) {
	for i := range frames {
		if frames[i].SrcName() != ProgramFile {
			continue
		}
		p := frames[i].Position()
		line, col = p.Line, p.Column
		if line == 1 && col > len(programPrefix) {
			col -= len(programPrefix)
		}
		return line, col
	}
	return 0, 0
}
