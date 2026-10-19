# Hardening and upgrades

`cmd/hardening` records three independent results for the standard target matrix, including
experimental Swift. Install the pinned Linux x64 toolchains from
`toolchains.lock` first, then run it from the repository root. The installer
verifies every archive's SHA-256 and builds bdwgc with POSIX thread support.
Reports use JSON schema version 2 and
record the compiler, source profile, Git revision and dirty state, host and
target toolchains, the lock file hash, case counts, observations, generated build hashes, and
timings.

`observation_sha256` hashes normalized exit status, standard output, and
standard error. For multi-case runs it combines those hashes in case order.
The reports keep hashes rather than full program output; a changed hash can
be investigated by rerunning the recorded seed or fixture.

```sh
scripts/fetch-toolchains.sh
go test -timeout 30m ./...
go run ./cmd/goalchemy spec generate -check
go run ./cmd/hardening -mode compat -budget 15m -out reports/compat-next.json
go run ./cmd/hardening -mode fuzz -seed 1 -cases 20 -ops 64 -budget 30m -out reports/fuzz-next.json
go run ./cmd/hardening -mode baseline -samples 3 -budget 15m -out reports/performance-next.json
```

`go test -short -timeout 5m ./...` is the development check. It runs Go and
TypeScript for the language, corpus, contract, and integration matrices and
skips the sanitizer and heap stress suites. The standard full command above
runs all targets. The Go-only, one-case, eight-operation fuzz smoke test
runs in both modes.

`-target go,typescript` selects a smaller matrix during development. The
`-budget` limit is checked between target runs; an individual compiler or
target runner may continue until its own test timeout. An incomplete campaign
reports `budget_exhausted` and exits nonzero. Keep the seed and operation
count with every fuzz report. The generator uses a fixed xorshift sequence, so
the same seed and count produce the same Go program on later toolchains.

## What the modes measure

- `compat` runs the canonical runtime contract cases, two sequential source
  fixtures against native Go, and a second clean emission of each fixture. It
  fails if the generated file trees differ. Each target's `build_sha256` is
  the hash of its generated `calls_results` build directory before execution.
- `fuzz` generates type-valid Go programs that combine integer widths and
  conversions, unsigned shifts, bounded control flow, struct copies,
  interface dispatch and type switches, closure captures, slice sharing,
  maps, byte strings, recovered error paths, and channel handoffs. It compares
  normalized native Go observations with every selected target. On a mismatch
  it removes operations while preserving the mismatch, up to 24 checks or the
  remaining budget, and writes `<report>.repro/main.go` plus `meta.json`.
  Infrastructure failures stay failures and are never reduced into corpus
  cases. Review a reproducer before adding it to `tests/corpus/` with the
  required `meta.yaml` and, if needed, `want.txt`.
  Native Go is built and run once per generated case, then its observation is
  reused for every selected target.
- `baseline` compiles and executes `examples/calc` twice by default. It
  records separate compiler and target runner durations in milliseconds.
  The target runner includes its language build step; native Go is run as an
  oracle but its build time is excluded. Generated build hashes must match
  across samples. Durations are measurements, not pass/fail thresholds;
  compare them on the same machine and toolchain image.

The repository's `reports/compat-v1.json`, `reports/fuzz-v1.json`, and
`reports/performance-v1.json` are the initial seven-target reference reports.
`reports/` is a snapshot of the local validation environment, including its
recorded Git revision and toolchain lock hash. A new report supersedes a
snapshot only after the changed toolchains and source are reviewed.

When refreshing committed snapshots, commit code and `toolchains.lock` first.
Run each mode with an output path outside the repository, copy the three JSON
files into `reports/` after every run has finished, and commit the reports.
That sequence records the code revision with `dirty: false` in every report.

## Upgrade workflow

1. Save the old compiler/toolchain report and note its Git revision. Pin the
   proposed toolchain or dependency versions in the relevant build files.
2. Run the full tests and the three commands above. Pass `-previous` with
   `reports/compat-v1.json` on the compatibility run and with
   `reports/performance-v1.json` on the timing run. The report lists version,
   status, case-count, and generated-build changes. A changed hash needs a
   manifest/source review even if observations still match.
3. For a semantic change, update `specs/language.md`, affected canonical
   contracts, contract versions, generated specs, and conformance cases
   together. Save any reduced fuzz mismatch in `tests/corpus/` and rerun all
   targets.
4. Archive the old and new JSON reports with the release revision. Keep the
   seed, budgets, and toolchain image together so the result can be replayed.

Run a nightly campaign with explicit time budgets per family, for example
`compat` for 15 minutes, `fuzz` for 30 minutes, and `baseline` for 15 minutes.
The amount of generated fuzz work varies with target speed; the seed and
completed case counts make each run auditable.

The experimental [TinyGo WASI profile](tinygo.md) has its own opt-in
`GOALCHEMY_TEST_TINYGO=1 go test -p 1 ./internal/driver -run TinyGo` suite and
`scripts/bench/bench.py tinygo` benchmark. It is not included in the full
language/conformance matrix because its accepted subset is narrower. The
[WASI performance report](../reports/performance-tinygo-wasi.json) records
build and execution timing with peak process RSS and Wasm linear-memory
usage beside those timings, including source versus lowered comparisons.
