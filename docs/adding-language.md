# Adding a target language

This guide covers adding a compiler backend, its runtime, native capabilities,
exported library API, and verification to Goalchemy. The
[Swift target](swift-target.md) provides a concrete example. Commands run from
the Goalchemy repository root unless stated otherwise. Replace `NEW_TARGET`
and `new_target` with the chosen CLI identifier and package name.

A target is more than a source emitter. It must preserve the accepted Go
subset's observable behavior, produce a usable build directory, and participate
in the same tests as existing targets. A working hello-world program is a useful
first checkpoint, but does not establish language or SDK support.

## Behavior authority and scope

Read these before selecting representations:

- [Language specification](../specs/language.md): accepted constructs, evaluation
  order, value copies, integer and floating-point behavior, errors, and concurrency.
- [Type contracts](../specs/types) and [runtime contracts](../specs/runtime):
  canonical operations, versions, dependencies, cases, and postconditions.
- [Feature manifest](../specs/features.yaml): current supported and rejected
  features and the fixtures that establish them.
- [Usage guide](usage.md): naming, output layout, configuration, and library use.
- [Host operations](host-operations.md): scheduler ownership, asynchronous
  completion, cancellation, cleanup, and real versus virtual time.
- [CI guide](ci.md) and [hardening workflow](hardening.md): prerequisites,
  differential testing, memory checks, and reproducibility.

The frontend and lowered IR already decide source semantics. Reuse them.
Implement the new target's representation of those semantics rather than
changing Go behavior to fit the host. If the existing IR cannot express a
required operation, make that a separately explained compiler change, with
regression coverage for existing targets. A semantic change also needs the
affected specification and versioned contracts updated.

Agree on the delivery scope first:

| Scope | Required result |
| --- | --- |
| Executable prototype | A documented subset compiles and runs; unsupported operations fail clearly. |
| Language target | Released source features and runtime operations pass differential and contract tests. |
| SDK-capable target | Native capabilities, host lifecycle, and exported libraries also work with real consumers. |
| OpenTDF SDK delivery | The separate SDK repository packages the target and verifies TDF3 interoperability with OpenTDF and real KAS. |

Keep partial targets out of supported-feature claims. The target schema has
`planned`, `experimental`, and `released` statuses; these are metadata, not an
automatic implementation of feature gating. The driver and emitter still need
to reject unsupported behavior. Catalog validation requires every type
representation for a target whose status is not `planned`, even while its
function mappings are being completed.

## Repository map

Target names are not discovered uniformly. Some components discover catalog
directories; others contain explicit target lists or dispatch tables.

