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
python3 scripts/bench/bench.py swift-sdk --sdk ../opentdf-sdk before=/tmp/gc-before after=/tmp/gc-after
```

## micro

Process wall time of three cooperative programs in
`scripts/bench/testdata`, median of `--runs` (default 7), with a check that
every build prints the same output:

- `cpu`: a 30M-iteration integer loop in a suspending `main`.
- `calls`: 2M calls that may suspend (a buffered channel send and receive),
  then `fib(27)` through suspending recursion.
- `pingpong`: 300k round trips over unbuffered channels between three tasks.

`--targets` selects targets (`go`, `typescript`, `python`, `swift`; default
`go,typescript`), and `--pause-model native` selects the pause model of the
targets that implement both (Go, TypeScript, Swift). Swift programs are built
in release mode (`-O -wmo`) before timing. A compiler named `NAME@MODEL`
uses that pause model, so one run can compare models:
`bench.py micro --targets typescript base=/tmp/gc-before after@frames=/tmp/gc-after after@native=/tmp/gc-after`.

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

## swift-sdk

Compiles the OpenTDF SDK's library (`--sdk`, a checkout of the SDK
repository) to Swift with each compiler and times
`swift build -c debug -j 2 --target GoalchemyGenerated` from a clean build
directory. It needs several GB of memory.
