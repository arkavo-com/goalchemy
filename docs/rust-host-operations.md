# Rust pending host operations

This bounded Phase 4 port implements a generic Rust native mailbox, separate
resource-cleanup acknowledgement, real monotonic executable entry and guarded
source recursion. Production HTTP/crypto/encoding adapters and SDK libraries
remain gated. It opens no `lib.*` mapping or GCE006 library gate. C host work,
production adapters and importable seven-target TDF3 packages with real
OpenTDF/KAS interoperability remain required.

## Entry and ownership

Cooperative `src/main.rs` exposes `run_host() -> Result<(), HostError>`. The actual
compiler init/entry frames run on a dedicated 16 MiB owner thread. A process-wide
atomic reservation rejects overlapping host, virtual, sequential, harness and
reset entries before touching the active owner. The calling host holds that
reservation through thread join and TLS teardown. Constructor/initialization
and native-thread creation failures release it. Source panic handles are
classified/formatted on the owner; only owned error strings/bytes leave that
thread. No source `Rc`, `RefCell`, frame, descriptor, `V` or heap handle is made
`Send`/`Sync` or transferred to a worker.

Default `main`, `sh run.sh`, Cargo execution and `run_isolated` preserve virtual
conformance time and existing executable behavior. The native fixture-only
`run_host_frame` accepts an `Rc` frame factory on its calling thread and requires
a sufficiently sized caller stack. It is not the generated entry's stack-safety
guarantee or an exported SDK/library API.

The executable ABI retains thread-local heap/scheduler/source globals and static
function/type tables. Compiler `init_zero_globals` zeroes every source slot on
**every** entry, then source initialization runs again. The actual two-call host
probe prints `init 1`, `main 2` twice. Each dedicated entry owns a new thread-local
heap. There is no package-init guard, initialize-once library semantics,
persistent client or independent overlapping source-instance API. Later minimal
TDF3 façades may serialize calls, but must satisfy the actual
[SDK library requirements](../../sdk/docs/library-requirements.md).

Globals now own one unrooted `Fr` traced separately by `trace_globals`; repeated
same-size initialization reuses it and size changes release the old allocation.
This removes the prior permanently leaked root on every initialization without
truncating ordinary function or temporary root slots.

## Wire data, completion and cleanup

The owner alone drives source frames/tasks, contexts, channels and synchronization.
A native `HostToken` holds an `Arc` mailbox and numeric owner/operation/task IDs;
it never retains source inputs or frames. Owned `HostWire` values are restricted
to nil, bool, int64, `Vec<u8>`, strings, lists and string-keyed plain records.
Rust ownership and explicit input snapshots preserve unsigned arbitrary bytes
and prevent post-publication aliases. Declared transport errors must be ordinary
wire results, decoded into source errors on the owner. Unexpected worker,
submission, decode or callback faults produce `HostError::Fault`, outside source
panic/recover.

Future keys require a driver-owned registry with opaque native wire IDs and
owner/generation validation. A scalar source heap `H` is not such an ID. Native
input acquisitions retain actual key snapshots through release-before-ACK;
output key ownership transfers explicitly into the registry and has a separate
client/Close lifecycle. The owner decoder validates IDs and constructs traced
source values, rather than cloning source descriptors or handles. This port
implements neither a production key registry nor crypto mappings.

Registration roots declared source captures and parks the task **before**
submission, including synchronous completion. Mailbox validation checks exact
owner/operation/task identity before accepting the first publication or ACK.
Foreign-task faults and acknowledgements leave valid work intact. Explicit
publication sequence determines FIFO application across acknowledged holes and
later publication; live records/ACKs prune with operations. Cancellation observed
before ordinary result application wins; committed success is final. An applicable
adapter fault beats cancellation.

A separate resource ACK permits application only after transport/body/key/input
cleanup. Cancellation notification, dropping a token or native handle, or a
finished/canceled worker is not proof of that release. `launch_host` catches
unwinding worker faults and ACKs after the work closure and its RAII acquisitions
have unwound. The adapter must own acquisitions in that closure with real cleanup
on return/unwind; submission failure must drop/release them before returning.
Adapters with delayed cleanup must register/publish/ACK manually. A broken adapter
that never ACKs prevents safe return; the driver invents no resource release.