| Location | Role and work for a new target |
| --- | --- |
| [`internal/emit/`](../internal/emit) | Add the emitter package, following an existing target's organization and `Emit` interface. |
| [`internal/driver/targets.go`](../internal/driver/targets.go) | Register the backend and assemble emitted source, linked runtime, build files, maps, README, and manifest. |
| [`internal/driver/emit.go`](../internal/driver/emit.go) | Check the library-target allowlist and preserve naming options and capability preflight diagnostics. |
| [`cmd/goalchemy/targets.go`](../cmd/goalchemy/targets.go) | CLI target names come from the driver registry; there is no separate static CLI list here. |
| [`cmd/goalchemy/build.go`](../cmd/goalchemy/build.go) | Add the target to the CLI runner map or dispatch; existing shell runners launch `run.sh`, but unregistered targets have no automatic fallback. |
| [`specs/schema/target.schema.json`](../specs/schema/target.schema.json) | Add the identifier to the `target` enum. |
| `targets/NEW_TARGET/target.yaml` | Declare environment, representations, runtime mappings, support files, and harness. |
| `targets/NEW_TARGET/types/` | Hand-maintained shared representations and native adapters, following existing layout conventions. |
| `targets/NEW_TARGET/runtime/` | Hand-maintained implementation files for individual runtime contracts. |
| `targets/NEW_TARGET/tests/harness/` | Hand-maintained JSON Lines transport, codecs, and build/launch scripts; generated case bindings live alongside them. |
| [`internal/specgen/`](../internal/specgen) | Add and register a harness generator; shared spec generation discovers targets from the catalog. |
| [`internal/contracts/`](../internal/contracts) | Catalog/schema validation; directory discovery already reads `targets/*/target.yaml`. |
| [`internal/link/`](../internal/link) | Dependency closure, runtime copying, contract hashes, and build manifest generation. Reuse these mechanisms. |
| [`internal/naming/`](../internal/naming) | Shared source, readable and compact private identifier support. |
| [`assets.go`](../assets.go) | Embeds `specs`, `targets`, `lib`, and `std` in the compiler. Files under the existing embedded trees are included automatically, subject to Go embed rules. |
| [`internal/testutil/fixture.go`](../internal/testutil/fixture.go) | Add a target runner that builds and runs generated fixtures and preserves exit/output observations. |
| [`tests/language/language_test.go`](../tests/language/language_test.go), [`tests/corpus/corpus_test.go`](../tests/corpus/corpus_test.go) | Extend both explicit target lists once ready; `GOALCHEMY_TEST_TARGETS` supports focused development runs. |
| [`tests/contracts/`](../tests/contracts) | Runtime conformance discovers catalog harnesses; add target-specific byte, capability, and lifecycle checks. |
| [`tests/integration/`](../tests/integration) | Add real library consumers and include the target in explicit reproducibility and float-library matrices. |
| [`specs/features.yaml`](../specs/features.yaml) | Add support claims only after their listed fixtures pass on the target. |
| [`toolchains.lock`](../toolchains.lock), [`scripts/`](../scripts), [`ci/`](../ci) | Pin and install compiler/toolchain/dependency prerequisites and prepare clean runners. |
| [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) | Add a `Language and corpus (<Name>)` matrix entry (`suite: language`) and the display name in `scripts/ci-suite.py`; install prerequisites in jobs that execute the target; preserve the aggregate `test` check. |

Search for other explicit target switches and lists before final acceptance.
Registration in one table does not update the others:

```sh
rg -n 'Register\(|harnessGenerators|Runners|targets :=|var targets|case "(go|typescript|python|java|csharp|rust|c)"' internal cmd tests scripts
rg -n 'typescript|csharp' specs/features.yaml specs/schema/target.schema.json .github toolchains.lock examples docs README.md
```

Review the matches rather than replacing every existing language name. Some
describe an intentional platform-specific path or a historical result.

## 1. Choose the environment and representations

Record the minimum compiler/runtime, supported OS and architecture, executable
command, library packaging convention, and native dependencies. Pin exact
versions and lock dependency graphs where the toolchain supports that. A local
developer cache must not be required to build on CI.

Pick an existing backend as a structural reference, then inspect its emitter,
target mapping, runtime, harness, and library-boundary documentation together.
Choose by similarity of representation and execution, not syntax alone. For
example, a class-based backend demonstrates shared storage and frame objects;
Rust documents explicit roots and tracing; C documents native ownership.

Write a representation table in the new target's documentation. At minimum,
answer the following questions before translating operations:

