# Benchmarks

`scripts/bench/bench.py` holds opt-in benchmarks for comparing compiler
builds. They are not part of `go test ./...` or CI. Each run takes one or more
`NAME=PATH` goalchemy binaries, alternates them between runs, and writes the
results to `out/bench/bench-<command>.json` (or `--json`).

Run one benchmark at a time on an otherwise idle machine. The issue #92
numbers were taken under
`systemd-run --user --scope -p MemoryMax=6G -p CPUQuota=200%`.

```sh
go build -o /tmp/gc-before ./cmd/goalchemy   # on the base commit
go build -o /tmp/gc-after ./cmd/goalchemy    # on the change
python3 scripts/bench/bench.py micro before=/tmp/gc-before after=/tmp/gc-after
python3 scripts/bench/bench.py swift-matrix before=/tmp/gc-before after=/tmp/gc-after
python3 scripts/bench/bench.py csharp-matrix --variants C after@frames=/tmp/gc-after after@native=/tmp/gc-after
python3 scripts/bench/bench.py swift-sdk --sdk ../opentdf-sdk before=/tmp/gc-before after=/tmp/gc-after
```

## micro

Process wall time and per-child peak RSS of three cooperative programs in
`scripts/bench/testdata`, median of `--runs` (default 7), with a check that
every build prints the same output:

- `cpu`: a 30M-iteration integer loop in a suspending `main`.
- `calls`: 2M calls that may suspend (a buffered channel send and receive),
  then `fib(27)` through suspending recursion.
- `pingpong`: 300k round trips over unbuffered channels between three tasks.

`--targets` selects targets (`go`, `typescript`, `python`, `swift`, `csharp`, `java`, `rust`, `tinygo`;
default `go,typescript`), and `--pause-model native` selects the pause model of
the targets that implement both (Go, TypeScript, Swift, C#, Python, Java). Swift programs are
built in release mode (`-O -wmo`) before timing; C# programs are compiled with
their `run.sh` compiler line and timed as `dotnet bin/main.dll`; Java programs
are compiled the same way and timed with their `run.sh` `java` command line. A compiler named `NAME@MODEL`
uses that pause model, so one run can compare models:
`bench.py micro --targets typescript base=/tmp/gc-before after@frames=/tmp/gc-after after@native=/tmp/gc-after`.

## tinygo

`bench.py tinygo --runs 7 candidate=/tmp/gc-after` compares direct source
and lowered output with both Go and TinyGo, using CPU, allocation and map
workloads. The console and JSON report include build seconds/peak RSS bytes,
run seconds/peak RSS bytes, Wasm execution seconds/peak linear memory bytes,
and executable size. The default is WASI Preview 1 on Node; use
`--tinygo-platform native` for the optional native comparison. See the [TinyGo guide](tinygo.md)
for the supported profile and measurement limits.

RSS measurement in `micro` and `tinygo` requires Linux or macOS. `wait4`
returns a per-command process high-water mark; the runner does not subtract
cumulative `RUSAGE_CHILDREN` peaks. Linux KiB and macOS bytes are normalized
to bytes. These values include process overhead and are not heap allocation
counts or simultaneous process-tree peaks.

## swift-matrix

Synthetic Swift build times for generated programs of growing size, built
with the generated program's own `swiftc` command line, debug (`-Onone -j 2`)
and release (`-O -wmo`):

- `A`: one function with N basic blocks (if/else chains and loops with
  `continue`/`break`) that cannot pause.
- `B`: N/4 small functions of about four blocks each, called from `main`.
- `C`: `A` plus a channel send and receive every ~50 blocks, so the function
  is a resumable frame.

`--variants`, `--blocks` (default `50,200,1000`) and `--modes` narrow the
matrix. `--pause-model` and `NAME@MODEL` compilers select the pause model as
for `micro`, so `C` compares frames with native async functions. The Swift toolchain comes from `PATH` or `.toolchains/swift-6.4.0`.

## csharp-matrix

The `swift-matrix` programs compiled to C#: the time of the generated
program's `csc` build and of one run, which includes JIT compilation of the
large function. `NAME@MODEL` compilers select the pause model, so `C`
compares a frame with an `async` method. The .NET SDK comes from `PATH` or
`.toolchains/dotnet`.

## swift-sdk

Compiles the OpenTDF SDK's library (`--sdk`, a checkout of the SDK
repository) to Swift with each compiler and times
`swift build -c debug -j 2 --target GoalchemyGenerated` from a clean build
directory. It needs several GB of memory.
