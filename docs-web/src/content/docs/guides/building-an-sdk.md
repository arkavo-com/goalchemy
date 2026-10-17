---
title: Building an SDK
description: Compile non-main Go packages into native libraries and design public values, state, and ownership deliberately.
sidebar:
  order: 3
---

Select a non-`main` root package to generate a library. Keep reusable SDK logic in the Go subset, then expose a public boundary appropriate for each target.

## Start with a value API

Create `greeting/greeting.go` in a [prepared source module](/getting-started/installation/#prepare-a-source-module):

```go
package greeting

import "github.com/eugenioenko/goalchemy/std/strings"

func Greet(name string) string {
    return "Hello, " + strings.TrimSpace(name)
}
```

Compile the package:

```sh
goalchemy check ./greeting
goalchemy compile -target typescript -out out/typescript ./greeting
goalchemy compile -target python -out out/python ./greeting
```

Read each output's `README.md` for its import and build instructions. Target public APIs use native representations and have explicit copying, error, and cancellation rules.

## Design the boundary

| Concern | Design choice |
| --- | --- |
| Values | Use supported scalars and value trees; verify structs, arrays, slices, and errors against each target's boundary. |
| Byte strings | Use the boundary's byte representation and explicit lengths. Host text conversion is a separate decision. |
| State | Value-library calls initialize fresh source state. Use explicit library handles for state that must survive calls. |
| Cancellation | Use the target's owned call API for cooperative operations; cancellation completes after required cleanup. |
| Resources | Close handles and release owned results according to the target API. |
| Packaging | Add the native package metadata and dependency locks required by your SDK's delivery environment. |

The compiler warns about some writes to globals in ordinary value libraries (`GCW001`), because a later call starts with fresh state. [Library handles](/guides/library-handles/) document retained state, instances, source streams, and close behavior.

## Read the target boundary

- [Go](/targets/details/go-library-boundary/), [TypeScript](/targets/details/typescript-library-boundary/), [Java](/targets/details/java-library-boundary/), [C#](/targets/details/csharp-library-boundary/), and [Rust](/targets/details/rust-library-boundary/) have dedicated boundary references.
- [Python](/targets/python/) and [C](/targets/c/) are described in the [library output guide](/guides/usage/#libraries).
- [Swift's setup and library API](/targets/details/swift-target/#swiftpm-library-api) document the experimental SwiftPM product.

Generated compiler output is one part of SDK delivery. Test imports, dependency installation, cancellation, callbacks, and public errors from an actual native consumer in each ecosystem.
