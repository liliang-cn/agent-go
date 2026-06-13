# AgentGo Manus Engine — Design Spec

Date: 2026-06-13
Status: approved, in implementation

## Goal

Give AgentGo the "hands and body" of a Manus/Hermes-style autonomous general agent:
an **isolated execution environment** (sandbox), a **full-featured browser**, **vision**,
**long-horizon autonomy**, and **deliverable output**. All capabilities are **optional,
pluggable, and zero-dependency-by-default** — a bare AgentGo install is unaffected; users
opt in via builder options.

This is a **framework capability enhancement**, not a product. New capabilities plug into
the framework core (`pkg/agent`), following existing patterns.

## Integration contract (existing patterns — do not deviate)

- **Tool registration**: `svc.AddToolWithMetadata(name, description, parameters, handler, ToolMetadata{...})`.
  Handler signature: `func(ctx context.Context, args map[string]interface{}) (interface{}, error)`.
  Tools **always** return a stable structured shape `{ "ok": bool, "data": ..., "error": "..." }`
  (so PTC's Goja sandbox can consume them). Never return freeform strings.
- **Tool state model**: `ToolMetadata{ReadOnly, ConcurrencySafe, Destructive, InterruptBehavior}`.
  `InterruptBehaviorCancel` for read-only/quick tools; `InterruptBehaviorBlock` for stateful/destructive.
- **Arg helpers**: `toolArgString(args,k)`, `toolArgInt(args,k)` (in `builtin_tools_datetime.go`); add `toolArgBool` if needed.
- **Opt-in registration funcs**: mirror `RegisterFetchURLTool(svc *Service)` — guard against
  double registration via `svc.toolRegistry.Has(name)`.
- **Builder**: chainable `New(name).WithXxx(...).Build()` in `pkg/agent/builder.go`.
- **PTY base**: reuse the `operator_sessions.go` PTY pattern (`creack/pty`) for shell sessions.
- Random high ports (3000+) for any dev/test server.

## Module breakdown

### ① `pkg/sandbox` — execution environment (NEW package)

```go
type Sandbox interface {
    Exec(ctx context.Context, req ExecRequest) (ExecResult, error)   // one-shot command
    Shell(ctx context.Context, opts ShellOpts) (Session, error)      // persistent PTY session
    WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error
    ReadFile(ctx context.Context, path string) ([]byte, error)
    Stat(ctx context.Context, path string) (FileInfo, error)
    List(ctx context.Context, path string) ([]FileInfo, error)
    Remove(ctx context.Context, path string, recursive bool) error
    Mkdir(ctx context.Context, path string) error
    Move(ctx context.Context, src, dst string) error
    Glob(ctx context.Context, pattern string) ([]string, error)
    Grep(ctx context.Context, pattern string, opts GrepOpts) ([]GrepHit, error)
    Workspace() string
    Snapshot(ctx context.Context) (string, error)   // tar.gz of workspace -> path
    Restore(ctx context.Context, snapshot string) error
    Close() error
}
```

- **`LocalSandbox`** (zero-dep): all paths jailed under a workspace root; reject `..` escape and
  absolute-path escape (`resolveInWorkspace` helper). `Exec`/`Shell` via `os/exec` + `creack/pty`,
  `context` timeout, and (unix) `syscall.Setrlimit`-style CPU/mem/proc caps via `SysProcAttr`.
  Workspace defaults to a `os.MkdirTemp` dir; configurable.
- **`DockerSandbox`**: implemented by **shelling out to the `docker` CLI** (no docker SDK dependency).
  Per-sandbox container, workspace bind-mounted, network policy (`none` / `bridge`), cpu/mem quotas
  (`--cpus`, `--memory`), `docker exec` for commands. Container lifecycle tied to `Close()`.
  Constructor probes for the `docker` binary; returns an error if absent so callers can fall back to Local.
- Both: `Snapshot`/`Restore` for workspace state (tar.gz), to pair with task checkpoints later.
- Tests: path-jail escapes rejected; exec timeout; read/write/list/move/glob/grep roundtrip;
  snapshot→restore roundtrip. Docker tests **skip if `docker` binary absent**.

### ② `pkg/agent` fs/shell tools — `RegisterSandboxTools(svc *Service, sb sandbox.Sandbox)` (NEW file `builtin_tools_sandbox.go`)

| tool | metadata | notes |
|---|---|---|
| `fs_read` | ReadOnly, ConcurrencySafe | line numbers, `offset`/`limit` |
| `fs_write` | Destructive | whole-file write |
| `fs_edit` | Destructive | exact unique-string replace (Claude-Code style); error if not unique |
| `fs_multi_edit` | Destructive | array of {old,new} applied atomically |
| `fs_list` | ReadOnly | |
| `fs_glob` | ReadOnly | |
| `fs_grep` | ReadOnly | |
| `fs_move` / `fs_remove` / `fs_mkdir` | Destructive | |
| `bash` | Destructive, InterruptBehaviorBlock | one-shot command w/ timeout |
| `shell_start` / `shell_send` / `shell_read` / `shell_interrupt` / `shell_stop` | Destructive/ReadOnly | persistent session via sandbox `Shell` |

### ③ `pkg/browser` — full browser (NEW package, chromedp)

```go
type Browser interface {
    Navigate(ctx, url string) (PageState, error)
    Back(ctx) error; Forward(ctx) error
    Click(ctx, ref string) error          // ref or CSS selector
    Type(ctx, ref, text string) error
    Fill(ctx, fields map[string]string) error
    Select(ctx, ref, value string) error
    Hover(ctx, ref string) error
    Scroll(ctx, dx, dy int) error
    WaitFor(ctx, cond WaitCond) error
    ReadText(ctx, selector string) (string, error)
    Snapshot(ctx) (Snapshot, error)       // accessibility tree w/ clickable refs
    Screenshot(ctx) ([]byte, error)       // PNG
    Evaluate(ctx, js string) (any, error)
    Console(ctx) ([]ConsoleMsg, error)
    Tabs(ctx) (TabOps, error)
    Download(ctx, url, destPath string) error
    Close() error
}
```

- **`ChromedpBrowser`**: holds an allocator + reusable browser context (stateful, multi-tab).
  Headless by default, configurable. `Download` writes into a caller-provided dir (wired to the
  sandbox workspace at the tool layer).
- Tests: drive against a local `httptest` server (random high port) serving a static page + a form
  page; assert navigate/read/click/type/screenshot.

### ④ `pkg/agent` browser tools — `RegisterBrowserTools(svc *Service, br browser.Browser, sb sandbox.Sandbox)` (NEW file `builtin_tools_browser_ctl.go`)

`browser_navigate`, `browser_back`, `browser_read`, `browser_snapshot`, `browser_click`,
`browser_type`, `browser_fill`, `browser_select`, `browser_scroll`, `browser_wait`,
`browser_screenshot` (ReadOnly, returns base64 PNG), `browser_evaluate`, `browser_console`,
`browser_download` (writes into sandbox workspace if `sb != nil`). Destructive ones use
`InterruptBehaviorBlock`.

### ⑤ Vision

`WithVision(true)` builder flag. `browser_screenshot` / image `fs_read` return image data that the
runtime injects as a multimodal image content part when the model supports vision. If runtime
message types are text-only today, add minimal image-part support; otherwise return base64 in the
tool result and document the limitation. (Implemented opportunistically; must not break text-only path.)

### ⑥ Long-horizon autonomy

- `WithAutonomy(AutonomyProfile{MaxRounds, CompactionThreshold, LintRetryBudget})`.
- New `scratchpad` tool (NEW file `builtin_tools_scratchpad.go`): agent maintains a todo/notes list
  scoped by task (backed by in-memory store keyed by task_id), so long runs don't lose the thread.
  `scratchpad_set` / `scratchpad_get` / `scratchpad_check` (mark item done).
- Reuse existing compaction; just make the threshold configurable.

### ⑦ Deliverables

- `RegisterDeliverableTools` + workspace scan: at task end, scan sandbox workspace → `[]Deliverable{Path,Type,Size}`.
- `Service.Deliverables(taskID)` accessor + CLI `agentgo task artifacts <id>` (added later if time).

### Builder options (in `builder.go`)

`WithSandbox(sb sandbox.Sandbox)`, `WithBrowser(br browser.Browser)`, `WithVision(on bool)`,
`WithAutonomy(p AutonomyProfile)`, `WithDeliverables(on bool)`. Each stores config on the Builder
and, in `build()`, registers the matching tool set on the Service. Store `sb`/`br` handles on the
`Service` struct for accessors + cleanup.

### Example

`examples/manus/main.go`: wire a Local sandbox (Docker if available) + chromedp browser + vision
into an agent; run an end-to-end task ("research a topic → browse pages → write a Markdown report +
screenshot into the workspace"); print the deliverables. Full imports + cleanup (`defer sb.Close()`,
`defer br.Close()`).

## Implementation phasing (each independently buildable)

- **P1**: `pkg/sandbox` (Local + Docker) + fs/shell tools + `WithSandbox`.
- **P2**: `pkg/browser` (chromedp) + browser tools + `WithBrowser`; browser downloads land in sandbox.
- **P3**: vision (`WithVision` + image content part if feasible).
- **P4**: autonomy (`WithAutonomy`, scratchpad tool, configurable compaction).
- **P5**: deliverables + example + eval scenario.

## Testing & verification

- `go build ./...` and `go vet ./...` clean.
- `go test ./pkg/sandbox/... ./pkg/browser/... ./pkg/agent/...` pass (`-race` for sandbox/session code).
- Docker + chromedp tests skip gracefully when the binary/Chrome is unavailable (CI-safe).
- Example compiles (`go build ./examples/manus`).

## Non-goals (this spec)

- Multi-tenant task scheduling / resource accounting (that was the "self-hosted service" path — out).
- Firecracker/gVisor backends (Docker is the strong-isolation backend here).
- Remote CDP browser backend (interface leaves room; not built now).
