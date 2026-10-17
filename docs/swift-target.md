# Swift target

Swift is an experimental Goalchemy target. It emits native Swift code and a
small C module; generated programs do not invoke Go. The development baseline
is Swift 6.4.0 on Linux x86_64, using Swift 5 language mode. macOS, iOS and other
Apple SDK integration have not yet been verified. Adding Swift to Goalchemy
does not by itself deliver a Swift OpenTDF SDK in the separate SDK repository.

## Setup and output

Install Swift, a C compiler, pkg-config, OpenSSL 3, zlib and libcurl development
files. On Ubuntu the native packages are `build-essential`, `pkg-config`,
`libssl-dev`, `zlib1g-dev` and `libcurl4-openssl-dev`; the Swift toolchain also
needs the prerequisites listed in the official Swift Linux installation guide.
The repository's hash-verified toolchain fetcher installs the pinned compiler:

```sh
bash scripts/fetch-toolchains.sh swift
export PATH="$PWD/.toolchains/swift-6.4.0/usr/bin:$PATH"
go run ./cmd/goalchemy run -target swift ./examples/calc
go run ./cmd/goalchemy compile -target swift -out out/swift-calc ./examples/calc
sh out/swift-calc/run.sh
```

An executable output directory contains `main.swift`, `run.sh`, `rt/types/`,
selected `rt/runtime/` operation files, the C module in `rt/native/`, the
generated-source line map, `LICENSE`, `README.md` and the standard manifest.
`run.sh` obtains native include/link flags from pkg-config, compiles the C
module, builds the Swift program and runs it. `SWIFTC` and `CC` can select
alternate compiler binaries. Native support is currently included in every
Swift output, even when the source does not call crypto or HTTP.

## Source semantics

The frontend's typed IR determines source behavior; Swift's implicit integer,
string and collection semantics do not replace it.

| Source value | Swift representation |
| --- | --- |
| Integer | Explicit width and signedness over UInt64 bits; wrapping arithmetic and signed division toward zero |
| `float32`, `float64` | Float/Double precision, explicit binary32 rounding, signed zero and IEEE exceptional values |
| String | Immutable `[UInt8]`, including NUL and invalid UTF-8 |
| Byte array or slice | Native `[UInt8]` backing with source headers for slices, bulk append/copy and public byte conversion; nil, empty, length, capacity and shared aliases preserved |
| Other array or struct | Stable cells and recursive explicit value copies |
| Map | Source equality and type descriptors, including aggregate keys and nonreflexive NaN |
| Pointer | Stable cell identity; assignments update cells in place |
| Interface | Dynamic source type plus copied payload and method descriptors |
| Function or task | Captured cells and resumable frames driven by the cooperative scheduler; under `--pause-model=native`, `async throws` functions on the owner's executor |

ARC releases ordinary acyclic native objects. A weak allocation registry and
explicit tracing pass break unreachable source cycles at scheduler safepoints.
Roots include source globals, live frames, deferred captures, channels, pending
host operations and retained public call inputs. Under the native pause model
each async function registers its local cells with its task, and the compiler
never holds a computed value in a Swift temporary across an `await`.
`GOALCHEMY_GC_THRESHOLD=<n>` collects after fewer allocations; `1` collects at
every safepoint. Field pointers and bound
method receivers retain their backing objects. This preserves source ownership
even when the host representation introduces a closure or reference cycle.

Bulk byte operations retain native source views instead of expanding each byte
into a runtime value. Swift copy-on-write preserves source snapshots for
overlapping operations and caller-owned inputs; source slice aliases continue
to observe updates through their shared backing object. Other element types
retain recursive Go value copying.

Readability and `--compact-names` affect private identifiers, while the public
Swift names retain source identifiers. Swift keywords are escaped with
backticks. Unsupported exported shapes and reserved public names that would
collide with the runtime or Swift primitives fail with `GCE007`.

## Native capabilities and ownership

Crypto uses OpenSSL 3 through `GoalchemyNative`: AES-GCM, RSA-OAEP, RSA and
P-256 signing, key generation/import/export, ECDH, SHA-256, HMAC, HKDF and
secure randomness. Keys retain native leases during operations. Closing a key
waits for leases and rejects subsequent uses through any alias. IEEE CRC32
uses zlib's native `crc32` API rather than a transpiled per-byte loop.

HTTP uses libcurl without redirects. It checks request/response bounds,
verifies TLS certificates, owns request data, decodes compressed bodies and
keeps ordered duplicate response values. The C shim reads raw response
headers before canonicalizing and sorting names; this avoids losing distinct
`Set-Cookie` values.

Native workers publish completions to a mailbox. The scheduler owner alone
updates source state, resumes tasks and applies context cancellation or
deadlines. Retirement waits for native cleanup acknowledgement. Host-driven
library calls use monotonic time; deterministic source fixtures retain virtual
time. Callback providers run outside the source owner and return their result
through the same completion boundary.

