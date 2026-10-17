---
title: Compiler architecture
description: Follow package loading, subset checking, typed lowering, target emission, runtime linking, and conformance.
sidebar:
  order: 1
---

Goalchemy centralizes source semantics before target emission. Each backend consumes the same typed representation and implements the required runtime contracts.

## Compilation pipeline

1. **Load packages.** `internal/frontend` uses Go package loading and typing under the pinned source profile.
2. **Check the gate.** `internal/subset` rejects unsupported source with stable diagnostics and remedies.
3. **Lower and verify.** `internal/lower` constructs `internal/ir`, making sequencing, value copies, storage, and runtime operations explicit.
4. **Emit native source.** `internal/emit` contains the target emitters; `internal/naming` manages legal and deterministic identifiers.
5. **Link the runtime.** `internal/link` includes only required operation files and shared representations, then records inventories, contracts, and hashes.
6. **Map diagnostics.** `internal/sourcemap` connects generated positions with source locations.

`internal/driver` coordinates compilation. The public CLI is in `cmd/goalchemy`. The binary embeds the catalog and runtime assets so compilation does not require a source checkout.

## Runtime architecture

| Layer | Source of behavior |
| --- | --- |
| Source semantics | [Language specification](/language/specification/), checked features, and typed IR. |
| Canonical contracts | YAML under `specs/types/` and `specs/runtime/`, validated against schemas. |
| Target mappings | `targets/<target>/target.yaml`, representation code, operation implementations, and harnesses. |
| Shared source packages | `std/`, compiled with the user program. |
| Native capabilities | `lib/` declarations and native implementations per target. |

Contracts refine operations without replacing the language specification. An emitter must not silently adopt different host semantics.

## Verification layers

Native Go acts as an oracle for accepted source fixtures. Runtime harnesses check operation contracts independently. Corpus regressions retain reduced failures; integration tests exercise native public consumers, dependency delivery, and host lifecycle behavior.

See [testing and hardening](/internals/testing/) and [compiler CI](/internals/ci/) for commands and coverage. The [adding a target guide](/internals/adding-a-target/) provides the implementation and acceptance sequence.

## Inspect the intermediate representation

```sh
go run ./cmd/goalchemy compile -target ir ./examples/calc
```

IR output is written to standard output and does not require `-out`. The [design plan](https://github.com/eugenioenko/goalchemy/blob/main/plan.md) records the broader compiler design and roadmap; historical phase descriptions are not a replacement for the current feature manifest.
