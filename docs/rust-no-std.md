# Rust without the standard library

`rust-nostd` is an experimental Rust profile for sequential embedded programs
and owned-value libraries. It emits `#![no_std]` plus `alloc`, with no Cargo
dependencies. The application supplies a global allocator, panic handler, and
platform entry point. See Rust's [no_std overview](https://doc.rust-lang.org/embedded-book/intro/no-std.html)
for those platform responsibilities.

```sh
goalchemy compile -target rust-nostd -out out/embedded ./my/package
rustup target add thumbv7em-none-eabi
cargo build --manifest-path out/embedded/Cargo.toml --release --lib \
  --target thumbv7em-none-eabi -j 1
```

Use a stable edition-2021 toolchain (the generated Cargo manifest requires
Rust 1.85 or later). The target must support 8-bit compare-and-exchange atomics;
Cortex-M4 (`thumbv7em-none-eabi`) is covered by cross-compilation checks. Hardware
execution and a board-specific allocator/linker configuration remain the
embedding application's responsibility.

Project configuration accepts the same target name:

```yaml
schema_version: 1
packages: [.]
targets:
  rust-nostd: {out: out/embedded}
```

## Entry points and values

All outputs contain `src/lib.rs`, package modules, selected runtime files,
`Cargo.toml`, `build.rs`, source line maps, and a manifest identifying
`rust-nostd`. The build script selects the no-std implementation of shared
runtime modules. Compile through Cargo, or pass `--cfg goalchemy_nostd` when
invoking `rustc` directly.

A Go `main` package exposes:

```rust
pub fn run(options: RuntimeOptions) -> Result<CallResult<()>, NoStdError>;
```

For a library, exported functions are synchronous and accept `RuntimeOptions`
as their last argument. Booleans, integers, floats, byte strings, arrays, slices,
and public struct value trees use the same owned representations as the normal
Rust value boundary. Strings become `Vec<u8>` to preserve arbitrary Go bytes.
Each function returns `Result<CallResult<T>, NoStdError>`. Multiple results form
a tuple; a final Go `error` becomes `NoStdError::Source(Vec<u8>)`.

`CallResult` contains `value` and `heap` statistics. Every entry initializes
fresh source globals, and releases internal objects and retained capacities
before returning. Returned owned values belong to the caller. Concurrent and
reentrant entries return `NoStdError::Busy` immediately; calls do not wait or
spin. The private entry gate protects runtime storage, including calls from
interrupt handlers. No runtime references escape through the public ABI.

## Memory and output

```rust
let options = goalchemy_out::RuntimeOptions {
    gc_threshold: 256,
    max_call_depth: 64,
    output: Some(write_bytes), // fn(&[u8]); None discards print output
};
let result = goalchemy_out::run(options)?;
let peak_objects = result.heap.peak_objects;
```

The traced collector retains cycles correctly and reclaims unreachable ones.
It uses a minimum of 256 allocation units between collections by default,
adapting the threshold to the live object count. Byte buffers add a unit per
KiB. A threshold of one collects at every safepoint for stress testing. Maps
use `alloc::collections::BTreeMap`; no random-number source is needed.

The runtime uses the caller's stack and creates no threads. The default source
call-depth limit is 64. Collection thresholds and call-depth limits are **not
hard byte or stack limits**: frames, live data, strings, output copies, and
collector scratch storage also need memory. Configure the allocator and stack
for the actual workload. Allocation failure follows Rust's allocation-failure
behavior. This profile requires a heap; it is not allocation-free Rust.

`heap` reports peak live object count, live objects after a final collection,
and number of collections, just before entry cleanup. These counts exclude
owned return values and do not measure total bytes. The library integration
test separately tracks allocation sizes and checks that both successful and
source-error calls release their internal allocations.

## Supported scope

Sequential arithmetic, strings, arrays, slices, maps, pointers, interfaces,
closures, and normal-return `defer` use the shared Rust semantics. Pure error
capabilities from `lib/errors` are supported.

Panics and runtime faults use the embedding application's panic handler. There
is no Go panic unwinding, panic-path defer execution, or `recover`. `recover`,
concurrency, host capabilities (including clocks, logging, OS, HTTP, and crypto),
and exported handles, streams, contexts, or callbacks fail compilation.
Use the normal `rust` target for those features.

Validation covers all included functions, including unused declarations in an
imported package. For example, `std/errors` contains recovery-based comparison
code; use `lib/errors` when only its pure error operations are needed.

## Running and verification

For `main` packages, a separate optional `host.rs` and `run.sh` exercise the
no-std library with a host allocator and print sink:

```sh
goalchemy run -target rust-nostd ./tests/memory/testdata/cycles
GOALCHEMY_HEAP_STATS=1 GOALCHEMY_GC_THRESHOLD=256 \
  goalchemy run -target rust-nostd ./tests/memory/testdata/cycles
```

The host runner uses `std`; the generated library does not. Embedded consumers
configure `RuntimeOptions` directly rather than using environment variables.

```sh
GOMAXPROCS=2 GOMEMLIMIT=1GiB CARGO_BUILD_JOBS=1 \
  GOALCHEMY_NOSTD_TARGET=thumbv7em-none-eabi \
  go test -p 1 ./internal/emit/rust -run TestNoStd -count=1 -v
go test -p 1 ./tests/memory -run 'TestRust(NoStd)?Heap' -count=1 -v
```

Tests cover all naming modes, actual host execution, Cortex-M compilation when
the environment variable is set, owned library values and source errors,
reentrant calls, moving calls between host threads, allocation cleanup, cyclic
garbage, retained structures, and interior references. The normal Rust heap
suite also runs against the shared runtime to guard existing behavior.
