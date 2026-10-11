# Diagnostic categories

Goalchemy diagnostics have stable codes, severity, source location where available, symbol, feature, message, and a one-line remedy. Human and JSON output carry the same information.

| Prefix | Category | Meaning |
| --- | --- | --- |
| `GCL` | Loading | Package selection, parsing, typing, or source-profile failure. |
| `GCS` | Source subset | Go code is valid but exceeds the currently implemented language gate. |
| `GCC` | Contracts | A canonical or target contract is malformed or inconsistent. |
| `GCI` | Intermediate representation | Lowering or IR verification failed. |
| `GCE` | Emission | A target lacks a required operation, type representation, or runtime dependency. |
| `GCT` | Conformance | A generated program or runtime operation differs from its contract. |
| `GCW` | Warning | Code compiles and runs but likely does not do what it appears to; the build continues. |

The initial checker reserves `GCL001`–`GCL004` and `GCS001`–`GCS010`. Later phases allocate codes from the remaining categories when their corresponding checks exist. A diagnostic code's meaning must not change silently between releases.

The source baseline is Go 1.25 on Linux amd64 with cgo disabled, checked using Go 1.27.1. The repository's `go.mod` selects the reference toolchain. Source build tags are explicit checker inputs.
