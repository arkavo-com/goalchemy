---
title: Language overview
description: Understand the Go source profile, feature gates, shared semantics, and the limits of accepted source.
sidebar:
  order: 1
---

Goalchemy accepts ordinary Go syntax under a restricted semantic profile. The source baseline is Go 1.25 on Linux amd64 with cgo disabled; the reference compiler is Go 1.27.1. A newer source language requirement is rejected.

## Two gates

| Gate | Adds |
| --- | --- |
| `sequential` | Packages, initialization, functions, methods, closures, integers, floats, strings, structs, arrays, slices, maps, supported pointers, interfaces, control flow, errors, and deferred calls. |
| `cooperative` | Source tasks, channels, `select`, synchronization, cancellation, and resumable execution. |

Select the gate with `-gate` or project configuration. Check the [feature matrix](/language/features/) for tested coverage. The specification describes semantic intent; the manifest and fixtures establish implemented support.

## Semantics belong to the source

Emitters preserve a typed intermediate representation rather than relying on host-language defaults. Examples include:

| Source behavior | Consequence across targets |
| --- | --- |
| Fixed-width integer operations | Source overflow and conversion rules survive JavaScript numbers and C arithmetic differences. |
| Byte-based strings | Length, indexing, slicing, and invalid UTF-8 preserve Go observations. |
| Struct and array copies | Assignment copies values even when a host would share objects. |
| Slice aliasing | Aliases share backing storage while retaining separate headers. |
| Dynamic interfaces | Type identity, method sets, and typed nil values are preserved. |
| Panic and defer | Source unwinding is separate from a host implementation failure. |

Read the [language specification](/language/specification/) for the exact rules and [floating-point semantics](/language/floating-point/) for IEEE behavior.

## Unsupported constructs fail early

Generics, complex numbers, source reflection, `unsafe`, cgo, arbitrary `goto`, and slice/array element pointers are outside the accepted profile. Imported code is checked too. See [limitations](/language/limitations/) before designing a public API.

Run `goalchemy check -json .` for structured diagnostics. Diagnostic codes retain their category and meaning across text and JSON forms; see [diagnostics](/reference/diagnostics/).