Main return, source panic, source fatal, host fault and reset retire the owner,
request native cancellation and wait for every resource ACK without further
source execution. Cleanup is exhaustive after a faulty hook. Retirement clears
mailbox/run/timer/context/task/result/panic roots and detaches blocked direct
send/receive/select, mutex and WaitGroup registrations from retained external
objects. Channel waiters use weak task references and cleared shared payloads;
late callbacks reject before modifying results/select/panic state. Supported
active attach/ready paths validate the task before mutation. Safe Rust rejects
sending those source objects to native threads at compile time.
`lib.task.all` validates its caller before results/spawns and uses the same owned,
removable WaitGroup wait registration; its selective-link mapping explicitly
requires that wait primitive.

Defer runners have actual child/parent `Rc` cycles. Retirement iteratively visits
the reachable frame/value graph and clears parent/a/b links, locals, defers and
primitive captures; merely removing the scheduler task set did not release them.
No source callback runs during frame detachment.

## Collector, contexts and clocks

Allocation stays on the owner; collection runs at safepoints, including while
source is blocked and during driver decode. Independently removable owner root entries
retain operation inputs/key placeholders and decoder/cancel/cleanup captures;
closures capturing `V` must explicitly declare all such captures. A Rust closure
is not automatically traced. Decoder partial outputs need ordinary synchronous
roots at decoder safepoints, and complete decoded results are rooted before
cleanup hooks run. Long-lived operations never use out-of-order LIFO root guards.

Detached callback batches retain **all** captures before the first callback:
parent hooks and children before recursive cancellation, due timer values/task
frames before timer dispatch, and all retirement hooks before collection-capable
cleanup. Forced-collection regressions cover these boundaries and deferred cycles.
Background remains a never-canceling per-thread sentinel and retains no owner
hooks. Child cancel/hook removers reject retired/foreign generations. Nil source
contexts raise Go nil-dereference panic; foreign source contexts cause host fault.
Future capability boundaries must translate invalid inputs into declared errors.

Host time uses an owner-local `Instant` epoch. Checked elapsed duration converts
through `u128` nanoseconds, caps before narrowing to signed int64, and never
regresses under anomalous samples. Positive deadlines saturate. Contexts anchor
at creation, inherit earliest deadlines and immediately observe expired or
nonpositive parents; timers register their captured absolute deadline without
resampling. Cancellation prunes parent/child/timer/hook registrations. A generic
`host_boundary` request timeout anchors before copying/submission and does not
cancel its source parent. It is not a production HTTP mapping.

Timers order by absolute deadline then sequence; due real timers dispatch even
with runnable source. Pending work causes a mailbox/deadline wait, never false
deadlock or virtual fast-forward. The Condvar predicate/version and publication
share one mutex, recheck spurious wakes, and bound each native timed wait to 60
seconds without adding an enormous duration to `Instant`. Poisoned mailbox locks
become explicit faults while allowing resource ACK/retirement to finish. One
source step that never yields can delay observation; nanosecond OS wake precision
is not promised. Wall UTC remains independent and unmapped here.

## Stack and panic limits

For the experimental `--pause-model=native` execution option, async GC roots,
adapter compatibility and additional stack limits, see
[Rust native pausing](rust-native-pausing.md).

Generated ordinary functions use balanced `SourceGuard` scopes, capped at 128
nested ordinary source calls in host mode. Cooperative call chains cap at 512
frames. The dedicated generated owner has a fixed 16 MiB stack. These preemptive
bounds unwind typed `SourceStackFatal`, which source recover cannot absorb; owner
cleanup/ACK precedes exact Go-compatible stack-fatal stderr and exit 2. Genuine
emitted ordinary and cooperative recursive source, including pending native
acquisitions and a parent-controlled release gate, exercise this path. Tests do
not intentionally exhaust Rust's native stack or inject a pretend overflow.

This is a generated-source depth limit, not recovery from native Rust stack
exhaustion or Go dynamic stack growth. Arbitrarily huge native adapter frames,
OOM, catastrophic native failure and `panic=abort` cannot promise cleanup. The
unwind bridge requires `panic=unwind`. It does not replace the application's
panic hook: unexpected native `panic!` calls that hook **before** unwinding, so
application hook stderr/abort is outside deferred source-report guarantees.
Source Go panic and typed host/fatal payloads use `resume_unwind`; their final
executable report occurs after cleanup. Panic payload/destructor destruction can
itself panic; arbitrary hostile-host destructor recovery is not promised.

