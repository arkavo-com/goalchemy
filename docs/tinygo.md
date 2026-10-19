# TinyGo WASI compiler profile

`-target tinygo` emits lowered Go and builds a **WASI Preview 1 WebAssembly
command** with TinyGo 0.43.0. It shares the Go emitter, contract implementations,
deterministic map iteration and slice growth rules. Source `int` and `uint`
remain 64-bit on wasm32. This is an experimental profile, not full Go conformance.

```sh
scripts/fetch-toolchains.sh tinygo # pinned TinyGo + Binaryen, Linux x86_64
go build -o bin/goalchemy ./cmd/goalchemy
bin/goalchemy run -target tinygo ./scripts/bench/testdata/tinygo-cpu
bin/goalchemy compile -target tinygo -out out/tinygo ./scripts/bench/testdata/tinygo-maps
(cd out/tinygo && sh run.sh)
```

On macOS, install official TinyGo 0.43.0 and Binaryen 133 archives on `PATH`,
or unpack them into `.toolchains/tinygo` and `.toolchains/binaryen`. TinyGo
needs a compatible Go installation; the included runner needs Node.js 22.6+.
`sh build.sh` produces `main.wasm`; `sh run.sh` builds and executes it. To run
an existing module, use `node --disable-warning=ExperimentalWarning run.mjs`.
The runner provides WASI Preview 1 without preopened directories or inherited
host environment variables. A browser deployment needs a WASI host adapter;
this is not a JavaScript/browser ABI or a WASI Preview 2 component.

Builds use `-opt=2`, one TinyGo job and one Binaryen worker. Change these with
`GOALCHEMY_BUILD_JOBS` and `BINARYEN_CORES`. GC remains enabled. A project can
use `targets: {tinygo: {out: out/tinygo}}` in `goalchemy.yaml`. The manifest
identifies `tinygo` independently of `go`. `GOALCHEMY_TINYGO_TARGET=native sh
run.sh` optionally builds and executes a native binary using the same lowering.

## How the source subset relates to TinyGo

The source gate and output compiler answer different questions. The gate
admits the portable Goalchemy language; TinyGo must compile the *lowered
program and its linked runtime*. The subset helps by excluding unsafe,
cgo, arbitrary standard-library imports and ordinary source generics, but
the generated Go runtime still uses generics, interfaces and reflection.
Passing the source gate alone does not establish TinyGo compatibility.

| Area | Current profile |
| --- | --- |
| Integers, strings, arrays, structs, pointers, slices, maps | Shared Go runtime. Source `int`/`uint` lower to distinct signed/unsigned 64-bit storage types, including in interfaces; lengths and allocation sizes cross the wasm32 boundary explicitly. |
| Tasks, channels, select | Cooperative gate with the frames pause model; native pause is rejected. |
| Interfaces | Native dispatch and assertions. Failed method assertions use static single-method checks instead of reflective method signatures. Boxing function-containing values is rejected. |
| Printing | Explicit formatting and writes to stderr preserve Goalchemy's source output contract. TinyGo's builtin print uses stdout. |
| `recover` | Rejected. TinyGo 0.43.0 differs on indirect recovery and `panic(nil)`. Normal-return defer remains supported. |
| Floating point | Rejected until NaN, signed-zero, conversion and rounding edge semantics match the Go reference. |
| Runtime failures | Bounds, negative shifts and other runtime-generated panic messages follow TinyGo and can differ from Go. This profile is not a full Go conformance claim. |
| Capabilities and libraries | Core contracts and `std.errors` are available. Host capabilities, library exports and host-file injection are outside this profile. |
| Platforms | WASI Preview 1 command output is the default. Native execution is optional. MCU and browser-specific host adapters are not included. |

The ordinary `go` target remains available for the full existing source
profile. The TinyGo profile uses a private catalog copy, so selecting it
does not remove capabilities from later Go builds.

TinyGo's [WASI guide](https://tinygo.org/docs/guides/webassembly/wasi/) describes
the deployment model; the [Node WASI API](https://nodejs.org/api/wasi.html)
provides the bundled runner. TinyGo's [language documentation](https://tinygo.org/docs/reference/lang-support/)
and [package compatibility table](https://tinygo.org/docs/reference/lang-support/stdlib/)
describe compiler support separately from complete behavioral compatibility.
The [0.42 release](https://tinygo.org/blog/2026/tinygo-0.42-recover-is-real/)
added runtime panic recovery, but that does not establish identical recovery
semantics for every source program. The restrictions above come from running
this repository's differential fixtures with 0.43.0.

## Performance and memory

```sh
GOMAXPROCS=2 GOMEMLIMIT=1GiB GOTOOLCHAIN=go1.27.1 \
  python3 scripts/bench/bench.py tinygo --runs 7 candidate="$PWD/bin/goalchemy" \
  --json /tmp/performance-tinygo.json
GOALCHEMY_TEST_TINYGO=1 go test -p 1 ./internal/driver -run TinyGo
python3 -m unittest discover -s scripts/bench -p 'test_*.py'
```

The benchmark compares four builds of each integer-loop, allocation-churn
and map-churn program: direct source with Go/TinyGo, and lowered output with
Go/TinyGo. This separates compiler effects from Goalchemy runtime overhead.
The four variants run on the same Node WASI host. The benchmark sources use
explicit `int64` for overflowing arithmetic so direct TinyGo source has the
same width. They avoid Goalchemy-specific task syntax and capabilities; arbitrary
Goalchemy input cannot simply be passed to TinyGo unchanged. The separate
`integers64` differential test verifies lowering of source `int`/`uint`, overflow,
interface identity, map keys, lengths and UTF-8 range offsets on wasm32.
Use `--tinygo-platform native` for a native comparison instead.

Every row reports build wall time and peak RSS, run wall time and peak RSS,
and executable bytes. WASI rows additionally report execution time inside
`wasi.start` and peak linear-memory bytes. Run samples alternate among the four variants after
one excluded warm-up. Build measurements are single observations with caches
retained, using one job. Executable sizes include each compiler's default
debug information. Process timing includes Node startup, module compilation,
instantiation and execution; the separate execution time excludes startup and
instantiation. Text output must agree;
the differential fixture tests separately compare stdout, stderr and exit.

RSS comes from `wait4` for each measured child, normalized to bytes on Linux
and macOS. It measures resident process memory, not allocated heap bytes,
and is not the simultaneous peak of an entire compiler process tree. It is
neither an MCU RAM estimate nor a portable heap budget. Wasm linear memory
can grow but cannot shrink, so the memory size after execution is its page
high-water mark. It includes stack, heap and static data, and is not live heap
usage. Set `GOALCHEMY_WASM_METRICS=/tmp/metrics.json` on the runner to save
execution time and initial/peak linear-memory sizes independently. `GOMEMLIMIT` constrains
the Go compiler process, not LLVM's native allocations or generated programs.

See [the benchmark guide](benchmarks.md) for other targets. The `micro`
command also reports runtime peak RSS beside its timing statistics.