| Source behavior | Representation decisions and cases to cover |
| --- | --- |
| Booleans and integers | Preserve widths and signedness; `int`/`uint` are 64-bit. Define wrapping, conversions, signed division, remainder, shifts, and unsigned comparison. Host overflow traps or floating-point division must not replace Go behavior. |
| `float32` and `float64` | Preserve precision at the specified boundaries, signed zero, NaN, infinities, conversion behavior, and collection equality/key behavior. See [float support](float-support.md). |
| Strings | Preserve arbitrary bytes, byte indexing/length, and specified UTF-8 decoding. A host Unicode string alone is not sufficient unless its encoding preserves every source byte. |
| Structs and arrays | Copy values recursively at Go copy boundaries. Preserve stable addressable storage where pointers or closures observe it. |
| Slices | Separate header from shared backing storage; retain offset, length, capacity, and nil versus empty. Preserve reslicing, append aliasing, overlap-safe copy, and zeroing of new elements. |
| Byte slices and arrays | Use a native byte buffer where practical, while retaining slice headers and Go aliasing/copy behavior. Verify no accidental per-byte boxing or conversion at every operation. |
| Maps | Define nil handling, comparable key encoding, lookup zero values, iteration, and source-visible equality rules. Host object equality or insertion order must not become a new source guarantee. |
| Pointers | Define identity, nil, stable cells, and references into supported addressable values. Do not expand the source subset to include currently rejected element pointers. |
| Interfaces and errors | Preserve dynamic type identity, method tables, assertions, interface equality, and an interface holding a typed nil. Errors remain ordinary values. |
| Functions and closures | Preserve capture identity, function nil behavior, multiple results, variadic calls, and source call evaluation order. |
| Panic, defer, recover | Distinguish source panic, fatal runtime conditions, compiler defects, and host faults. Preserve deferred calls and mutation of named results during unwinding. |
| Globals and initialization | Preserve dependency/package initialization order and the documented program or library lifecycle. |
| Tasks and channels | Implement cooperative frames, owner-controlled scheduling, channel queues, select, and cancellation rather than assuming host threads have the same semantics. |
| Opaque capabilities | Keep native key/resource handles opaque, with declared ownership and close behavior. Type-only uses also need a real representation. |

Host memory management needs its own decision. Reference counting can retain
cycles; tracing or explicit roots need correct safepoints; native pointers need
ownership and lifetime rules. Exercise repeated operations, cyclic structures,
retained handles, and shutdown instead of assuming the host's default memory
model is enough.

Checkpoint: the table states how each current type contract is represented,
which operations remain unsupported, and how those operations fail.

## 2. Add target metadata and the smallest executable

Add the schema identifier and `targets/NEW_TARGET/target.yaml`. Start from an
existing mapping's structure, but replace its representations and dependency
information with real choices for the new target. Do not declare an operation
implemented merely by copying its mapping.

Important mapping fields:

- `representations`: canonical type ID, its current contract version, and an
  explanation of target storage and semantics.
- `functions`: canonical operation ID, contract version, callable `symbol`,
  `implementation` path, optional target-specific `requires`, and `harness`
  call template where conformance cases are available.
- `support_files`: shared runtime representations/adapters required in emitted
  output, separate from the implementation files selected by linking.
- `environment`: runtime, minimum version, build instructions, and native
  dependencies.
- `harness`: hand-maintained support files, generated binding path, and launch
  command. Add it when a real generator and transport exist.

Paths are relative to the target directory. Current catalog validation rejects
duplicate operation IDs, duplicate implementation paths across operations,
missing implementation/support files, mismatched contract versions, and
missing canonical or target-specific function dependencies. Shared code
belongs in support files or helpers used by individual operation files.

Create `internal/emit/new_target/`, register the emitter with
`driver.Register`, and assemble output through the driver. Reuse `link.Plan`,
`link.CopyRuntime`, and `link.WriteManifest`; link the closure of contracts the
program actually uses. Include source mapping or line-location metadata,
build/run instructions, and an output README.

Compile a scalar fixture, build it with the actual host toolchain, and compare
its output and exit status with native Go. Use fresh output directories when
changing the layout so stale files cannot make a broken build appear valid.

```sh
go run ./cmd/goalchemy spec validate
go run ./cmd/goalchemy compile -target NEW_TARGET -out out/new-target-smoke ./tests/language/testdata/scalar_basic
go run ./cmd/goalchemy run -target NEW_TARGET ./tests/language/testdata/scalar_basic
```

