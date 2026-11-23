# Changelog

## [Unreleased]

## [3.84.0] - 2026-11-23

### Added

- The trace says why a run did what it did, not only what it did. Three new
  records, each written on every path including the empty one:
  - `decision_engine` lines: every decision-engine consultation (gate, engine,
    label, confidence, floor, whether it was acted on and saved a call, error),
    including the answers that were ignored. A gate that never clears its
    floor is latency and nothing else, and only those lines show it.
  - `run_constraints` decision: the constraints a run ended up with and where
    they came from (`model_extracted`, `caller_declared`, `extraction_disabled`,
    `decision_gate_skipped`, `extraction_failed` with the cause). When a
    delivery lint blocks a run, the trace now names the deliverable it read
    into the goal. The goal itself is not recorded.
  - `inject_memories` decision: which memory ids reached the prompt, with
    score and scope, the scope chain the query could read, and how many
    candidates the scope filter and the cap dropped. Ids only, never content.
- `GenerationOptions.Reasoning` lets a structured call keep reasoning on.

### Fixed

- Constraint extraction never worked on a model that thinks before it
  answers. qwen3.5 on Ollama spent the whole token budget reasoning, returned
  `finish_reason: length` with no content, and the larger re-ask ran past the
  20s cap — 12 runs of 12 — so every run went on with no constraints and the
  delivery lints never fired. The `run_constraints` line above is what showed
  it. Structured calls now turn reasoning off from the first request, in both
  `pkg/pool` and `pkg/providers`, sending `reasoning_effort: "none"` beside
  `thinking: {"type": "disabled"}` (Ollama ignores the second and honours only
  the first). An upstream that refuses either field is asked again without
  it, once, and not sent it again (`retry_without_reasoning_off` on the
  trace). Measured: 1–3.5s per extraction instead of a timeout. Memory
  extraction and the timeaware resolver keep reasoning on: both run in the
  background, and extraction without it has misfiled what was said.
- `docs/trace-schema.md` lists every runtime decision the code writes;
  `untried_block`, `offer_installables`, `lint_rejected` and
  `deliverable_check` were missing.

## [3.80.3] - 2026-11-07

### Fixed

- A model told it could not read a calendar whose tools it was holding. When some
  tools of a family were in the schema and the rest deferred, the tool index listed
  the family as "mcp_noted_* (65 tools)" under tools that cannot be called yet, so
  the model searched for tools it already had; with a second search in the same
  batch refused, it gave up. The family line now says how many of its tools are
  already in the schema and to call those directly, and the refused-search message
  points at the schema before offering `task_blocked`.

## [3.80.2] - 2026-11-07

### Fixed

- A model without server-side tool search (OpenAI models, for example through a
  gateway serving a Codex account) could never load a deferred tool. Its calls to
  `tool_search_tool_regex` and `tool_search_tool_bm25` were "repaired" to the
  closest registered name, `tool_output_read`, and failed on that tool's
  arguments. The search names now reach dispatch unchanged.

## [3.80.1] - 2026-11-07

### Fixed

- Structured output against OpenAI models (for example through a gateway serving a
  Codex account) failed with `invalid_json_schema`: strict mode needs every object
  closed with `additionalProperties: false` and every property listed in `required`.
  The schema is now normalized just before it is sent, on a copy: optional
  properties become nullable, and a null answer decodes to the Go zero value.
  Gemini models accept the same schema.

## [3.80.0] - 2026-11-03

### Added

- Code mode. An agent can now build things by writing a program instead of writing
  the result out: `agent.New(...).WithCodeMode(scene.Scene3D())` gives the model a
  tool that runs its JavaScript in a sandbox, checks what it built and returns a
  short report (errors with line numbers, sizes, counts, overlaps). The model
  fixes the program and runs it again, up to a set number of attempts, and
  `svc.RunCode(ctx, goal)` returns the best result as JSON rather than code in the
  answer. The sandbox has no file system, network or timers, and stops endless
  loops. `pkg/codemode/scene` is the first builder (boxes, cylinders, spheres,
  groups, materials, rendered with three.js in the browser); applications can
  plug in their own. `codemode.Run` runs a program without a model. See
  `docs/code-mode.md` and `examples/code-mode-scene`.
