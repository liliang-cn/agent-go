# Code mode

Some things are easier to build with a program than to write out by hand. A rack of
6 × 4 shelves with a box on each is a hundred-odd parts, but it is a dozen lines
of JavaScript with two loops. Code mode lets the model write that program, runs
it, checks what it built, and tells the model what is wrong until it is right.

```go
import (
	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/codemode/scene"
)

svc, err := agent.New("scene-builder").
	WithLLM(llm).
	WithCodeMode(scene.Scene3D(), agent.CodeModeAttempts(6)).
	Build()

out, err := svc.RunCode(ctx, "a rack of 6 bays × 4 shelves with a box on each shelf")
// out.OK        did the kept attempt pass the checks
// out.Artifact  the scene as JSON (render it in the browser with three.js)
// out.Code      the kept program, if you want to store or replay it
// out.History   every attempt with its report
```

## How the loop works

1. The mode becomes one tool (`build_scene` for the scene mode). Its argument is
   the whole program. Its description is the builder API.
2. The tool runs the program in a fresh sandbox, then runs the mode's checks.
3. The tool result is a short report: errors with line numbers (and the line
   itself), warnings, measurements (counts, bounding box, group sizes) and
   console output.
4. The model reads it and calls the tool again with a fixed program, or stops
   when the report passes and the measurements match the request.
5. Attempts are bounded (`CodeModeAttempts`, default 6). The best attempt is
   kept: the latest one that passed, or, if none did, the one with the fewest
   errors.
6. `RunCode` hands back the kept artifact as data. A lint rejects an answer that
   pastes the program, and, in a `RunCode` run, an answer given before anything
   was built.

The program cannot call tools. It only sees the mode's builder, `console.log`
and `assert(cond, msg)`.

## The sandbox

A fresh JavaScript VM (Goja) per run. Nothing from the host is bound: no
`require`, no file system, no network, no timers. Each run has a time limit
(default 2 s; an endless loop is stopped and the report points at the loop),
a call-stack limit, a cap on program size, output size and printed lines, and
`Math.random()` is seeded so the same program builds the same thing. Modes add
their own caps; the scene mode allows at most 5000 nodes.

## The scene mode

`scene.Scene3D()` gives the model:

```js
scene.box({w, h, d}, {x, y, z}, {name, color, material, rotation})
scene.cylinder({r, h}, position, opts)      // or {rTop, rBottom, h, segments}
scene.sphere({r}, position, opts)
scene.group(name, {position, rotation, scale}, () => { ... })
scene.material(name, {color, metalness, roughness, opacity, emissive})
```

Metres, y up, positions are centres, rotations in degrees. The output is a
plain node tree (`type`, `name`, `geometry`, `position`, `rotation`, `scale`,
`material`, `children`) plus named materials. Three.js runs only in the browser;
`examples/code-mode-scene/viewer.html` shows how little it takes to draw it.

The checks report the bounding box, counts per shape, the size of each group,
meshes whose bounding boxes overlap, and meshes that sink below the floor.
Options: `scene.NoOverlaps()` makes overlaps errors, `scene.MaxSize(m)` caps the
size, `scene.MaxNodes(n)`, `scene.AllowBelowGround()`, and
`scene.WithValidator(fn)` adds your own check (use `scene.Parse` to read the
artifact).

## Your own mode

A mode is a struct. The builder is a JavaScript prelude that records what the
program builds, and `Export` is the expression that reads it out:

```go
mode := &codemode.Mode{
	Name:    "floorplan",
	Tool:    "build_floorplan",
	Summary: "Lay out a factory floor.",
	Docs:    "plan.machine(kind, {x, z}, {rotation}) ...",
	Prelude: floorplanJS,            // defines `plan`, keeps a list
	Export:  "__plan.export()",
	Validators: []codemode.Validator{checkAisles, checkClearance},
	Render:  screenshotWithHeadlessChrome, // optional; its notes go into the report
}
svc, _ := agent.New("planner").WithLLM(llm).WithCodeMode(mode).Build()
```

## Without a model

`codemode.Run(ctx, mode, code)` runs one program and returns the result and
its report; tests and hosts replaying a stored program use it. `codemode.Session`
and `codemode.Iterate(ctx, session, author)` run the same bounded loop with any
author you supply (your own model client, a script, a test double).

## Example

```bash
go run ./examples/code-mode-scene -offline          # built-in program, no model
LLM_BASE_URL=... LLM_API_KEY=... LLM_MODEL=... go run ./examples/code-mode-scene -serve
```

It writes `scene.json`, `program.js` and `scene.html` (a single file that loads
three.js from cdnjs) and, with `-serve`, serves them on port 43781.
