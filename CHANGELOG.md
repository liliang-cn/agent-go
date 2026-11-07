# Changelog

## [Unreleased]

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