## SwiftPM library API

Compile a non-`main` source package as usual:

```sh
go run ./cmd/goalchemy compile -gate cooperative -target swift -out out/my-sdk ./path/to/source
sh out/my-sdk/build.sh
```

The generated `Package.swift` exports the library product/module
`GoalchemyGenerated`. Add that directory as a package dependency, then depend
on its product from your consumer target. System-library targets discover
OpenSSL, zlib and libcurl through pkg-config.

Public functions retain their Go names. Source context parameters become
`CallOptions`, and the source error result becomes a thrown `GoalchemyFailure`.
For example, a source `Echo(ctx, v) (Value, error)` can be called as:

```swift
import GoalchemyGenerated

let operation = Echo(input)
let synchronous = try operation.wait()
let asynchronous = try await Echo(input).value()

let token = CancellationToken()
let pending = Echo(input, CallOptions(cancellation: token,
                                     timeoutNanoseconds: 1_000_000_000))
token.cancel()
let result = try pending.wait()
```

Calls are lazy and serialized. Each invocation resets and initializes the
generated source globals before running the exported function. Conversion
copies and retains inputs when an operation is created; outputs and source
error fields detach from source storage. `wait()` and `value()` observe the
same operation result. Cancellation is cooperative, using the supplied token
or the operation's `cancel()` method. Calls on [handles](library-handles.md)
use their instance's globals instead. See [package variables in libraries](usage.md#package-variables-in-libraries).

Public structs retain source field names and provide default initializers.
Integers use exact native widths, floats use Float/Double, arrays use Swift
arrays with source fixed-length validation, and slices use optional arrays to
distinguish nil from present empty values. Nested slices preserve the same
distinction. `GoString` stores arbitrary bytes; `GoString("text")` and string
literals encode UTF-8, while `GoString(bytes: ...)` preserves binary strings.
`GoalchemyKey` exposes native key ownership and `close()`. Key aliases preserve
the source pointer identity and share close state. A retained wrapper keeps its
source cell rooted across later calls and tracing collections. Submitted calls
retain the key inputs they need; callers may release their wrappers before
waiting for completion.

The public boundary accepts booleans, exact-width integers, floats, strings,
fixed arrays, slices, structs with exported nonembedded fields, and opaque
crypto keys, including supported nested values. It rejects maps, channels,
function values, ordinary pointers, arbitrary interfaces, variadic exports,
and structs with private or embedded fields with `GCE007`. These restrictions
apply to exported values; the corresponding supported source constructs remain
available inside generated programs.

`CallOptions.callbacks` maps a capability name to a `CallbackProvider`. A
provider receives an owned byte request and cancellation token, invokes its
completion once, and may return a cancellation hook. Providers must eventually
complete after cancellation so the owner can acknowledge cleanup.

Providers and cancellation hooks must not synchronously wait on another
generated call. Such reentry fails with a `GoalchemyFailure` whose kind is
`host`. Hooks run on host workers so they can wait for their own native cleanup
without blocking source scheduling. A canceled, failed or panicking call
retires pending host work and waits for cleanup acknowledgement before
publishing its result or error.

## Logging

`std/log/slog` records reach the host through the module's
`setLogHandler(_:level:)`, which receives `GoalchemyLogRecord` values. `nil`
restores standard error at warn and above. See [the logging section of the usage guide](usage.md).

## Verification

Swift joins target discovery, the feature matrix, corpus/examples, both naming
modes, float consumers, checksum checks and runtime conformance. Run focused
checks with the actual toolchain and native prerequisites installed:

```sh
go test -v -timeout 15m ./tests/contracts -run '^TestSwift' -count=1
go test -v -timeout 15m ./tests/contracts -run '^TestTargetConformance$/swift$' -count=1
go test -v -timeout 15m ./tests/integration -run '^TestGeneratedFloatLibraries/(readable|compact)/swift$' -count=1
go test -v -timeout 15m ./tests/integration -run '^TestSwift(PureLibraryOutput|LibraryHostLifecycle)$' -count=1
python3 scripts/ci-suite.py language --target swift
```

The native acceptance checks use Go-generated RSA/P-256 keys and ciphertext,
then verify Swift-produced signatures, wrapped keys and derived bytes with Go.
Real local HTTP servers check duplicate headers, gzip, redirect rejection,
limits, cancellation, deadlines and untrusted TLS. The float consumer imports
the generated SwiftPM product in both naming modes and checks IEEE values,
async suspension, input/result ownership, nil/empty slices and typed errors.
Runtime contract cases use the Swift harness generated from the canonical
catalog, with a real native build rather than a Go-backed oracle implementation.
