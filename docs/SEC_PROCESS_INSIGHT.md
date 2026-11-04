# `/sec` process insight provenance and boundary

The process-insight feature is a clean-room implementation informed by the
behavior of `ref/witr` (Apache-2.0). No Witr Go package, Cobra command,
Bubble Tea view, renderer, or dependency tree is imported. Runtime snapshots
remain separate from the persisted static `/sec` report.

The collector accepts explicit PID, port, process-name, or executable-file
targets; uses fixed OS commands/paths with a two-second timeout and output
cap; never reads or persists a full environment block; never elevates
privileges or controls a process; redacts sensitive arguments; and reports
unsupported, permission-denied, exited-process, and not-found states honestly.