The final command requires registration in the CLI runner map or dispatch and
a working launch script where that is the selected convention. This is separate
from the test runner in `internal/testutil`. Run generated output from
outside the source checkout as well: the standalone compiler's embedded assets
and generated runtime must not depend on untracked local source files.

Checkpoint: metadata validates, CLI registration works, and a complete emitted
directory builds and runs independently.

## 3. Implement the sequential language and naming

Work through existing fixtures in small groups: scalars/conversions/control
flow, strings, aggregates/slices/maps, pointers/interfaces/methods, closures,
defer/recover, package initialization, and pure `std/` packages. Add the runtime
operations each group needs and retain the dependency closure in the mapping.

Emit from the typed IR and its explicit copy/evaluation-order operations.
Do not reconstruct source semantics from target syntax. Shared IR/effect
changes must preserve existing backends; target-specific mechanics belong in
the emitter/runtime. Missing capabilities should fail preflight before opaque
native types become placeholder target objects.

Use the shared naming index in `internal/naming`. Source names are the
default; `--names=readable` and `--names=compact` are opt-in. Give the emitter a
`NameRules` with the target's reserved words, and start every identifier the
emitter invents with `naming.Prefix` (or name it through `Names.Derived` or
`Names.Type`), so source names never meet it. Every mode must preserve public API
names, runtime/capability symbols, serialized type identities, and behavior.
Choose legal target prefixes and separators; test reserved words, Unicode,
shadowing, repeated names across packages, anonymous types, and synthesized
temporaries. Public API collisions need a useful boundary diagnostic rather
than invalid output or silent renaming.

Add focused emitter tests, including naming tests, in the new emitter package.
Extend the runner map in `internal/testutil/fixture.go` before using the shared
fixture harness. Its observations must include stdout, stderr, and exit status;
compiler/build diagnostics must not pollute program observations.

Checkpoint: sequential fixtures match native and lowered Go, including failure
and panic cases, under both naming modes. Pure `std/` code works by compilation;
it does not require a second hand-written implementation in the new language.

## 4. Add the runtime conformance harness

Add `internal/specgen/<target>harness.go` and register its generator in
`harnessGenerators`. Use an existing generator to understand typed arguments,
call templates, and canonical cases; implement a codec for the new runtime's
representations rather than copying another host's values blindly.

Under `targets/NEW_TARGET/tests/harness/`, provide the transport, codec,
support helpers, and launcher. The launcher builds the actual runtime and
serves a long-lived JSON Lines process. Keep build diagnostics on stderr and
reserve stdout for protocol messages.

Follow the protocol consumed by `internal/conformance`: version and request
ID, case identifier, typed setup values, result/status records, source panic,
and postcondition observations. Preserve exact integer bits, arbitrary bytes,
nil/empty distinctions, identity, and aliases through setup and response
encoding. Controlled scheduler observations must be reproducible.

Generated bindings come from `spec generate`; do not hand-edit them or
`targets/NEW_TARGET/spec/`. Supply real `harness` templates for mapped
operations with cases. `collectCases` omits mappings without templates, so a
green harness with missing templates is not proof of complete coverage. Review
the executed cases against the intended runtime contract set and explain any
intentional gaps.

```sh
go run ./cmd/goalchemy spec generate
go run ./cmd/goalchemy spec generate -check
go run ./cmd/goalchemy test -target NEW_TARGET -v
```

Generation also updates the shared [library reference](library.md). Review and
commit generated specifications and harness bindings with their inputs.
If an implementation file changes later, regenerate its recorded hash.

Checkpoint: the real target harness executes the intended canonical cases,
including aliases and error paths, and generated files are current.

## 5. Implement cooperative execution and host I/O

Port the scheduler contract and resumable-frame emission together. Cover task
creation, channels, select, synchronization, context cancellation, virtual
timers, deadlock, and panic propagation. Source tasks do not require one native
thread each. Ordinary host async/await is an implementation tool, not a
replacement for Goalchemy's scheduling contract.

