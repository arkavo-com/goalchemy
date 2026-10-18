# Goalchemy implementation plan

Status: implementation baseline. Updated October 4, 2026.

Goalchemy is a transpiler for a restricted, Go-compatible source language. It lowers one typed program into Go, TypeScript, Python, Java, C#, Rust, and C. A shared semantic model, individually specified runtime functions, and differential tests keep those outputs consistent.

The current project builds the compiler, its language runtimes, contract generators, and conformance tooling. OpenTDF is the motivating source workload. Its source survey is deferred by project direction; building and distributing seven OpenTDF SDKs is a later project phase.

This document selects concrete defaults so implementation can begin. Changes are allowed as evidence arrives, but a semantic change must update the language specification, affected contracts, and conformance tests together. A planned feature is not advertised as implemented until its acceptance tests pass.

## Readable generated names and release

Keep Go source identifiers by default across Go, TypeScript, Python, Java, C#,
Rust, C and Swift, renaming only on a collision (issue #93); readable Go-derived
names stay available with `--names=readable`. Provide opt-in `--compact-names`
for generated internal identifiers, with equivalent command/config behavior.
Public export and wire names stay consistent between modes. Escape target keywords and invalid
identifier characters, disambiguate packages/shadowing, and provide deterministic
names for anonymous types and compiler temporaries without changing runtime IDs
or Go value semantics.

Acceptance: shared naming and CLI/config tests; source-level emitter tests;
all-seven differential checks of both naming modes including collisions, Unicode,
anonymous types, closures and cooperative execution; actual importing library
consumers in both modes; existing short suite, vet and spec freshness; passing
hosted CI on the PR head. Update user/compiler documentation, merge the accepted
PR and publish a signed Goalchemy release with a verified external install.
Work proceeds in `feat/readable-names`, preserving the original SDK checkouts.

Implementation acceptance: all seven target emitters support both naming modes;
shared naming/config/CLI tests and race checks pass. The all-seven differential
matrix (three fixtures in two modes) and fourteen importing library consumers
passed locally. Focused tests also cover anonymous/public-name collisions,
dependency aliases, private method package identities and reserved façade names.
User documentation is updated. The broad local suite identified old numeric-name
assumptions in C/Rust byte helper probes and a C# GC probe; these were corrected
without weakening native storage/retirement checks. The affected checks passed,
including the independent C# consumer's 93 checks in both modes. Other local
suite packages passed. Final vet and all 736 generated-spec freshness checks
passed. Hosted CI on the final PR head remains the merge/release gate.


## Floating-point follow-up

Add `float32` and `float64` across all seven targets, including constants,
arithmetic, comparisons, conversions, collections and exported APIs. Preserve
Go semantics and verify the resulting feature through native-Go differential
tests, all-target exported-library consumers, runtime contract conformance and
passing hosted CI. Publish the accepted implementation as a pull request.
Complex numbers and the full `math` library remain outside scope.

The detailed requirements and acceptance status are in
[float support](docs/float-support.md). All-target language fixtures and native
library consumers verify both precisions; CI also requires runtime contract
conformance and the existing regression suite.

## 1. Scope and completion criteria

The full compiler roadmap includes all seven targets. The first usable compiler release supports lowered Go and TypeScript; the other five follow the same contract and test system.

In scope:

- Go package loading and type checking, source selection, semantic subset validation, and diagnostics.
- A target-independent typed intermediate representation, semantic lowering, and target emitters.
- Core runtime types and operations, with one implementation file per runtime function and a matching specification file.
- External capability declarations, stub generation, fake implementations, and adapter conformance tooling.
- Language tests, stateful differential tests, reproducible generation, and regression reduction.

Outside the current delivery scope:

- Production crypto, ZIP, HTTP, JWT, and OpenTDF adapters.
- KAS integration, authentication workflows, TDF golden archives, and cross-SDK interoperability.
- Public encrypt/decrypt APIs, SDK package publishing, and a production C FFI SDK.
- Automatic migration of arbitrary Go applications and unrestricted standard-library support.
- The OpenTDF source survey and its pinned source fixture.

The first release is complete when the CLI can validate and compile documented sequential Goalchemy programs to Go and TypeScript, both outputs run, the required runtimes are packaged with them, and the supported-feature conformance suite passes against the Go oracle. Later releases extend the supported-feature manifest and target matrix.

## 2. Baseline decisions

| Area | Decision |
| --- | --- |
| Project and language name | Goalchemy |
| Source files | Ordinary `.go` files; no new syntax or custom parser |
| Source language baseline | Go 1.25 syntax and typing, restricted by the Goalchemy feature whitelist |
| Reference toolchain | Go 1.27.1, explicitly selected in development and CI; changing it requires an oracle compatibility run |
| Source platform | `GOOS=linux`, `GOARCH=amd64`, `CGO_ENABLED=0`, plus explicit build tags |
| Integer profile | `int` and `uint` are always 64-bit, independent of the output platform |
| Compiler implementation | Go; `go/packages`, `go/ast`, `go/types`, and `go/constant` |
| IR | A small project-owned typed IR; no direct AST-to-target emitters |
| Source generics | Rejected in the initial language; add finite specialization only when a real workload justifies it |
| Runtime specialization | Compiler-generated concrete operations for the types actually used; this does not require source generics |
| Spec format | Restricted YAML validated as JSON-compatible data against JSON Schema 2020-12 |
| Shared types | One canonical semantic contract per type family, with target-specific representations |
| First output targets | Lowered Go, then TypeScript running on Node.js |
| Browser support | A later compatibility profile; browser APIs are not assumed in the first TypeScript runtime |
| Concurrency | Cooperative source tasks, with explicit suspension and one logical task executing at a time |
| Scheduling implementation | Shared state-machine lowering and a specified scheduler; no source task mapped directly to an unconstrained host thread |
| Rust memory | Safe handle-based tracing heap initially; no automatic weak-reference rewrites |
| C memory | Boehm GC initially; generated execution stays single-threaded |
| Overrides | Explicit, version-checked source replacements; no silent name-based shadowing |
| Repository | One monorepo; independently testable compiler and target runtime directories |
| Generated naming | Source names by default, renamed only on a collision; opt-in readable and compact internal names; consistent public wrappers in every mode |
| Optimization | Correctness first; optimizations must preserve observations and pass differential tests |

The installed workspace toolchain was Go 1.25.1 when this plan was written. Bootstrap must select the reference toolchain before oracle results are recorded. Toolchain installation is implementation work, not a completed action in this plan.

## 3. Repository layout

```text
github.com/eugenioenko/goalchemy/
  plan.md
  specs/
    language.md
    schema/
      function.schema.json
      type.schema.json
      target.schema.json
    declarations/                 # Go declarations for external capabilities
    types/                        # Canonical type behavior contracts
      integer.yaml
      string.yaml
      slice.yaml
      map.yaml
      pointer.yaml
      interface.yaml
      error.yaml
    runtime/                      # Canonical function behavior contracts
      core/
        slice_get.yaml
        slice_append.yaml
        integer_add.yaml
      concurrency/
  cmd/github.com/eugenioenko/goalchemy/
  internal/
    frontend/
    subset/
    ir/
    lower/
    contracts/
    emit/
    diagnostics/
  targets/
    go/
      target.yaml
      runtime/                    # One runtime function per implementation file
      types/                      # Shared representation declarations
      spec/                       # Generated specifications matching runtime files
      tests/
    typescript/
      target.yaml
      runtime/
      types/
      spec/
      tests/
    python/
    java/
    csharp/
    rust/
    c/
  tests/
    language/
    contracts/
    integration/
    corpus/
  tools/
    differential/
    specgen/
  overrides/
  examples/
```

This is the intended layout. Create directories when they contain real work, rather than scaffolding empty placeholders. The language specification is maintained now; the schema files and generators are milestone deliverables.

## 4. Runtime function and type contracts

### Canonical contracts

Each runtime function has one stable ID, such as `core.slice.get`, and one canonical YAML file. Filenames are organizational; IDs are the compiler's binding keys. Schema version and semantic contract version are separate fields.

For external capabilities, Go declarations own callable signatures and record shapes. Their YAML contracts reference fully qualified declaration symbols. The generator extracts signatures with the Go toolchain rather than maintaining a second editable signature.

For core intrinsics, signatures are determined by the typed operation: a slice access over `[]int32` returns `int32`. A canonical family spec describes this relationship with structured type references. Its type variables are specification metadata, not accepted generic Go source. Instantiation creates a concrete signature and a distinct target symbol.

Each function contract contains:

- Stable ID, schema version, semantic version, and signature source.
- Inputs and outputs, type constraints, and dependencies on type or function contracts.
- Zero and nil behavior, bounds behavior, conversions, and overflow rules where relevant.
- Observable mutations, aliasing, value-copy behavior, and allocation of semantic storage.
- Returned error behavior and panic categories.
- Suspension classification: `never` or `may`.
- Determinism and observation rules, including any allowed nondeterminism.
- Named conformance cases with structured setup, operations, expected results, and postconditions.
- Short explanatory prose for behavior not conveniently expressed in test data.

Allocation contracts describe semantic storage and observable identity. They do not require every backend to perform the same host allocations or garbage collections.

### Spec validation

Accept YAML mappings, sequences, strings, booleans, null, and bounded metadata integers. Require string mapping keys. Reject duplicate keys, aliases, custom tags, merge keys, and non-finite numeric values. Encode test-language integers as typed decimal strings, including small values, so the test representation never depends on host numeric precision.

JSON Schema checks document shape and rejects unknown fields. A second semantic validator resolves symbols, type references, function dependencies, contract versions, and test operations. Schema validation alone cannot establish semantic correctness.

### Per-language files

Every runtime function implementation has a matching generated target spec. A target spec combines its canonical contract with the concrete signature, representation references, symbol name, and implementation path. It records the canonical contract hash. Hand edits to generated specs fail the generation check.

For example, a concrete C slice access can have `runtime/core/slice_get_i32.c` and `spec/core/slice_get_i32.yaml`. Both trace back to `core.slice.get`. Each separately callable helper also gets its own file and contract; declarations, import lists, registration tables, and generated headers may be shared.

Target-specific representation choices live in hand-maintained type mapping files. Those mappings can explain how a target implements a contract but cannot weaken it.

### Shared type contracts

Type contracts define observable behavior rather than imposing the same host layout:

| Type family | Required contract |
| --- | --- |
| Integers | Width, signedness, zero, conversions, arithmetic, shifts, comparisons |
| Strings | Immutable arbitrary bytes, byte indexing and length, rune decoding |
| Arrays and structs | Value copying, field and element behavior, equality where permitted |
| Slices | Shared backing storage, offset, length, capacity, nil, copying of headers |
| Maps | Shared identity, key equality, nil behavior, lookup presence, iteration rules |
| Pointers | Stable logical identity, dereference, nil, addressable storage locations |
| Interfaces | Dynamic type plus value, typed nil, dispatch, assertions, equality |
| Functions | Closure environment, capture identity, nil calls, argument and result behavior |
| Errors | Ordinary Go error values, identity, matching, wrapping, and observable messages |
| Tasks and channels | Task state, wait registration, channel identity, close, cancellation |

## 5. Compiler pipeline

### Loading and source selection

Load packages with `go/packages` under the locked source profile. Type-check the selected program with the Go toolchain. Start reachability from configured exported roots or `main`, include reachable declarations and initialization, and resolve interface calls conservatively.

The ordinary Go toolchain may load dependency declarations and packages needed for typing. The transpiler only accepts executable dependency behavior through explicit source inclusion or a registered external mapping. Package imports and initialization side effects must not disappear because a function call is unreachable.

When dynamic dispatch cannot be resolved conservatively within the supported type universe, report an unsupported boundary. Do not guess that a method is unused.

### Subset validation

Validate syntax, types, operations, built-ins, address-taking forms, imported symbols, and initialization. Whitelisting an AST node does not whitelist every meaning of that node. Unknown cases fail closed.

Collect independent errors in stable source order. Each diagnostic contains a code, severity, source span, symbol, unsupported feature, and one-line remedy. Human output and JSON output carry the same information. Invalid Go may prevent further semantic analysis; report recoverable findings without claiming that every downstream error was discovered.

### Typed IR

Use typed functions, basic blocks, explicit branch targets, local storage cells, and typed values. Keep source spans and stable symbol/type IDs throughout lowering. The IR must distinguish value copies from shared references and must represent side-effect sequencing explicitly.

Initial operations include constants, local storage, calls, returns, branches, value copying, integer operations, aggregate access, slice/map operations, boxing, and interface dispatch. Later feature gates add closures, deferred calls, panic state, and task suspension.

Use `go/constant` for compile-time constants. Do not route untyped Go constants through host floating-point values.

### Semantic lowering

Resolve embedding and promoted methods, multi-value assignment, evaluation order, short-circuiting, closure capture, value-copy boundaries, and runtime intrinsic selection centrally. Emitters receive these decisions already made.

Compute a conservative `may_suspend` effect over calls. Runtime contracts seed the analysis; unresolved callback or interface effects are conservatively suspending. Propagate effects through recursive call groups to a fixed point. Ordinary scalar operations remain synchronous.

Lower suspending functions into explicit resumable frames before target emission. This gives C and every other target the same task semantics without relying on native coroutine syntax. Non-suspending functions use ordinary calls.

### Emission and linking

Emit target declarations and control flow plus calls to generated functions and the runtime. Every operation whose host semantics can differ is represented by an explicit core intrinsic in the baseline lowering. Host scaffolding does not bypass integer, string, copying, or aliasing rules.

Resolve runtime dependencies transitively and include only the required implementation files and concrete type instances. Missing functions, incompatible contract versions, or missing type mappings are compilation errors. Produce a build manifest listing source profile, compiler version, required contracts, target versions, and generated files.

Generated output is deterministic for the same inputs and lockfiles. Stable symbol mangling includes package identity, receiver, function identity, and specialization IDs, with collision detection.

## 6. Language and execution choices

The normative source contract is [the Goalchemy language specification](specs/language.md). These choices guide implementation:

- Source generics, reflection, unsafe operations, cgo, `go:linkname`, and `goto` are rejected initially.
- Integers, booleans, strings, structs, arrays, slices, maps, pointers to supported storage, functions, methods, and interfaces form the planned type system.
- Floating-point and complex source operations are excluded from the first language profile. A capability may expose encoded data or opaque handles without introducing those source types.
- Pointers to slice or array elements are excluded initially. Pointers to locals and struct fields refer to logical storage locations rather than copied values.
- Explicit close operations own resource cleanup; host GC or destructors do not implement observable source cleanup.
- Errors are values. Panic is a separate control effect, and implementation faults are distinguished from source panics.
- Go value semantics are preserved even on host languages that normally share objects.
- Slice and map sharing is preserved. Copy-on-write is only an optimization after proving that detachment is unobservable.

Map iteration is unspecified to the source. Goalchemy uses insertion order as its reproducible baseline, while tests against native Go compare permitted behavior rather than assuming that Go chooses that order.

Slice growth is fixed for Goalchemy: when an append exceeds capacity, allocate capacity `max(required_length, max(1, 2 * old_capacity))`, after checked representability calculations. Preserve capacity when it already suffices. This is a deliberate choice within Go's implementation-dependent growth behavior. Native-Go differential tests do not assert its incidental growth capacity; Goalchemy's target-to-target tests assert the selected rule exactly.

The equivalence claim therefore covers Go-defined observable behavior plus Goalchemy's explicit choices for implementation-dependent behavior. It does not promise identical map traversal or incidental allocation behavior to every native Go release.

## 7. Cooperative concurrency

Concurrency is a later implementation gate with a concrete execution contract now. Each program instance owns a scheduler, task registry, and runtime state. Only one source task executes at a time.

- `go f(args)` evaluates the callee and arguments in the parent, enqueues a child, and continues the parent.
- A runnable task continues until it blocks, explicitly yields, returns, or panics. Ordinary calls and core operations do not yield.
- Runnable tasks are dispatched FIFO. External completions enqueue resumptions through the scheduler.
- Native threads may perform adapter work, but may not execute generated source code or mutate its heap concurrently.
- Unbuffered channels rendezvous; buffered channels apply backpressure; nil-channel operations block.
- Mutexes have locked state and queues. They are never no-ops. Unlock need not occur in the same task, matching Go's mutex usage model.
- WaitGroup state and invalid transitions are explicit runtime behavior.
- Context cancellation and deadlines become scheduler events. Tests use an injected clock.
- `select` uses shared wait registrations and a single commit operation. Choose uniformly among ready cases using an injectable PRNG; unregister losing cases before resumption.
- An unhandled task panic terminates the program instance. Recovery operates only through the panicking task's deferred-call rules.

The first sequential release rejects concurrency constructs. The concurrency release implements `select` together with channels, avoiding a permanently partial multi-channel contract. Automatic spawn/collect pattern rewriting is excluded; explicit source overrides are available when needed.

Tests cannot assume native Go's scheduler produces Goalchemy's exact task trace. Compare permitted synchronized observations against Go, and use an identical controlled schedule for target-to-target comparisons. Programs relying on preemption or unsynchronized busy waiting are outside the cooperative execution contract.

## 8. Backend choices

| Target | Initial environment | Runtime representation and execution |
| --- | --- | --- |
| Go | Locked reference Go toolchain | Explicit core operations; Go GC; shared frame scheduler |
| TypeScript | Node.js 22 or later; ES2022 output | BigInt for 64-bit arithmetic, explicit byte strings and slice storage; shared frame scheduler |
| Python | Python 3.10 or later (the current baseline; it can be raised later) | Checked/masked integer helpers; ordinary synchronous functions until a frame can suspend |
| Java | Java 21 or later | Explicit unsigned helpers; classes for shared storage; frame scheduler rather than virtual threads for source tasks |
| C# | .NET 8 or later | Explicit copy boundaries and unchecked/checked arithmetic as required; frame scheduler |
| Rust | Edition 2021, stable toolchain | Handle-based traced heap; short host borrows that never span callbacks or suspension |
| C | C17, 64-bit platforms | Boehm-managed allocations, explicit tagged values and panic status, generated resumable frames |

These are minimum environment profiles, not claims that packages have already been installed. Each implemented backend locks exact build-tool and dependency versions in its own build files and CI image.

For Rust, all shared semantic objects use typed handles into a tracing heap. Root discovery is explicit from program globals, active call frames, task frames, and retained host handles. Collection occurs only at documented safepoints with complete root registration. Cycles are collected without changing reference strength. For C, Boehm handles cycles; the runtime ensures semantic references remain visible to the collector and tests retained/interior references.

No backend exposes GC timing to source programs. Memory tests cover repeated operations and cyclic structures. Removing source finalizers does not justify unbounded retention in a long-running compiler output.

## 9. External capabilities and overrides

External modules use the same contract catalog and target manifest mechanism as the core. The initial compiler ships small fake capabilities for bytes, I/O, clock, randomness, and suspending operations so boundaries can be tested without production SDK adapters.

Module signatures may use supported source types, declared records, interfaces, callbacks with declared effects, and opaque handle types. Adapters own conversion to native types. JSON serializers, when eventually provided, receive compiler-generated type descriptors; source reflection remains excluded.

Declare dependencies by stable contract ID. A new capability that uses existing supported types and effects requires declarations, contracts, implementations, and tests. A new calling convention, semantic type, or source construct is a language/compiler extension and cannot be added through an interface file alone.

Overrides are explicit manifest entries keyed by package path, receiver type, function name, expected signature, and source-body hash. Replacements use Goalchemy source, pass normal validation, and appear in the build manifest. An upstream change invalidates the override until reviewed. Tests compare the replacement with the original function wherever equivalent inputs can be exercised.

## 10. Deferred OpenTDF source survey

This work is deferred and is not a compiler milestone exit criterion. The pinned revision and intended method below remain as design notes for a future project phase.

Use `github.com/opentdf/platform/sdk` at tag `sdk/v0.21.0`, resolved to commit `5d0508fb8c58690f91e1a6e5d1bfe3ea7609f721`, as the initial reproducible survey fixture. This is a selected baseline, not a claim that it is the latest SDK release.

Start from TDF creation and loading/decryption roots present at that revision. Include callbacks, interface dispatch candidates, initialization, and helpers needed by those roots. Produce counts by source construct, semantic operation, imported symbol, generic instantiation, concurrency primitive, and proposed external boundary. Report conservative over-approximation separately from confirmed call edges.

Classify every finding as supported, planned language feature, external capability, explicit override, or outside the selected workload. Do not modify upstream source simply to make the report pass.

For this compiler phase, crypto, ZIP, JWT, HTTP, JSON, and platform RPC behavior are external boundaries. KAS endpoint details, RSA versus EC product coverage, DPoP handling, SDK builders, and package publishing do not affect compiler acceptance and are excluded from its release gates. Their future adapter contracts must be based on a separately pinned protocol profile. This resolves those questions at the current scope boundary rather than guessing product behavior.

The survey may reveal unsupported source features. The chosen response is an explicit diagnostic and either a tested override or a separately scoped language extension. The source snapshot is never assumed compatible before the survey runs.

## 11. Differential testing

### Reference programs

Maintain three comparison points: native Go fixtures, lowered Go output, and other target outputs. A lowered-Go mismatch can originate in lowering, its runtime, a contract, or the harness; the shared host language narrows the search but does not prove the runtime correct.

Pure runtime operations and sequential source fixtures compare exact canonical observations where behavior is specified. Nondeterministic operations use controlled inputs, sets of permitted outcomes, or explicit properties. Preserve source-visible error messages when specified or inspected; normalized error kinds alone are insufficient for source code that calls `Error()`.

### Harness protocol

Use one long-running process per target. Reserve stdout for JSON Lines protocol messages and stderr for diagnostics. Requests carry protocol version, request ID, fixture or function ID, typed inputs, and an optional scheduler seed. Replies distinguish returned values, returned errors, source panic, expected blocked state, harness failure, and process failure.

Initially expose declared runtime operations and purpose-built source fixtures. Arbitrary function export is not required. Stateful requests create handles, invoke operations, inspect values, and release fixture state. A fixture can contain several tasks so blocking operations need not block the harness driver itself.

Canonical values use typed decimal strings for integers, hex for bytes and arbitrary-byte strings, explicit tags for nil and empty values, structural type IDs for interfaces, and object handles for identity and aliasing. Maps are serialized as entries sorted by canonical key encoding for transport only. Sorting is never applied to source map execution.

### Test layers

1. Contract validation and generated-file checks.
2. Runtime function and shared-type conformance cases in each implemented target.
3. Source language fixtures comparing native Go, lowered Go, and implemented targets.
4. Compiler integration fixtures covering package loading, imports, overrides, diagnostics, and reproducible output.
5. Stateful fuzzing and regression replay, including aliasing, error paths, and scheduler operations.

Every supported feature needs positive cases, negative cases where rejection is part of its boundary, and interactions with existing features. Important interactions include nested aggregate copies, interface-held typed nil, deferred mutation of named returns, and append through overlapping aliases.

Start with deterministic fixtures and a simple reducer. Add type-driven fuzzing after the harness is useful. Shrinking must preserve type validity, object dependencies, and the mismatch. Save the source or operation sequence, seed, profiles, and all relevant versions with each regression.

Each pull request runs schema validation, generation checks, compiler tests, and the regression suite for every backend that has reached its acceptance gate. Nightly CI spends a fixed time budget per test family; it does not promise a fixed number of expensive cases per function. Use synthetic data for recorded external-call fixtures.

## 12. CLI and build interface

The CLI is `goalchemy`. Its initial commands are:

- `check`: load, type-check, validate the subset, and report diagnostics.
- `compile --target <name> --out <directory>`: emit a complete target build directory and manifest.
- `spec validate`: validate canonical contracts and target mappings.
- `spec generate`: generate target specs, stubs, harness bindings, and API documentation.
- `test`: run selected conformance fixtures through configured targets.

Use a project configuration file for roots, build tags, source profile, mappings, overrides, and target settings. Do not overwrite hand-maintained source files during generation. Generated files carry a marker and are replaced deterministically. Runtime implementations remain hand-maintained unless explicitly designated as generated specializations.

## 13. Milestones and exit criteria

| Milestone | Deliverables | Exit criterion |
| --- | --- | --- |
| 0. Baseline | Language spec, source/toolchain profile, diagnostic categories | Baseline rules and diagnostic classes are documented and the reference toolchain is selected |
| 1. Frontend and contracts | CLI, package loading, subset validator, schemas, semantic spec validator, normalized contract catalog | Valid/invalid fixtures classify correctly; malformed or inconsistent contracts fail clearly |
| 2. Typed IR and lowered Go | Sequential scalar/control-flow IR, IR verifier, core runtime linking, Go emitter and harness | Native and lowered Go agree on the first semantic fixtures; generation is reproducible |
| 3. TypeScript vertical slice | Node backend, matching core runtime, target specs, harness | The same scalar/control-flow fixtures pass in Go and TypeScript, including 64-bit edges |
| 4. Sequential language completion | Aggregates, aliasing, pointers, methods, interfaces, closures, defer/recover, initialization | All documented sequential features pass contract and language suites on both targets |
| 5. First compiler release | Required runtime packaging, source maps, diagnostics, regression corpus, examples | Users can compile documented sequential programs to runnable Go and TypeScript outputs |
| 6. Cooperative execution | Effect propagation, resumable frames, tasks, channels, select, synchronization, cancellation | Controlled scheduler tests match across Go and TypeScript; native-Go comparisons respect permitted outcomes |
| 7. Python | Runtime, emitter, manifests, harness | Passes all released language and runtime contracts |
| 8. Java and C# | Separate emitters and runtimes using the same scheduler contract | Both pass all released contracts and regression fixtures |
| 9. Rust | Tracing heap, emitter, runtime, harness | Semantic suite and cyclic/repeated-allocation memory tests pass |
| 10. C | C17 emitter, Boehm runtime, panic propagation, frame scheduler | Semantic suite passes under address/undefined-behavior checks compatible with the GC; generated library can be called by a small C host |
| 11. Hardening | Broader fuzzing, performance baselines, upgrade workflow | Repeatable builds, bounded test budgets, and versioned compatibility reports for all seven targets |

Complete the narrow Go/TypeScript vertical slice before implementing every advanced feature. Do not build a sophisticated fuzzer or all module adapters before generated programs can run.

Milestone 11 evidence is in [the hardening workflow](docs/hardening.md),
`toolchains.lock`, and the versioned `reports/` snapshots. The bounded
differential campaign covers integer widths, conversions, control flow,
aggregate copies, interfaces, closures, aliasing, recovered panics, and
synchronized channel handoffs. The compatibility run checks contract cases,
source observations, and repeatable generated builds on all seven targets;
the performance run records compile and execution baselines for the same
target set.

## 14. Change control and working rules

The language spec and canonical runtime contracts are the behavior authority. Emitters must not silently establish new semantics. Proposed changes include the motivating example, affected contracts, compatibility effect, and required tests.

A feature advances from planned to supported only after validator rules, IR representation, lowering, runtime dependencies, diagnostics, and conformance fixtures are present for the released target set. Experimental targets may lag, but compilation must report unsupported capabilities honestly.

Semantic contract changes bump their versions. The compiler manifest locks required versions and hashes, and generation checks catch stale target specs. Upstream source and toolchain upgrades are explicit changes with compatibility and regression results. A future OpenTDF survey may add workload-specific before/after reports.

Measure performance after establishing correctness. There is no initial promise of zero-cost runtime wrappers. Optimizations such as intrinsic inlining, specialized copies, or allocation removal require equivalent observable behavior and focused regression coverage.

Implementation details can be polished during milestones. The initial defaults above are resolved choices; changing one is an explicit revision, not an unanswered prerequisite to starting work.

## 15. Reference material

- [Go language specification](https://go.dev/ref/spec): parent-language semantics. The project uses its pinned language/toolchain profile rather than implicitly adopting every future addition.
- [Go types package](https://pkg.go.dev/go/types): type information used by validation and lowering.
- [Go downloads](https://go.dev/dl/): reference toolchain distribution.
- [JSON Schema object validation](https://json-schema.org/understanding-json-schema/reference/object): required and closed fields for contract schemas.
- [Pinned OpenTDF SDK source](https://github.com/opentdf/platform/tree/5d0508fb8c58690f91e1a6e5d1bfe3ea7609f721/sdk): initial survey fixture.
- [Rust Rc documentation](https://doc.rust-lang.org/std/rc/struct.Rc.html): shared ownership and copy-on-write behavior informing the decision to preserve aliases explicitly.

## Remove fixed library value-size caps

Remove the arbitrary 64 MiB value limits in TypeScript, Python, Java and C#,
and C's 128 MiB leaf and 256 MiB aggregate byte budgets. Native allocation
capacity and source descriptor ranges determine representable buffer sizes.
Keep type, fixed-array shape, integer-range and structural traversal validation,
owned snapshots and operation-specific capability contracts.

Acceptance: importing consumers round-trip 100 MiB buffers on the four affected
managed targets, 129 MiB through C, and copy a C value tree containing 270 MiB
of byte leaves. Verify large binary strings and input ownership, retain malformed
value rejection, and run existing importing-library checks on affected targets.
Publish through a reviewed PR with green CI and a signed v0.7.4 release, then
remeasure the blocked OpenTDF SDK cases using released compiler output.

Local acceptance: the four managed targets passed 100 MiB round trips and
65 MiB binary strings under both frames and native pausing. C passed a 129 MiB
round trip and a 270 MiB aggregate copy. Existing library suites passed for
all five changed targets, including both naming modes where supported and
actual Chromium imports. Java/C# emitter checks, spec validation and all 1,121
generated-spec freshness checks passed, as did the installed compiler test.
Hosted CI on the release PR is the remaining merge and release gate.

An SDK build during CI exposed its C# adapter's direct use of the public
`Library.length` helper. Preserve the Java/C# helper for compatibility, validating
only nonnegative native lengths. Large-buffer consumers now exercise this API
at 100 MiB and reject negative lengths in both pause models. Recheck the real
C# SDK build before publishing the corrected PR head.
