# Python pending host operations

This bounded Phase 4 port implements the generic CPython mailbox lifecycle and
an explicit monotonic entry for emitted cooperative executables. It adds no
production HTTP, crypto or encoding mapping and opens no library/capability gate.
The GCE006 gate remains. Rust/C hosts, native adapters, importable TDF3 packages
and seven-target OpenTDF/real-KAS delivery remain required. Broader service and
full-reference API parity are outside the current SDK goal.

## Executable entry and source ownership

Cooperative `main.py` exposes synchronous `runHost()`. It blocks its calling
owner thread while that thread drives compiler frames, source initialization,
contexts, channels and tasks. Native worker threads/Future callbacks publish
records and wake the owner; they never execute source, even under the GIL.
`runHost()` returns only after native cleanup acknowledgement. It does not return
a Future or implement Python SDK sync/async library wrappers. `python3 main.py`
retains virtual time, arbitrary-byte output, source-line metadata and default
executable behavior.

The executable ABI retains module globals, `_sched` and panic bindings. All
host, virtual, sequential, harness and reset starts share an atomic reservation.
Overlap rejects before touching the active owner; factory, scheduler and native
thread construction failures release the reservation. Source primitives validate
owner-thread access before mutation. Public context cancel/hook removers reject
foreign/retired owners; an internal remover supports inactive-owner retirement
while retaining thread and generation checks. Retirement restores an empty usable
panic binding. An inactive reset cancels/awaits pending work; active reset rejects.

Importing emitted `main.py` does not run source init/main. Each `runHost()` reruns
compiler init/entry against persistent globals. The two-call probe prints
`init 1`, `main 2`, then `init 3`, `main 4`. There is no package-init guard or
initialize-once library claim. Later minimal TDF3 façades may safely serialize
calls and construct/close shared clients per call, but must satisfy the
[SDK library boundary](../../sdk/docs/library-requirements.md) and package-consumer
evidence. This executable entry alone is not that façade.

## Clocks, contexts and wire records