Preserve the split between deterministic virtual execution and the explicit
real host entry described in [host operations](host-operations.md):

- The source owner alone changes tasks, frames, contexts, queues, and result
  vectors. Host workers submit immutable completions through the mailbox.
- Register and park before submission so immediate completion follows the
  same path as delayed completion.
- Pending native I/O prevents false deadlock and virtual clock fast-forward.
  Wait for a wake signal or real deadline when no source task is runnable.
- Honor cancellation/deadline observation order, duplicate/stale tokens,
  foreign owners, and owner generation retirement.
- Deliver terminal results after native cleanup acknowledgement. Shutdown,
  panic, and host faults cancel and drain pending work before releasing owners.
- Preserve the distinction between declared error results, source panic, and
  host faults that source `recover` must not swallow.

Use existing target host-operation docs and tests as concrete references.
Test synchronous completion, delayed completion, completion/cancellation races,
timeouts, host failure, shutdown with outstanding work, reentry where supported,
and repeated invocations. Document recursion/resource limits where the target
needs them rather than silently accepting different semantics.

Checkpoint: cooperative fixtures and host lifecycle tests pass; real I/O can
wait and complete without deadlock or callbacks mutating source state.

## 6. Implement native capabilities and dependency delivery

Inventory the current `lib/` declarations and matching canonical contracts.
Implement the capability set needed for the promised delivery scope, including
crypto, HTTP, encodings, clock, callbacks, and checksum for the OpenTDF SDK.
Existing mappings are examples, not authority for changing behavior.

Use maintained native libraries for cryptography. Match algorithm parameters,
key representations, PEM/DER formats, nonce/tag conventions, ownership, input
limits, validation, and error results to the contracts. Verify actual operations
and malformed inputs; successful native key construction alone is insufficient.

Connect suspending capabilities to the host mailbox/lifecycle. HTTP checks need
real local transport cases, cancellation/deadline behavior, response bounds,
and cleanup. Crypto handles must close correctly and reject invalid or closed
uses according to their contracts. See [checksum mappings](checksum.md) for
the existing distinction between native APIs, maintained dependencies, and
portable fallbacks; acceleration is controlled by the host implementation.

Pin native dependencies, commit relevant lockfiles, and ensure generated build
metadata delivers the dependencies required by linked contracts. Check both a
small program that needs no native capabilities and a program that uses them.
Bootstrap clean-runner prerequisites before timed tests; a warm developer cache
must not conceal a missing fetch/restore step.

Keep required runtime and third-party license/notice files with distributed
packages. Dependency delivery includes their applicable attribution obligations,
not only compiler flags and downloaded binaries.

Checkpoint: native adapters pass contract and focused transport/crypto checks,
and generated consumers build with the documented locked dependencies.

## 7. Export libraries with a usable public API

Add non-`main` package emission and update the library-target allowlist in
`internal/driver/emit.go`. Follow existing target `library.go` implementations
and boundary docs for supported public value trees, opaque handles, callbacks,
and final error results. Unsupported ABI types need boundary diagnostics.

Define native public types and wrappers separately from compiler-private
representations. Test exact integer values, byte buffers containing zero and
non-UTF-8 bytes, nested aggregate copies, nil/empty values, multiple results,
errors, and floating-point values. Decide synchronous/asynchronous entry points
from the host execution model and supported exported effects.

For host work that outlives a call's submission, test caller mutation of input
buffers and caller release of handles. An adapter must use owned input data or
an explicitly documented retained lifetime; asynchronous work must not observe
an unexpected mutation or access released storage.

Document initialization, owner serialization or concurrency, cancellation,
reentry, error conversion, and resource release. Do not assume executable global
lifecycle is an acceptable SDK lifecycle. Define which values are detached
copies and which objects retain ownership across the call boundary.

