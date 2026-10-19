# Micro C for ESP32

`-target c-micro` emits sequential C17 with an ESP-IDF application adapter.
It shares the C backend's lowering and value representation, and replaces
the desktop collector and scheduler with a bounded arena. This profile is
experimental and targets Espressif's C toolchain, not the proprietary mikroC
compiler. The initial validated chip is ESP32-C3 (32-bit RISC-V).

```sh
goalchemy compile -target c-micro -out out/esp32 ./path/to/package
sh out/esp32/run.sh                         # host smoke test
. /path/to/esp-idf/export.sh
cd out/esp32/esp-idf
idf.py set-target esp32c3
idf.py build
idf.py size
idf.py -p PORT flash monitor                # only when a board is connected
```

ESP-IDF 5.5.1 is the validation baseline. Its [component build system](https://docs.espressif.com/projects/esp-idf/en/v5.5.1/esp32c3/api-guides/build-system.html)
supplies libc, FreeRTOS and the timer API. Generated sources use C17; the IDF
component uses GNU C17 for Espressif's assembly-bearing headers. Minimal
component selection avoids pulling unrelated drivers into the build. Use
`cmake --build build -j 1` after configuration to limit build memory.

Original ESP32 and ESP32-S3 can be selected with `idf.py set-target` and their
toolchains, but are not covered by the initial validation. No GPIO, radio,
Arduino API or device driver mapping is included.

## Source semantics and profile limits

The same restricted Go frontend and IR feed `c` and `c-micro`; Micro C is
a runtime profile, not a new source language. Source `int`/`uint` stay 64-bit
on the ESP32, while pointers and `size_t` follow the device ABI. Bounds checks
compare full-width source values before narrowing. Native byte backing stores
one byte per element; other source values retain the C runtime's tagged
`gx_V` representation. This is not a packed-struct or unboxed scalar backend.

Supported bounded sequential programs include integers, strings, slices,
arrays, maps, structs, pointers, interfaces, closures, defer and recover.
Explicit diagnostics reject cooperative programs, channel/opaque values,
native capabilities, library builds, oversized arrays and floating point.
Floating-point conversion edge semantics need separate validation across
32-bit processors and are deliberately outside this first profile. The
existing desktop C target remains available for the wider runtime.

TinyGo accepts a related Go subset but uses its own compiler/runtime. Its
WASI profile and this ESP-IDF C profile are independent: accepting source
in the frontend does not establish target runtime or host API compatibility.

## Memory and entry lifetime

The generated adapter reserves **64 KiB static arena storage** and configures
a **16 KiB main-task stack**. Customize `GX_MICRO_ARENA_BYTES` and
`GX_MICRO_MAX_DEPTH` through the component's compile definitions; customize
the task stack with `idf.py menuconfig`. The default source depth limit is 16.
It is a recursion guard, not a bound on C stack bytes. Large generated local
arrays and runtime helpers can use stack independently; test real workloads
with the IDF stack canary and measured stack headroom.

`rt/micro/micro.h` exposes:

```c
int goalchemy_micro_run(void *arena, size_t capacity, gx_micro_output output,
                       void *state, gx_micro_stats *stats);
```

Supply storage aligned to `max_align_t` and an optional output callback.
Every invocation initializes source globals afresh. Allocation zeroes memory,
checks the remaining capacity before addition, and preserves interior pointers
and aliases until return. The arena has no collector, per-object headers or
per-object free. **All allocations accumulate until the invocation returns**,
including temporary print strings, map keys, closures and call arguments.
Bound the work per invocation; an allocating firmware loop cannot run forever
in a fixed arena. The caller may immediately reuse storage after return.

Only one invocation can be active globally, even with separate arenas.
Reentry from another task or the output callback returns `GX_MICRO_BUSY` (4).
Callbacks receive borrowed bytes, must finish synchronously, and must return
normally. No source pointers or values cross this API. Invalid storage returns
1; invalid/busy calls leave the caller's statistics untouched.

Normal completion returns 0. Unrecovered source panic returns 2 after source
defers and report formatting. Arena exhaustion, recursion limit and runtime
faults return 3 directly without allocating, running defers or terminating
the firmware. Panic reporting can itself exhaust the arena and return 3.
`stats.fault` then identifies the fault with static text. A subsequent call
starts cleanly after either kind of failure.

## Performance and memory reports

The host runner writes JSON when `GOALCHEMY_MICRO_METRICS` names a file.
The ESP-IDF adapter prints elapsed microseconds alongside arena peak/capacity,
allocation count, peak source depth, minimum free task stack, current free
heap and minimum free heap. Timing includes output callbacks/UART writes.
The [ESP-IDF memory guide](https://docs.espressif.com/projects/esp-idf/en/v5.5.1/esp32c3/api-guides/performance/ram-usage.html)
explains the stack watermark; heap minimum is since boot, not just this call.
`idf.py size` separately reports linked RAM and firmware size. The configured
FreeRTOS task stack is dynamically allocated and must not be mistaken for
part of the linked static arena.

Reproduce the reports with a committed compiler binary:

```sh
go build -o /tmp/goalchemy ./cmd/goalchemy
python3 scripts/bench/micro_c.py --compiler /tmp/goalchemy --runs 7 \
  --json /tmp/performance-c-micro-host.json
# In an ESP-IDF 5.5.1 environment with qemu-riscv32 installed:
python scripts/bench/micro_c_esp32.py --compiler /tmp/goalchemy --runs 3 \
  --work /tmp/micro-esp32 --json /tmp/performance-c-micro-esp32c3.json
```

Both runners compare program output with Go. Host RSS is a fresh child
process peak from `wait4`; it is not microcontroller RAM. QEMU execution time
is emulated time and is not a physical ESP32 performance claim. The timer
watchdog is disabled in QEMU, matching the SDK emulator launcher. The CPU,
byte-buffer and map fixtures exercise bounded workloads; map lookups allocate
encoded keys, which are included in the arena high-water mark.

## Validation

`go test ./internal/driver -run TestMicroC` checks 14 differential fixtures,
profile isolation/cleanup, unsupported modes, and an ASan/UBSan lifecycle
consumer. The consumer checks 100 repeated invocations, fresh globals,
callback reentry, small-arena exhaustion, overflow, invalid alignment,
depth faults, source panic and successful reuse after failure.

The ESP32-C3 benchmark runner builds and links real firmware against IDF,
runs it with Espressif QEMU, compares output, and captures arena, task stack,
system heap, linked memory and firmware sizes alongside execution times.
Hardware flashing and physical-device measurements remain unverified.
