# C# pending host operations

This bounded Phase 4 port provides a generic CLR Task/mailbox lifecycle and an
explicit monotonic entry for compiler-generated cooperative executables. It does
not implement production HTTP/crypto adapters or generated SDK libraries. All
unsupported capability mappings and the GCE006 library gate remain closed. Python,
Rust and C host ports and the full seven-target library boundary remain required.

## Entry, clocks and ownership

Cooperative output exposes `GoProgram.runHost()` returning a Task. A dedicated
owner thread drives the actual emitted frames, and the Task settles only after
native cleanup acknowledgements. Default `GoProgram.Main`, `sh run.sh`, and
runtime harnesses retain deterministic virtual time, byte-exact output, source
positions and Go-compatible fatal reports/exit status 2. Ordinary sequential
mutex misuse retains its fatal report and exit 2 as well.

The executable ABI keeps static source globals, `R.sched` and panic bindings.
Executable/harness entries and reset reserve an atomic guard before touching the
owner. Overlapping entry/reset rejects; constructor/start failures release that
guard. Task continuations use `ConfigureAwait(false)` and never capture a source
SynchronizationContext. Source frames, contexts, channels, sync registrations,
timers and result vectors mutate only on the owner driver. Retiring an inactive
owner during reset requests cancellation and awaits ACK; reset during a running
entry rejects without changing it. Retirement restores an empty usable default
panic binding.

The emitted two-call probe prints `init 1`, `main 2`, then `init 3`, `main 4`.
Initialization reruns against persistent globals. This is executable reuse,
not initialize-once library semantics, persistent clients, or independent source
instances. Future library work must satisfy the actual stateful
[SDK boundary requirements](../../sdk/docs/library-requirements.md).

Host time uses `Stopwatch.GetTimestamp` with an owner-local epoch. An unsigned
128-bit product converts elapsed ticks to nanoseconds with one division; it
saturates before narrowing to `long`. Observations do not regress. Timer deadlines
saturate positive duration addition and order by absolute deadline, then sequence.
Due real timers dispatch even with runnable tasks. With no runnable source, the
driver waits on mailbox changes or its next deadline; pending work causes neither
false deadlock nor virtual fast-forward. One source step that never yields can
delay driver observations. Wall UTC remains an independent capability; no new
wall-clock mapping was added by this port.

Contexts anchor deadlines at creation, inherit the earliest parent deadline and
immediately observe expired or nonpositive parents. Registration uses captured
absolute deadlines without resampling drift. Cancellation prunes parent/child,
timer and hook registrations; retirement removes remaining registrations. Nil
source contexts cause Go nil-dereference panic; foreign owners cause HostFault.
Future adapters must translate invalid capability boundary inputs into declared
validation results. `HostBoundary` captures request-only timeout before input
copying/submission, inherits any earlier source deadline, and leaves its source
parent uncanceled on request-only expiry. It is lifecycle plumbing, not HTTP.

## Mailbox, wire values and cleanup

Native callbacks receive `HostToken`, containing only mailbox identity and numeric
operation/task IDs, never owner, frames or source input roots. Mailbox identity is
the unique owner generation. Registration installs the operation and parks its
task before submission, including synchronous/reentrant Task completion.

Tokens copy result records and signal the mailbox. The first record wins; foreign
operation/task IDs, duplicate and retired-generation records/ACKs are ignored.
Applicable records require a separate resource-cleanup ACK. The driver applies
them exactly once and queues FIFO by explicit publication sequence. This sequence
is necessary because .NET Dictionary iteration can reuse deleted entry slots:
publication order survives an earlier record being removed while a later record
still awaits ACK. Live/record/ACK storage stays bounded by current operations;
completed background task roots are removed promptly.

Cancellation observed before ordinary completion application wins; committed
success is final. An applicable adapter fault beats cancellation and bypasses
source panic/recover. Unexpected Task exceptions, submission, decode and cancel
bridge faults are host faults. Declared transport/validation failures must be
ordinary result records. A canceled Task or CancellationToken notification does
not establish transport/body/key/input cleanup.