Add an actual native consumer under `tests/integration/` that imports/builds
the generated package and calls its public API. Inspecting generated source or
calling internal runtime helpers does not establish exported-library support.
Include the target in float-library consumer coverage under both naming modes.

Checkpoint: consumers can use the generated package through its public API
without knowledge of private IR names or manual scheduler setup.

## 8. Wire complete coverage and clean-runner CI

Add the target to the relevant explicit lists after implementation is ready:

- Language target list; naming tests share this list.
- Reproducible-output and example matrices, corpus, memory/fuzzing matrices,
  and other suites applicable to the target and its advertised scope.
- Float-library integration matrix and its native consumer dispatch.
- Capability, byte-storage, lifecycle, and library tests for the new target.
- Feature manifest target claims, with existing fixture references preserved.

`TestFeatureManifest` checks that supported features name tested targets and
existing fixtures. It does not by itself execute those fixtures or verify
native capabilities. Likewise, schema validation and generation checks do not
prove runtime behavior.

The current short language and conformance suites limit general target coverage
to Go and TypeScript. Adding a language to a list therefore does not ensure it
runs in short CI. Keep explicit unshort acceptance coverage for the new target.
The float and naming CI jobs use the language target list; runtime conformance
discovers catalog harnesses. Inspect actual subtest output to verify inclusion.

The CI suite script discovers new Go packages and integration top-level tests,
but does not infer new native toolchain prerequisites or target entries inside
tests. Update `scripts/ci-bootstrap.sh`, toolchain/dependency pins, environment
discovery where necessary, and workflow setup in every affected parallel job.
If adding a target-specific job, include it in the required aggregate gate.
See [CI coverage](ci.md) for job boundaries and local reproduction.

### Focused development commands

After the emitter package, runner, and harness exist:

```sh
go test ./internal/emit/new_target ./internal/driver ./internal/specgen -count=1
FIXTURE=scalar_basic GOALCHEMY_TEST_TARGETS=go,NEW_TARGET go test ./tests/language -run '^TestFixtures$' -count=1 -v
GOALCHEMY_TEST_TARGETS=go,NEW_TARGET go test ./tests/language -run '^TestFixtures$' -count=1 -v -timeout 30m
go test ./tests/contracts -run '^TestTargetConformance$/NEW_TARGET$' -count=1 -v -timeout 15m
```

`FIXTURE` is a substring filter, not an exact name selector; for example,
`FIXTURE=floats` selects sequential, cooperative, and conversion-panic float
fixtures. `GOALCHEMY_TEST_TARGETS` is implemented by `TestFixtures`; it does
not filter `TestNamingModes` or arbitrary integration tests. Avoid assuming one
environment variable selects the target in every suite.

### Acceptance commands

Run these with the prerequisites in [ci.md](ci.md), plus the new target's
toolchain and native dependencies. Timeouts are the existing baseline; if the
new target needs an adjustment, explain it rather than hiding omitted coverage.

```sh
go vet ./...
make spec-check
go test -short -timeout 30m ./...
python3 scripts/ci-suite.py language --target NEW_TARGET
go test -v -timeout 15m ./tests/language -run '^TestNamingModes$' -count=1
go test -v -timeout 15m ./tests/contracts -run '^TestTargetConformance$' -count=1
go test -v -timeout 15m ./tests/integration -run '^TestReproducibleOutput$/NEW_TARGET$' -count=1
go test -v -timeout 15m ./tests/integration -run '^TestGeneratedFloatLibraries$/(readable|compact)/NEW_TARGET$' -count=1
python3 scripts/ci-suite.py --verify-plan
```

Also run the new target's named capability, byte-storage, host lifecycle,
public-library, float-library, and memory checks. The commands above are not
a substitute for those tests. Reproducibility and naming commands require the
target to have been added to their matrices. A Go test command can succeed
with no matching subtest; inspect the verbose output and require the expected
target cases to have actually executed.

