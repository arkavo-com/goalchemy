---
title: Python
description: Python target setup, generated output, native library boundaries, and implementation details.
---

## Prerequisites

Python 3.10 or later. Native cryptographic capabilities also need their maintained Python dependencies; follow the generated README for the selected output.

## Compile and run

From a prepared source module containing a `main` package:

```sh
goalchemy compile -target python -out out/python .
cd out/python
python3 main.py
```

Or use `goalchemy run -target python .` to compile and execute in a temporary directory. Add `-gate cooperative` for source tasks or suspending operations.

## Generated output

The Python target emits `main.py`, source-package `pkg_*.py` modules, leaf `_shared.py`, and selected `rt/` package files. Generated programs preserve source integer widths, byte-string behavior, value copies, and error semantics.

Every output includes its own `README.md`, selected runtime files, source mappings, and `goalchemy.manifest.json`. See the [full output reference](/guides/usage/#output) for the inventory and entry behavior.

## Native libraries

Non-`main` packages expose owned value libraries with cancellable synchronous and asynchronous calls. Review [library output](/guides/usage/#libraries) and [stateful library handles](/guides/library-handles/) for initialization, ownership, and retained state. Verify dependency installation and imports in a native consumer.

## Runtime and diagnostics

Byte arrays and slices use `bytearray` storage, with explicit copies to immutable `bytes` for source strings. Generated `.py.lines` sidecars map positions back to Go. Cooperative `main.runHost()` has a calling-thread owner and waits for cleanup before returning.

## Implementation details

[Host operations](/targets/details/python-host-operations/) · [Byte storage](/targets/details/python-byte-storage/)