`launchHost` requires its work Task to settle only after resource cleanup. Adapters
whose terminal Task state precedes cleanup must register manually and acknowledge
only after release. On main return, source panic, host fault, fatal guard or reset,
retirement cancels native work, waits for every pending ACK, then exhaustively
runs registration/context/task cleanup and clears queues, timer/context roots,
frames, results and panic vectors. A fault in one cleanup hook does not stop the
others. A broken adapter that never acknowledges cleanup prevents safe return;
the driver cannot invent resource release. Fatal report/exit occurs after this
retirement, without further source execution.

Wire snapshots recursively copy `byte[]`, object arrays, object lists and
string-keyed dictionaries containing primitive/string leaves. They reject cycles,
source descriptors, closures, frames and opaque native handles. Native bytes keep
unsigned CLR byte semantics and the accepted source long representation.
`registerHost` provides a driver-side decode function for future declared result
records. Native keys require an owner registry with opaque wire IDs and explicit
input snapshot acquisition/output ownership transfer; neither indiscriminate
source-descriptor cloning nor a production key registry is implemented here.

Blocked direct send/receive/select registrations detach from retained external
channels; pending send payloads clear. Select cleanup retains registered waiters
and channels rather than the full evaluated send-case array. Mutex and WaitGroup
waiters detach too. Stale waiter references lose task/value roots and reject late
callbacks; nil send/receive, empty select and sleep drop task/frame roots during
retirement.

## Managed recursion guard

