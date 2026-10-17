---
title: "C#"
description: C# target setup, generated output, native library boundaries, and implementation details.
---

## Prerequisites

.NET SDK 8. `DOTNET_ROOT` can select the SDK used by the runner. IEEE CRC32 adds the pinned first-party `System.IO.Hashing` dependency.

## Compile and run

From a prepared source module containing a `main` package:

```sh
goalchemy compile -target csharp -out out/csharp .
cd out/csharp
sh run.sh
```

Or use `goalchemy run -target csharp .` to compile and execute in a temporary directory. Add `-gate cooperative` for source tasks or suspending operations.

## Generated output

The C# target emits `Main.cs`, source-package `pkg_*.cs` files, `shared.cs`, selected `rt/` files, `main.csproj`, and `run.sh`. Source files contribute to one native partial `GoProgram`.

Every output includes its own `README.md`, selected runtime files, source mappings, and `goalchemy.manifest.json`. See the [full output reference](/guides/usage/#output) for the inventory and entry behavior.

## Native libraries

Non-`main` packages emit .NET 8 class libraries with owned public values and cancellable asynchronous operations. Read the [C# library boundary](/targets/details/csharp-library-boundary/) before choosing exported structs, bytes, contexts, and error handling.

## Runtime and diagnostics

Native `byte[]` storage preserves slice headers, shared backing, and unsigned reads. `.cs.lines` files retain source positions. Cooperative `GoProgram.runHost()` returns a Task and uses a dedicated monotonic owner driver with explicit recursion protection.

## Implementation details

[Library boundary](/targets/details/csharp-library-boundary/) · [Host operations](/targets/details/csharp-host-operations/) · [Byte storage](/targets/details/csharp-byte-storage/)
