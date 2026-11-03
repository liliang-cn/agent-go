# Changelog

## [Unreleased]

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
