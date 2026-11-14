# The Builder: what the artifact store's budget clears

The artifact store is the folder `artifacts/` of labd's data directory: kernels, packages, configs, recipes, scripts and
payloads, each named by its SHA-256. It is 20 GB (`--artifact-budget-gb`, which the mini's LaunchDaemon file leaves alone).
labd looks at it at start, every hour, and after each build ends (`internal/server/gc.go`). The test is
`TestWhatTheArtifactBudgetClears`.

## Cleared whatever the store's size

- An artifact nothing refers to, once it is a day old. "Refers" means: one of the last 300 jobs or any unfinished job (its
  kernel, Mesa packages, scripts and payloads); one of the last 200 builds (its config or recipe; its outputs only for the 20
  newest builds and those of the last 14 days); a Mac's stored kernel config.
- An upload nobody finished, a day after its last part.

## Cleared only when the store is over 20 GB

Oldest first, until the store fits: anything older than an hour that is not pinned. That includes the kernels and packages
of finished builds and the artifacts of finished jobs, however recent the build or job.

## Never cleared by the budget

- The inputs of a queued or running build, and everything an unfinished job needs.
- A package recipe, for as long as any build record names it: it is the only record of what a release was built from.
- A Mac's stored kernel config.
- The three Mesa bootstrap files.
- Anything that arrived in the last hour. So the store can stand above 20 GB for up to an hour, and for longer while the
  pinned artifacts alone are larger than 20 GB.

## Not in the store at all

What a job collected from a Mac (journals, logs, screenshots, reports, under `jobs/<id>/`) and a build's log (under
`builds/<id>/`) are not artifacts. The budget never clears them, at any age: nothing in labd deletes a job's collected files.
The two build caches are `container` volumes on the mini (40 GB and 120 GB), bounded by `kbuild` and `pkgbuild` themselves.

## When a build's outputs are gone

A build whose outputs were cleared is not reused: the next request for the same source and config builds again. A retry of a
job whose kernel was cleared builds from the same commit with the same config, when the config is still stored.
