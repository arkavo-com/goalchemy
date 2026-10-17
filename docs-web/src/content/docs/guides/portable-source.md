---
title: Writing portable source
description: Choose supported imports and preserve value, byte, numeric, and error behavior across targets.
sidebar:
  order: 2
---

Start with code that passes the standard Go toolchain, then run `goalchemy check` early. The subset checker is the authority on whether that code can be lowered.

## Use Goalchemy packages

Import pure-logic packages through `std/` and host capabilities through `lib/`:

```go
package greeting

import "github.com/eugenioenko/goalchemy/std/strings"

func Normalize(name string) string {
    return strings.ToLower(strings.TrimSpace(name))
}
```

Keep the package's ordinary name (`strings`) at call sites. The `std/` implementation is source translated with your application. A `lib/` wrapper reaches a native target implementation governed by a runtime contract.

An arbitrary standard-library or third-party import is not automatically portable. Its reachable implementation must pass the source gate or have an explicit supported mapping. Consult the [runtime package index](/runtime/overview/#packages).

## Preserve source semantics

- `int` and `uint` are 64-bit on every target. Avoid assumptions about a host's native integer type.
- Strings contain bytes, including invalid UTF-8 and NUL. Convert deliberately at host text boundaries.
- Structs and arrays copy by value. Slice headers copy while backing storage can remain shared.
- Errors are ordinary values; `defer`, `panic`, and `recover` retain source behavior.
- Go leaves some evaluation and iteration choices open. Do not depend on map iteration order or incidental native Go slice growth.

The [language specification](/language/specification/) documents the precise choices. Numeric edge cases are covered in [floating-point semantics](/language/floating-point/).

## Keep boundaries explicit

Use capability packages for crypto, HTTP, wall clocks, and other host access. A pure parsing function belongs in source; a platform operation needs a native implementation and a contract. Read each package's target availability and dependency notes.

For exported libraries, design public values against the [target boundary](/guides/building-an-sdk/) you will ship. A type that is legal inside a program may not be legal in a public library API.

## Check before building

```sh
go test ./...
goalchemy check ./path/to/package
goalchemy compile -target typescript -out out/typescript ./path/to/package
```

Use native Go tests for fast source iteration, then exercise generated outputs on every supported delivery target. The compiler project's own conformance suite does not replace your SDK's tests.