CLR native `StackOverflowException` cannot be caught for managed cleanup, as the
[Microsoft documentation](https://learn.microsoft.com/en-us/dotnet/api/system.stackoverflowexception?view=net-8.0)
explains. Generated ordinary functions use a balanced `using` scope and
[TryEnsureSufficientExecutionStack](https://learn.microsoft.com/en-us/dotnet/api/system.runtime.compilerservices.runtimehelpers.tryensuresufficientexecutionstack?view=net-8.0),
plus a 4096 ordinary-source-call depth bound. Cooperative source calls have a
4096 frame-depth bound. Under `--pause-model=native` they are `async` methods
guarded by the execution-stack probe alone: a parked task keeps no stack, so a
depth count would add up across tasks. The host driver thread has a 16 MiB stack; default virtual
execution retains the existing large-stack thread. The effective guard raises
`SourceStackFatal`, which Go recover cannot absorb. It unwinds to the driver,
awaits cleanup ACK, then prints the exact Go-compatible stack fatal report and
exits 2. Ordinary generated recursion reached from a cooperative source frame is
tested, with pending native work and cleanup held behind a parent gate.

This is preemptive generated-source guarding, not support for catching native
CLR stack exhaustion. The execution-stack probe promises room for an average
.NET function; huge arbitrary native adapter stack frames or catastrophic native
host failures cannot promise managed cleanup. No test injects StackOverflowException
or intentionally exhausts the CLR stack. The bounded depth is an implementation
limit, not Go's dynamic native-stack growth behavior.

## Evidence and reproduction

Compiler commands use `GOTOOLCHAIN=go1.25.14`. Actual .NET 8 compilation/execution
uses the pinned binaries selected by `driver.ToolEnv`. Frontend reference Go,
profiles, references and lockfiles remain unchanged. Logs, original launchers,
status files, primary documentation snapshots and historical failures live in
ignored `out/csharp-host-operations/`.

`targets/csharp/tests/HostOperationsTest.cs` and
`tests/contracts/csharp_host_operations_test.go` execute the real CLR runtime.
They cover reentrant completion; FIFO/out-of-order/duplicate/foreign records;
ACK holes and dictionary slot reuse; copied inputs/results; deterministic
cancellation/success/fault order; driver decode faults; context equality/drift/
expiration/overflow; request-only anchored timeout; repeated pruning; nil/foreign
contexts; Task exception categories; and exhaustive cleanup-hook faults. Each
blocked queue is checked populated before independently retiring it with the
external object retained, then late callbacks are invoked and task/result/panic
vectors inspected. A concurrent 256-operation backlog verifies every result and
cleanup before main returns.

Native/key test leases retain copied inputs through cancellation. Tests observe
the owner actually waiting in retirement for withheld ACK; merely observing an
unfinished entry is insufficient. Main return, unexpected source-step exception,
applicable adapter fault and real source panic are distinct cases. Reset also
awaits ACK. A real deadline test starts a progress worker before creating its
measured nested context and confirms real elapsed time while the worker stays
runnable. This barrier prevents load from invalidating an otherwise correct
scheduler.

`tests/integration/csharp_host_operations_test.go` runs unchanged compiler output
through its virtual entry first, preserving native byte and buffered nil-pointer
behavior. It then instruments copied output at unique asserted replacement
markers and drives actual emitted frames via `GoProgram.runHost`. Independent
local HTTP/TLS servers check exact request counts. A separate real release
request requires source progress before the first body may complete; another
request gate establishes contact before emitted source-context cancellation.
Explicit certificate trust succeeds; default untrusted TLS rejection never
reaches its HTTP handler. Resource counts reach zero and exact cleanup counts
hold before both executable entries finish.

These visibly test-only HttpClient adapters omit the shared production HTTP
validation, bounds, headers, redirects, credential and error contract. Mock key
leases are lifecycle models, not production crypto. No capability mapping was
added. `tests/integration/csharp_host_fatal_test.go` exercises actual generated
recursive source, source panic and mutex-fatal with native work pending. The
parent releases cleanup only after the driver reaches its ACK wait; each child
must write a fresh exact-content marker before exact stderr and exit 2. A separate
unchanged sequential mutex-fatal program preserves the default executable path.

The initial byte replay failed because mailbox support was globally linked into
sequential output without GoTask; `bytes-initial.log` is retained. Mailbox types
now stay with the scheduler's linked runtime file. Root review found dictionary
slot reuse changed completion FIFO and a foreign task record could poison a live
operation; explicit publication sequence and mailbox task-ID validation now have
durable regressions. Preliminary logs are not final production-freeze evidence.

The final retained launcher was Unix PID 2690736, exec session 89876. It reached
terminal exit 0; all commands below passed and no required tests were skipped.
Pinned .NET was SDK 8.0.425 with runtime 8.0.31 (`dotnet-versions.log`). Production
and repaired Java source hashes still match their freeze records.

| Command | Final evidence |
| --- | --- |
| `go test -v -count=1 ./tests/contracts` | All seven target contracts, byte defaults, C sanitizers and actual TypeScript prerequisites passed in 139.961s; `contracts-final.log`. |
| `GOALCHEMY_TEST_TARGETS=csharp go test -v -count=1 ./tests/language` | Full C# language passed in 43.855s; `csharp-language-final.log`. |
| `go test -v -count=1 ./internal/... ./tests/corpus ./tests/integration` | Internals passed; all-target corpus 62.217s and full integration 242.528s, including actual emitted C# fatal/HTTP/TLS, repaired Java and TypeScript Node/Chromium; `compiler-corpus-integration-final.log`. |
| `node targets/typescript/tests/host_operations_browser.mjs` | Actual Chromium 147.0.7727.15 portable host suite and genuine fetch/source progress passed; `browser-final.log`. Retained ignored Playwright/esbuild tooling follows [the TypeScript reproduction instructions](typescript-host-operations.md#verification-and-reproduction). |
| `npm --prefix targets/typescript run typecheck` | Passed; `typecheck-final.log`. |
| `out/csharp-host-operations/goalchemy test -target csharp` | Passed 179/179 virtual cases; `csharp-harness-final.log`. |
| `out/csharp-host-operations/goalchemy spec validate` / `spec generate -check` | Passed 13 types, 94 functions, seven targets and all 503 current generated files; `catalog-final.log`, `freshness-final.log`. Generation followed the shared C#/Java production freeze. |
| `git diff --check` and changed-document local links | Passed; `whitespace-final.log`, `documentation-final.log`. |
| `go test -v -count=10 ./tests/contracts -run TestCSharpHostOperations` | Exact final native lease tests passed ten CLR/fatal replays in 10.174s; `runtime-lease-final.log`, `final-runtime-tests.sha256`. |

The final lease-test clarification happened while the retained broad launcher was
running: its runtime source read timing is not used as evidence for those exact
test bytes. The separate ten-replay suite covers the final test, which explicitly
assigns a completed fault operation no native lease and a separate pending
operation the retained input/mock-key leases. Production remained frozen throughout;
unrelated broad checks were retained without restart. Root's earlier focused
review and all initial successful runs remain historical evidence rather than
substitutes for the exact final lease suite.

Final command results and terminal handles are recorded in the ignored worker
handoff. The bounded C# assignment remains subject to root acceptance.
It does not complete Phase 4, production adapters, exported SDK libraries,
browser SDK/KAS interoperability or seven-target interoperable TDF3 SDK delivery.
