// Package codemode lets a model write a small JavaScript program that builds
// something — a 3D scene, a floor plan, a layout — instead of writing the
// thing out by hand. The program runs in a sandbox against a builder API the
// application supplies (a Mode), the result is checked, and a short report
// goes back to the model so it can fix the program and try again.
//
// The sandbox is a fresh Goja VM per run with nothing bound but the mode's
// builder, console.log and assert: no require, no file system, no network,
// no timers. Each run has a wall-clock limit, a call-stack limit and a cap on
// what the program may print; a mode adds its own caps (the scene builder
// limits how many nodes a program may create).
//
// The pieces:
//
//   - Mode: the builder API (a JavaScript prelude), the expression that reads
//     the built artifact out, docs for the model, and validators.
//   - Run: one program, no model — run it, validate it, get a Result.
//   - Session: many attempts at one task, bounded, keeping the best result.
//   - Iterate: drive a Session with any Author (a model, a script, a test).
//
// pkg/agent wires a Mode into an agent as a tool (Builder.WithCodeMode) so
// the agent's ordinary loop is the revise loop. pkg/codemode/scene is the
// first shipped mode: a three.js-shaped scene graph as plain JSON.
package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Mode is a domain API a program builds against. Applications describe their
// own builder with one of these; pkg/codemode/scene is an example.
type Mode struct {
	// Name identifies the mode ("scene3d"). Required.
	Name string
	// Tool is the tool name the model calls with its program. Defaults to
	// "run_" + Name.
	Tool string
	// Summary is one line saying what a program in this mode builds. It opens
	// the tool description.
	Summary string
	// Docs is the API reference the model writes against: the globals, their
	// arguments, units and conventions. Keep it short and exact.
	Docs string
	// Example is an optional short program shown with the docs.
	Example string

	// Prelude is JavaScript run before the program, in the same VM. It
	// defines the builder globals and keeps the state the program builds.
	Prelude string
	// Export is a JavaScript expression evaluated after the program. Its
	// JSON value is the artifact.
	Export string
	// Globals are Go values set in the VM before the prelude runs, for a
	// builder that needs host data (a catalogue of parts, a site plan).
	// Values must be plain data or Go functions; they are the only way the
	// program reaches the host, so do not hand it anything with side effects.
	Globals map[string]any

	// Validators check the artifact and measure it. They run in order, after
	// a run that produced an artifact; their issues and stats go into the
	// report.
	Validators []Validator
	// Render is an optional hook that turns the artifact into a picture
	// (a headless browser screenshot, say). Its notes go into the report and
	// its image is kept on the Result for the host.
	Render Renderer
	// Finalize optionally rewrites the artifact before it is handed out (to
	// strip bookkeeping the validators needed). It does not change what the
	// validators saw.
	Finalize func(artifact json.RawMessage) (json.RawMessage, error)

	// Limits bound one run. Zero fields take the defaults.
	Limits Limits
}

// ToolName is the tool the model calls with its program.
func (m *Mode) ToolName() string {
	if m == nil {
		return ""
	}
	if t := strings.TrimSpace(m.Tool); t != "" {
		return t
	}
	return "run_" + m.Name
}

// Validator checks an artifact. It returns the problems it found and any
// measurements worth showing the model (counts, sizes, bounds).
type Validator func(ctx context.Context, artifact json.RawMessage) Check

// Check is what a validator reports.
type Check struct {
	Issues []Issue
	Stats  []Stat
}

// Severity of an Issue. An error fails the attempt; a warning is reported
// but the attempt still counts as passing.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one problem with an artifact.
type Issue struct {
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	// Line is the program line that produced the offending part, when the
	// mode tracks it. Zero means unknown.
	Line int `json:"line,omitempty"`
	// Node names the offending part (a node name or path), when known.
	Node string `json:"node,omitempty"`
}

// Stat is one measurement shown to the model, in the order given.
type Stat struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Renderer turns an artifact into a picture. A host with a headless browser
// can screenshot its own viewer here; a host with a vision model can describe
// the picture in Notes so the model reading the report sees what it drew.
type Renderer func(ctx context.Context, artifact json.RawMessage) (*Render, error)

// Render is what a Renderer produces.
type Render struct {
	Image []byte `json:"-"`
	MIME  string `json:"mime,omitempty"`
	// Notes are put into the report as they are.
	Notes string `json:"notes,omitempty"`
}

// Limits bound one run of a program.
type Limits struct {
	// Timeout is the wall-clock limit for prelude, program and export
	// together. Default 2s.
	Timeout time.Duration
	// MaxStackDepth caps the JavaScript call stack. Default 256.
	MaxStackDepth int
	// MaxLogLines caps console.log lines kept. Default 40.
	MaxLogLines int
	// MaxCodeBytes rejects programs longer than this. Default 64 KiB.
	MaxCodeBytes int
	// MaxArtifactBytes rejects an artifact whose JSON is larger. Default 4 MiB.
	MaxArtifactBytes int
}

func (l Limits) withDefaults() Limits {
	if l.Timeout <= 0 {
		l.Timeout = 2 * time.Second
	}
	if l.MaxStackDepth <= 0 {
		l.MaxStackDepth = 256
	}
	if l.MaxLogLines <= 0 {
		l.MaxLogLines = 40
	}
	if l.MaxCodeBytes <= 0 {
		l.MaxCodeBytes = 64 << 10
	}
	if l.MaxArtifactBytes <= 0 {
		l.MaxArtifactBytes = 4 << 20
	}
	return l
}

// Describe is the tool description: what to build, the API, the rules of the
// loop. pkg/agent uses it as the tool's description.
func (m *Mode) Describe() string {
	var b strings.Builder
	if m.Summary != "" {
		b.WriteString(strings.TrimSpace(m.Summary))
		b.WriteString("\n\n")
	}
	b.WriteString("Write a JavaScript program (ES2020, no modules) that builds the result by calling the API below. ")
	b.WriteString("Use loops, functions and math rather than listing parts by hand. ")
	b.WriteString("The program runs in a sandbox: no require/import, no file system, no network, no timers; ")
	b.WriteString("console.log(...) output and assert(cond, msg) failures come back to you. ")
	b.WriteString("Each call returns a report: errors with line numbers, measurements and warnings. ")
	b.WriteString("If the report shows errors or the measurements do not match the request, fix the program and call this tool again with the whole program. ")
	b.WriteString("When it passes and matches the request, stop and reply with one or two sentences describing what was built. ")
	b.WriteString("Do not paste the program into the reply; the built result is handed over separately.\n\n")
	if m.Docs != "" {
		b.WriteString("API:\n")
		b.WriteString(strings.TrimSpace(m.Docs))
		b.WriteString("\n")
	}
	if m.Example != "" {
		b.WriteString("\nExample:\n")
		b.WriteString(strings.TrimSpace(m.Example))
		b.WriteString("\n")
	}
	return b.String()
}