Host mode uses an owner-local epoch from
[`time.monotonic_ns`](https://docs.python.org/3.10/library/time.html#time.monotonic_ns).
Elapsed observations never regress and saturate at signed int64 nanoseconds;
source durations retain the signed int64 range despite Python's unbounded ints.
Positive addition saturates. Timers register captured absolute deadlines and
order by deadline, then sequence. Due real timers dispatch with runnable source
and native records. Pending I/O causes a Condition/deadline wait, never virtual
fast-forward or false deadlock. Native waits cap at
[`threading.TIMEOUT_MAX`](https://docs.python.org/3.10/library/threading.html#threading.TIMEOUT_MAX)
and recheck the exact absolute deadline after wake. Wall UTC remains independent
and unmapped by this assignment. One source step that never yields can delay
observations; real timers do not promise nanosecond OS wake precision.

Contexts anchor at creation, inherit the earliest parent deadline and immediately
observe expired/nonpositive parents. Cancellation prunes parent/child, timer and
hook registrations. Nil source contexts panic with Go nil-dereference; foreign
contexts produce HostFault. Later capability adapters must return their declared
validation categories. `host_boundary` is a generic request-only timeout,
anchored before copying/submitting, which does not cancel its source parent. It
is not the production HTTP boundary.

Registration installs the operation and parks its task before submission,
including already-completed Futures/reentrant callbacks. `HostToken` retains
only mailbox identity and exact integer operation/task IDs. The mailbox is the
unique owner generation; tokens retain no source frames or inputs. A Condition
protects native records, first-publication order, live task identity and ACKs.
Python dict insertion order preserves FIFO across removal/ACK holes. Invalid,
foreign, duplicate, late and retired-generation records/ACKs cannot poison or
acknowledge a live operation. Completed tasks and record/ACK bookkeeping prune
promptly rather than accumulating until shutdown.

Wire snapshots accept exact None/bool/int/float/str/bytes leaves, bytearray copies,
lists/tuples and exact string-keyed dicts. Nested bytearrays become independently
owned immutable bytes. Cycles, custom objects, source descriptors/frames/closures
and opaque keys reject. Driver decode constructs future source results/errors;
no general deepcopy/pickle or cloned source error descriptor crosses the native
boundary. Keys need opaque owner-registry IDs with input acquisition/output
ownership transfer and cleanup; this assignment implements neither that registry
nor production crypto. Existing source bytearray aliasing and unsigned bytes stay
intact.

## Completion, cancellation and resource release

First applicable completion applies exactly once after its separate cleanup ACK
and queues the task FIFO. Cancellation observed before ordinary application wins;
committed success is final. An applicable adapter fault beats ordinary cancellation
and bypasses source panic/recover. Unexpected worker/Future/submission/decode/
callback faults are HostFault, including broken exception formatting. Declared
transport failures must be ordinary result records.

`launch_host` accepts an exact `concurrent.futures.Future`, attaches its callback
immediately and handles synchronous settlement. Its adapter contract requires
submission failure and Future settlement to occur after native lease cleanup.
A Future's done/cancelled state alone does not prove transport/body/key/input
release. [Future.cancel](https://docs.python.org/3.10/library/concurrent.futures.html#concurrent.futures.Future.cancel)
returns false for running work; Python cannot forcibly stop a thread. An adapter
whose Future settles before cleanup must register manually and ACK only after
release. [Callbacks may run immediately](https://docs.python.org/3.10/library/concurrent.futures.html#concurrent.futures.Future.add_done_callback),
so registration and parking always precede attachment/submission. A malformed
adapter that never ACKs prevents safe shutdown; the driver invents no release.

Main return, source panic, HostFault, source fatal and reset retire the owner,
request cancellation, wait every resource ACK, exhaustively clean registrations
and clear queues, context/timer/frame/result/panic roots. One cleanup fault does
not prevent other cleanup. Fatal report/exit 2 occurs afterward. Direct channel
send/receive/select, mutex, WaitGroup, nil operations and sleep each detach
blocked roots. Retained external channels/sync objects lose task waiters; send
payloads clear, and stale waiter callbacks cannot mutate a new owner.

## Managed source recursion

The host entry never changes `sys.getrecursionlimit()` or `threading.stack_size()`.
Python documents that an excessive
[recursion limit can crash the interpreter](https://docs.python.org/3.10/library/sys.html#sys.setrecursionlimit)
and that [stack_size applies to subsequently created threads](https://docs.python.org/3.10/library/threading.html#threading.stack_size).
The legacy virtual/sequential executable retains its historical large-stack
policy for conformance; the new host path does not pass through that policy.

Generated ordinary functions have balanced `@rt.source_guard` wrappers. In host
mode the bound is 128 nested ordinary calls; cooperative calls have a 512-frame
bound. The guard raises typed SourceFatal, bypassing Go recover. Actual CPython
RecursionError unwinds to the owner driver, becomes the same stack fatal, awaits
ACK, then reports exactly `runtime: goroutine stack exceeds limit` and
`fatal error: stack overflow` with exit 2. Normal returns, deferred ordinary calls
and exceptions balance the guard. These bounds are explicit implementation
limits, not Go dynamic-stack growth. Under `--pause-model=native` cooperative
calls are coroutines bounded by CPython's recursion limit instead of the
512-frame bound; its RecursionError becomes the same stack fatal. Arbitrary catastrophic interpreter/native
extension failures cannot promise managed cleanup or crash recovery.

## Evidence and reproduction

Use actual Python 3.10+ (`python3`, observed CPython 3.10.12) and
`GOTOOLCHAIN=go1.25.14`. Other prerequisites come from `driver.ToolEnv` and the
[pinned TypeScript browser tooling](typescript-host-operations.md#verification-and-reproduction).
No references/profiles/lockfiles or SDK tracked files changed. Logs, launcher
handles/status, freeze hashes and historical failures are ignored under
`out/python-host-operations/`.

`targets/python/tests/host_operations_test.py` and its Go contract wrapper prove
registration/reentrant settlement, ordering/duplicates/foreign generations/ACK
holes, nested snapshots, cancellation/success/fault precedence, native callback
thread rejection, captured hook/cancel retirement, controlled clock/deadline
ordering/overflow, actual capped native Condition wait, anchored pre-submission
expiry and Future cancellation/resource semantics. Each retained blocked root is
populated and retired independently, with stale callbacks afterward. Cleanup
faults release every other registration. The 256-operation backlog verifies every
result and cleanup before main returns; a source progress task starts before
measured nested deadlines and stays runnable while native work is pending.

`tests/integration/python_host_operations_test.go` first runs unchanged emitted
virtual output with byte/nil assertions. Unique asserted markers instrument only
copied test output. Actual emitted `runHost` frames perform independent local
HTTP/TLS using visibly test-only `http.client` adapters. A second real release
request proves source progress before the first response body completes;
server/header barriers precede source cancellation. Exact counters are two each
for normal/release/cancel/trusted TLS across two entries; untrusted TLS never
reaches its handler. All ten transport cleanups and zero native input/body/client
leases hold before return. These adapters omit production validation, bounded
headers/body, redirects and error contracts and add no capability mapping.

`tests/integration/python_host_fatal_test.go` compiles genuine recursive source,
source panic and mutex misuse. Both the source guard and actual CPython
RecursionError (test-only recursion limit 100) hold a native input until the owner
is actually waiting for ACK, then the parent releases cleanup. Each subprocess
requires a fresh exact cleanup marker before exact stderr/exit 2. An unchanged
sequential mutex-fatal case also passes. Runtime fatal subprocesses cover panic,
deadlock and mutex-fatal.

Historical failures are retained: the first stress run found completed task roots;
the expanded progress fixture accidentally overwrote Frame.parent with a context;
the first emitted sequential fatal lacked the legacy worker owner binding; moving
the reservation initially omitted the common HostFault import. Root review also
found native-thread/retired captured hook-remover access, now guarded with durable
regressions. Subsequent affected checks passed. Background hook retention was subsequently repaired with eight-entry no-root
and stale-remover checks. A final harness-constructor reservation edge moved its
constructor inside try/finally; its constructor-failure/re-entry regression and
current virtual harness passed. A final waiter audit added checks before active
send/receive/select result/panic mutation and before detach/attach/dequeue/try_recv
mutation. Native-thread direct/select callbacks and helper calls preserve queued
waiters, results, select state and pending panic; retired callbacks remain no-ops.
The current native lifecycle and 179-case harness passed this narrow delta.

All verification processes are terminal. The original broad launcher was PID
2846124/session 95943. Every behavioral command passed, with no required skips;
its old freeze comparison intentionally failed after the Background repair.
That failure remains visible in `final-launcher.status` rather than being
substituted with later evidence. The repair launcher PID 2921324/session 72192
passed every command. The final constructor launcher PID 2947353/session 28922
also passed every command. The final waiter-boundary launcher PID
2964281/session 98679 passed every command, including current native/fatal tests
in 0.192s, virtual 179/179 and catalog/freshness. Source/test freezes for the earlier
launchers remain historical;
`terminal-production-freeze.sha256` supersedes them. The constructor delta affects
`runtime/task_spawn.py`, `tests/host_operations_test.py` and generated
`spec/task_spawn.yaml`. The final waiter delta affects `runtime/chan_make.py`,
`tests/host_operations_test.py` and generated `spec/chan_make.yaml`; unaffected
broader checks were retained.

| Command | Terminal evidence |
| --- | --- |
| `go test -v -count=1 ./tests/contracts` | All seven contracts, retained byte defaults, mandatory C sanitizers and actual TS prerequisites passed: original 140.439s (`contracts-final.log`), post-Background repair 145.443s (`repaired-contracts.log`). |
| `GOALCHEMY_TEST_TARGETS=python go test -v -count=1 ./tests/language` | Full Python language passed in 37.796s (`python-language-final.log`). |
| `go test -v -count=1 ./internal/... ./tests/corpus ./tests/integration` | All internals, all-target corpus 70.594s and full integration 285.025s passed (`compiler-corpus-integration-final.log`). |
| Python native/emitted focused suites | Post-Background native/fatal tests 0.330s and actual emitted HTTP/TLS/reuse/guard/CPython RecursionError/panic/mutex/sequential fatal 51.652s passed (`background-hook-repair-focused.log`). |
| `go test -v -count=1 ./tests/contracts -run TestPythonHostOperations` | Final constructor-failure/re-entry and complete waiter-boundary/native/fatal lifecycle tests passed (`waiter-boundary-native.log`); preceding constructor supplement also passed (`constructor-native.log`). |
| `node targets/typescript/tests/host_operations_browser.mjs` | Actual Chromium 147.0.7727.15, portable lifecycle and genuine fetch/source progress passed (`browser-final.log`). |
| `npm --prefix targets/typescript run typecheck` | Passed (`typecheck-final.log`). |
| `out/python-host-operations/goalchemy-terminal test -target python` | Final 179/179 virtual cases passed (`waiter-boundary-python-harness.log`). |
| `out/python-host-operations/goalchemy-terminal spec validate` / `spec generate -check` | Final 13 types/94 functions/seven targets and all 503 generated files current (`waiter-boundary-catalog.log`, `terminal-freshness.log`). |
| SHA-256 preservation, documentation links and `git diff --check` | Current terminal production/docs hashes, accepted C#/repaired Java hashes and all changed-document links/whitespace passed. Historical accepted other-target tracked diffs match the captured baseline. |

The Background change affects only never-canceling root hook retention; its
current native/emitted/all-seven checks replace the affected historical evidence.
The final constructor change affects only a failure before harness drive; its
native regression/current 179-case harness/freshness checks cover that delta.
The final waiter checks affect only rejection before unsupported native-thread
mutations; current native, virtual and freshness checks cover that delta. These
changes do not justify repeating unaffected full language/corpus/browser work.
No Phase 4, production adapter, exported SDK library or seven-target TDF3/KAS
completion is claimed; this bounded prerequisite is subject to root acceptance.
