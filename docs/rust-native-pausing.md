# Rust native pausing

`goalchemy compile -target rust --pause-model=native` emits suspending functions
as `async fn` and their static calls as boxed `.await` expressions. The default
remains `frames`. This is step 7 of [issue #92](https://github.com/eugenioenko/goalchemy/issues/92),
tracked in [issue #97](https://github.com/eugenioenko/goalchemy/issues/97).

```sh
goalchemy compile -target rust -gate cooperative --pause-model=native \
  --line-directives=false -out out/rust ./cmd/app
```

The compiler uses the existing target-independent suspension analysis. It fully
structures native bodies and folds single-use pause results when safe. The
`PauseAlone` restriction keeps values evaluated before an await in registered
locals instead of untraceable Rust expression temporaries.

## Execution and compatibility

The existing scheduler owns the executor. One adapter frame per async entry
holds a pinned, owner-local future. Only the scheduler polls it, from the same
turn loop that steps frames. A no-op waker cannot schedule another task. A task
whose primitive blocks returns `Pending`; the scheduler readies it and polls it
again in FIFO order. Driver idle handling, timers, host mailbox publication,
deadlock detection, and host-mode boundaries use the existing frames driver.
Task bodies never poll other task bodies recursively.

A primitive that completes immediately does not await. Function values,
interface methods, goroutine starts, pausing defers, and library exports retain
their frame-convention entry points. The native frame adapter awaits those
entries on the same task, preserving recover identities. Host-facing
`Operation<T>`, handle, stream and provider APIs remain unchanged. No OpenTDF SDK
adapter changes are included.

Source panics cross awaits through a source-only catch and explicit async defer
runner. Scheduler failures stay in the driver and cannot be recovered as Go
panics. Shutdown drops parked futures without running source defers, then
detaches the owner graph and waits for native resource cleanup as before.

## Garbage collection

Each async activation registers its local storage with its task. Registrations
are removed by identity, allowing futures from different tasks to interleave and
parked futures to be dropped safely. Synchronous activations keep the existing
thread-local root stack. Deferred captures, panic state, and adapter arguments
are explicitly rooted; native closure captures alone are not GC roots.

Set `GOALCHEMY_GC_THRESHOLD=1` to collect at every safepoint, including points
with no intervening allocation. This stress mode covers both pause models.

## Limits and validation

- Resuming a boxed future chain re-polls its parents, so resume work grows with
  pausing call depth. This implementation does not bypass parent polls.
- Native poll and drop recursion use the Rust stack. Host cooperative calls
  retain the 512-depth guard, counting native activations and non-native frames
  without double-counting native adapters. Virtual native execution caps async
  activation depth at 4,096 and reports a stack fatal before exhausting the
  native stack. The existing synchronous host-call limit is unchanged.
- Static pausing calls allocate boxed futures and rooted activation storage;
  indirect calls additionally use frame adapters. Allocation and performance
  measurements are deferred to the parent issue's final cross-language run.
- Native recursive async output requires a Rust compiler supporting recursive
  async calls through boxed futures; development validation uses Rust 1.98.

`TestRustPauseModelsAgree` compares stdout, stderr and exit status against frames
for every cooperative fixture under seeds 1, 7 and 4242. The focused local checks
cover GC across await, pausing defers and recovery, function values and interface
calls, task ordering, channels, select, timers and host cleanup. The existing
frames golden is preserved, with a separate `rust-native` golden.

The optional **Native pause model (Rust)** CI job runs the full fixture and
agreement suites, cooperative GC stress, frames regression, corpus and Rust
integration checks. It does not join the required native-pause job. Run its plan
with `python3 scripts/ci-suite.py pause-rust --plan`.