Run focused checks while developing, then the agreed full acceptance once the
target is ready. Repeat checks when changed code, failures, or unresolved
concerns justify it; avoid repeatedly rerunning unchanged expensive matrices.
Record commit, toolchain/dependency versions, commands, and material limits in
the implementation PR.

## 9. Verify SDK delivery separately

Compiler acceptance lives in Goalchemy. OpenTDF TDF3 delivery lives in the
separate SDK repository and needs its own source/build registration, host
integration, package wrapper, and target-specific CI coverage.

For that workload, test both directions with OpenTDF and real KAS: decrypt SDK
output with the reference SDK, and decrypt reference output with the generated
SDK. Include the protocol/key/auth profiles in the agreed SDK scope and actual
native package consumers. Compiler fixture success cannot replace those
interoperability checks.

Benchmark only after correctness. Compare equivalent packaging, lifecycle,
warmup, inputs, authentication, and operations. Report performance as measured
behavior, not as a support gate or an assumed property of the new language.

## Common failure patterns

| Symptom | What to inspect |
| --- | --- |
| Target YAML rejected | Schema enum, directory/identifier agreement, type representations, versions, dependency closure, and existing implementation files. |
| CLI reports unknown target | Driver registration; a mapping alone does not register an emitter. |
| Executables work but libraries are rejected | Driver library allowlist, emitter's library path, and boundary validation. |
| `spec generate` reports no harness generator | Registration in `harnessGenerators`, not just the harness command in YAML. |
| All reported contract cases pass but an operation is broken | Missing call templates/cases, wrong codec conversions, or assertions that do not observe aliases/postconditions. |
| Slice tests diverge after append/copy | Shared backing storage, capacity, zeroing, overlap, and copy boundaries. |
| Small inputs work but large integers/floats differ | Host overflow/division rules, width normalization, float32 rounding, special values, and canonical transport. |
| HTTP hangs or reports deadlock | Owner registration before submission, pending-I/O accounting, wake delivery, monotonic deadlines, and cleanup acknowledgement. |
| Local build passes and clean CI fails | Missing toolchains, dependency locks/fetches, environment discovery, or reliance on ignored build artifacts. |
| CI is green but the target never ran | Short-mode restrictions, explicit target lists, absent runner/consumer dispatch, or a test selector matching zero cases. |
| Output changes between builds | Unsorted map traversal, unstable IDs/names, embedded paths, timestamps, or missing manifest files. |

## Completion checklist

- [ ] Delivery scope, supported environments, representations, and limits are documented.
- [ ] Schema, driver registration, CLI run path, runner, and harness generator include the target.
- [ ] Emission uses shared IR semantics and rejects unsupported behavior clearly.
- [ ] Runtime mappings use current contract versions and complete dependency closures.
- [ ] Canonical cases actually execute, including failure, identity, aliasing, and postconditions.
- [ ] Integers, floats, byte strings, aggregates, slices, maps, interfaces, and panic behavior match the source contracts.
- [ ] Cooperative frames and host I/O obey owner, cancellation, deadline, and cleanup rules for the advertised scope.
- [ ] Native capabilities and dependencies build on a clean runner.
- [ ] Public library consumers pass under source, readable and compact naming modes.
- [ ] Generated specifications, harness bindings, and library reference are current.
- [ ] Reproducible output, source locations, manifests, and standalone builds are verified.
- [ ] Applicable memory/lifetime checks pass, including repeated calls and cyclic/retained values.
- [ ] Feature claims and explicit test matrices include the target only where verified.
- [ ] CI logs show the new target executed; required aggregate checks include its coverage.
- [ ] README target table, usage/output docs, CI prerequisites, and per-target boundary/host/byte docs reflect the implementation.
- [ ] SDK interoperability with real KAS is verified separately if SDK delivery is part of the task.

Update this guide when the extension points or acceptance process change.
Keep historical plans as context; use the current source, contracts, and
executed tests to decide what a new backend must implement.