Primary Rust documentation checked 2026-10-02:
[Rc thread ownership](https://doc.rust-lang.org/std/rc/index.html),
[catch_unwind limits](https://doc.rust-lang.org/std/panic/fn.catch_unwind.html),
[Instant checked arithmetic](https://doc.rust-lang.org/std/time/struct.Instant.html),
[Condvar waiting/poisoning](https://doc.rust-lang.org/std/sync/struct.Condvar.html).
Online std docs identify 1.99; implementation and tests use actual pinned 1.98.

## Evidence and reproduction

Commands use `GOTOOLCHAIN=go1.25.14` and actual Rust/Cargo selected through
`driver.ToolEnv`; reference frontend/profile/lockfiles remain unchanged.
`targets/rust/tests/host_operations_test.sh` compiles the real runtime with rustc,
threshold 1 and serial test threads. The native suite covers synchronous
registration, FIFO/ACK holes, wrong task/generation records, snapshots,
cancellation/success/fault precedence, actual mailbox waits, timeout anchoring,
controlled clocks/deadlines, repeated pruning, every blocked queue, stale callbacks,
forced collection, context/timer batch roots, frame cycles, poison, native RAII,
constructor/dedicated initialization failure and repeated entry. Its 256-operation
stress checks every resumed result and cleanup before main returns. The real
runnable-source deadline test establishes native and source start barriers before
creating its measured deadline.

`tests/integration/rust_host_operations_test.go` first executes unchanged compiler
output, then instruments only copied output at unique asserted markers. Actual
emitted frames contact independent HTTP/TLS servers through a **test-only curl
subprocess adapter**. A `/started` request proves normal contact, source prints
progress while the body is still held, and `/release` permits completion. A
separate contact gate precedes emitted source cancellation. Two entries give
exactly two each normal/started/release/cancel/contact/trusted TLS contacts;
untrusted TLS reaches no HTTP handler. Fourteen native cleanup counts and zero
retained resources precede return. A separate post-acquisition native fault
reaches the real server once and proves its RAII child/file/counter cleanup before
ACK. Cargo release builds compile the same generated host frames.

The independent test-only curl dependency uses the local verified native tool;
its version is retained in `out/rust-host-operations/versions.log`. It is absent
from production mappings/packages, omits the full shared HTTP contract and cannot
be described as production `lib.http.do`. Mock key snapshots are lease models,
not crypto/key adapters.

`tests/integration/rust_host_fatal_test.go` runs genuine emitted recursion,
cooperative recursion, source panic, mutex fatal, source-step/deferred-step fault,
main return and applicable adapter fault with acquired native input/key snapshots.
The parent releases each fresh exact-content cleanup marker only after observing
the owner's actual ACK wait. Deadlock instead proves an ordinary pending mailbox
wait, releases/ACKs work normally, then unchanged source `select{}` reaches genuine
no-pending deadlock. Exact stderr/exit status follows resource release.

All original launcher/session handles, production hashes, failures and logs are
retained under ignored `out/rust-host-operations/`; the worker's terminal handoff
records final required verification outcomes. Historical native failures include
initial syntax/import/RefCell compilation and legacy byte fault-payload checks.
Historical emitted failures include a wrong virtual 126/127ns cancel oracle,
a release-response progress race (fixed with `/started` before source progress),
and a test-only helper absent from the linked runtime. Root's independent probes
found detached parent/timer capture roots and deferred-frame cycles; durable tests
cover each repair. These earlier failures are retained rather than overwritten
or counted as final evidence.

The original broad launcher is terminal: full Rust language (55.851s), all-seven
contracts with C sanitizers (139.558s), compiler internals/corpus/full integration,
actual Chromium browser, TypeScript typecheck, 179 Rust harness cases and Cargo
release/threshold-1 byte oracle comparison passed. Its old production-freeze check
failed after a late decoder-cleanup repair; that historical mismatch is retained.
The broad commands span that live repair and do not all establish one immutable
final source snapshot.

The decoder-repair supplement passed ten native repetitions, genuine emitted
host/fatal cases (56.043s), all-seven contracts (153.239s), harness and freshness.
The later task-all supplement passed all 24 native cases ten times (32.255s),
shared contracts and harness, but its actual emitted `co_task_all` failed because
selective linkage omitted WaitGroup wait. The dependency-repair supplement then
passed that unchanged emitted fixture (4.936s), all-seven contracts (141.194s),
179 harness cases, catalog/freshness and final source/preservation hashes. All
four launchers are terminal; exact statuses, source cohorts and failures remain
in their separate logs and the worker handoff. Existing accepted byte test/bench
artifacts are verification scope, not newly authored Rust-host changes.

This bounded evidence does not establish Phase 4 completion or seven-target SDK
delivery; production adapters, library façades, C host work and real KAS
interoperability remain separate work subject to root acceptance.
