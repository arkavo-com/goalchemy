# Using Goalchemy

Goalchemy compiles programs written in a restricted subset of Go into other languages. Source files are ordinary `.go` files: they build and run with the Go toolchain, and Goalchemy accepts the subset described in [the language specification](../specs/language.md). Targets are lowered Go, TypeScript for Node.js, Python 3.10+, Java 21, C# for .NET 8, Rust (edition 2021), C17, and experimental Swift 6.4 on Linux x86_64. Run `goalchemy features` for the current feature matrix.

## Install

```sh
go build -o bin/goalchemy ./cmd/goalchemy
```

The repository's `go.mod` selects the Go 1.27.1 reference toolchain. Source programs must declare `go 1.25` or earlier. TypeScript output needs Node.js 22.6 or later, because it runs `.ts` files directly through Node's type stripping.

## Commands

| Command | Purpose |
| --- | --- |
| `goalchemy check [-gate g] [-tags t] [-json] [packages]` | Load, type-check, and validate against the language gate. |
| `goalchemy compile -target <go\|typescript\|python\|java\|csharp\|rust\|c\|swift\|ir> -out <dir> [packages]` | Write a complete, runnable target directory. |
| `goalchemy build [-config goalchemy.yaml]` | Compile every target listed in a project configuration. |
| `goalchemy run -target <name> [packages]` | Compile to a temporary directory and run the program. |
| `goalchemy features` | Print supported features and targets. |
| `goalchemy spec validate` / `spec generate [-check]` / `test` | Maintain and verify the runtime contract catalog. |

## Project configuration

`goalchemy build` reads `goalchemy.yaml` from the current directory:

```yaml
schema_version: 1
packages: [./cmd/app]      # package patterns, relative to this file
tags: [prod]               # optional build tags
gate: sequential           # sequential (default) or cooperative
compact_names: false        # readable private identifiers (default)
pause_model: frames         # frames (default) or native (Go, TypeScript, Swift, C# and Rust, experimental)
line_directives: true       # map generated code back to the source (default)
targets:
  go: {out: out/go}
  typescript: {out: out/ts}
  python: {out: out/py}
  java: {out: out/java}
  csharp: {out: out/cs}
  rust: {out: out/rs}
  c: {out: out/c}
  swift: {out: out/swift}
```

The file uses the same restricted YAML as the contract catalog: no anchors, aliases, or floats, and no unknown keys.

`compile`, `run`, and `build` accept `--compact-names`. `compile` and `run`
default to readable names and do not load `goalchemy.yaml`. `build` uses the
configuration's `compact_names` setting unless the flag is explicitly present:
`--compact-names` selects compact names and `--compact-names=false` selects
readable names.

```sh
goalchemy compile -target rust -out out/rust --compact-names .
goalchemy run -target python --compact-names .
goalchemy build --compact-names=false
```

`compile`, `run`, and `build` also accept `--pause-model=frames|native`
(`build` overrides `pause_model`). `frames` emits suspending functions as
resumable frames. `native` is implemented by the Go, TypeScript, Swift, C# and Rust
targets.
On Go it emits them as ordinary Go functions that take the current task and
block at pause points; a turn-passing scheduler keeps exactly one task
goroutine running, and a task that blocks hands the turn directly to the next
task in the same FIFO order as frames. On TypeScript it emits `async`
functions that take the current task as their last parameter and `await`
their suspending callees; a primitive that blocks the task awaits a promise
that only Goalchemy's scheduler settles, when it chooses that task next, so
the JavaScript event loop never decides the order. On Swift it emits
`async throws` functions that take the task first; each task's body runs as a
Swift task on its owner's own executor, on the owner thread, and only
Goalchemy's scheduler resumes a blocked task, so Swift's cooperative pool
never runs source code. Swift native output needs task executors: on Apple
platforms, macOS 15, iOS 18, tvOS 18, watchOS 11 or visionOS 2 (the generated
package declares these minimums); frames output has no such requirement. On
C# it emits `async` methods that take the task first and return the
runtime's own `Async`/`Async<T>` types, whose builders never use the .NET
thread pool, `SynchronizationContext` or `TaskScheduler`: a blocked task's
continuation runs only when Goalchemy's scheduler chooses that task next, on
the owner thread. On Rust, pausing functions become `async fn` with boxed
`.await` calls. The existing FIFO scheduler polls their futures on the owner
thread; wakers never dispatch source code. Primitive calls that complete
immediately do not await. Per-task roots retain source values across awaits,
and frame adapters preserve function values, defers and library exports.
Rust keeps frames as its default. See [Rust native pausing](rust-native-pausing.md)
for execution limits and validation, and issue #92 for the overall redesign.

`compile`, `run`, and `build` accept `--line-directives` (default on; `build`
overrides `line_directives`). With `--line-directives=false`, generated code
carries no source annotations: Go output has no `/*line*/` comments,
TypeScript output has no source map, and the other targets write no `.lines`
tables. Runtime positions and compiler diagnostics then refer to the
generated files instead of the Go source.

```sh
goalchemy compile -target go -out out/go --line-directives=false .
```

## Generated control flow

Every target prints structured control flow rebuilt from the IR's basic
blocks: `if`/`else`, loops with `while` conditions, labelled `break` and
`continue`, and short-circuit conditions. Targets without labelled blocks use
`goto` (Go, C, C#) or a small exit variable (Python). Single-use temporaries
are folded into the expression that uses them when that keeps Go's evaluation
order. Two cases keep a dispatch over block numbers: a resumable frame
(`--pause-model=frames`) switches on the block its task resumes at, so a loop
that pauses runs as a dispatch, and an irreducible block graph (not produced
from Go source) is dispatched rather than structured. Swift functions that
cannot pause, defer or recover are plain Swift functions; the others are
frames.

## Generated names

Generated private names are readable by default for Go, TypeScript, Python,
Java, C#, Rust, C, and Swift. Source package, type, function, global, local, and field
names supply identifier stems. Category prefixes and deterministic numeric
suffixes distinguish reserved words, shadowed locals, and repeated names across
packages. Unicode characters are encoded in ASCII as `_u` followed by their
hexadecimal code point and `_`.

For example, an internal function for `main.Calculate` may be named
`fn_main_Calculate_2`; a source local `count` may be `v_count_7`. Targets use
their own legal separators and type prefixes. A named Go struct contributes its
package/type stem to the generated representation; when several named types
share an underlying representation, the lexicographically smallest source name
is chosen deterministically. Anonymous aggregates use shape names such as
`struct`, `array`, or `map`. Unnamed parameters, results, captures, and
temporaries use `param`, `result`, `capture`, and `temp` stems.

`--compact-names` replaces compiler-private names with shorter identifiers
derived from the same IR identities. Public library function/type/field names,
capability symbols, runtime type strings, and serialized identities retain
their established contracts in both modes. Native Go embedding and field rules
also remain intact. Go boundary values from dependency packages receive stable
exported `Source_<qualified-type>_<ID>` aliases, including values nested in
fields and collections.

Public façade names cannot occupy names reserved by their target's generated
API. These collisions produce a library-boundary diagnostic in both modes,
rather than invalid target code. For example, Rust rejects public structs named
`V`, `LibraryError`, or `Vec`, and exported functions named `Some`, `None`, `Ok`,
or `Err`; Python rejects exported functions named `None`, `True`, or `False`.
Swift instead renames a colliding public struct or handle with a trailing
underscore (`Error` becomes `Error_`), since Swift and Foundation reserve
common type names such as `Error`, `Data` and `Task`. These names remain
usable internally when the source language permits them.

Readable names help inspect generated source; they do not reconstruct Go's
original control flow or replace source maps. Some targets still store values
in runtime frames or numeric aggregate slots and identify them through named
helpers or slot constants. Runtime support has its own naming conventions, and
IR dumps keep their diagnostic identifiers. Internal names are not a stable
API across compiler versions. Neither naming mode promises a performance
improvement.

## Initialization guidance

Prefer explicit constructors or setup functions for configuration, I/O, key
generation and resource acquisition. They make dependencies, errors and cleanup
visible to callers. Avoid hiding this work in `init()` or in package-level
variables initialized by calls to setup functions.

Small deterministic initialization remains supported. Goalchemy preserves Go's
package-variable dependency ordering and `init()` functions in its central
initialization routine. Splitting generated code into files does not remove
those effects.

In a library, package initialization runs once per call of a function without
handles and once per handle instance, rather than once at process startup, so
an expensive initializer or externally visible side effect repeats. See
[package variables in libraries](#package-variables-in-libraries); the legacy
sequential C scalar/string API uses an explicit `goalchemy_init()` call.

## Output

Each target directory contains the generated program, the runtime files it needs (one file per runtime function, plus shared representation files), a `README.md`, and `goalchemy.manifest.json`. The manifest records the source profile, compiler version, contract IDs, versions, and hashes, and every runtime and generated file. Output is deterministic for the same inputs.

Program declarations are grouped by their original Go package. The manifest's
`source_packages` records each included package's import path, dependencies and
native source files. Shared representations and the central initializer remain
separate from package-owned declarations. Native public entry points and library
imports stay the same; this does not publish a separate native library for every
Go package. Existing identifier prefixes and compact-name behavior are retained.

Each source file has its own diagnostic map where source positions are available.
When an output directory is reused successfully, Goalchemy removes obsolete files
listed by its previous generated/runtime inventory and preserves unlisted caller
files. Failed emission does not clean that inventory. IR-only output does not use
native output inventories.

- **Go**: `main.go`, `shared.go`, `pkg_*.go`, `go.mod`, `rt/`, all in the existing native package. Run with `go run .`. Line directives map positions back to the Goalchemy source.
- **TypeScript**: native `pkg_*.ts` ESM modules and leaf `shared.ts`; sequential output has `main.ts`, while cooperative output adds portable `program.ts` and `host.ts`, with `main.ts` as its Node wrapper. Source files have individual `.ts.map` files. Both include `rt/` and `package.json`. Run with `node main.ts`, and add `--enable-source-maps` for source positions in stack traces. Cooperative `host.ts` exports Promise-based `runHost(host)` with real monotonic time and an explicit portable output/failure adapter; see [the bounded lifecycle and executable-global limits](typescript-host-operations.md).
- **Python**: `main.py`, native `pkg_*.py` modules, leaf `_shared.py`, and `rt/` (a package). Individual `.py.lines` sidecars map generated lines to Goalchemy source positions. Run with `python3 main.py`; Python 3.10 or later is required. Cooperative output exposes synchronous `main.runHost()` with monotonic time, a calling-thread owner and cleanup-before-return; see [its lifecycle and recursion limits](python-host-operations.md).
- **Java**: `Main.java`, source-package `pkg_*.java` holders, leaf `_GoalchemySupport.java`, `rt/`, `run.sh`, and per-file `.java.lines` sidecars (generated lines to source positions). Run with `sh run.sh`, which compiles the native source inventory with `javac` and runs on a Java 21 or later JDK (`JAVA_HOME` is honored). Cooperative output also exposes `Main.runHost()` for an explicit serialized monotonic executable drive that returns after cleanup; see [its lifecycle and executable-global limits](java-host-operations.md).
- **C#**: `Main.cs`, source-package `pkg_*.cs` files and `shared.cs` declaring one native partial `GoProgram`, `rt/`, `main.csproj`, `run.sh`, and per-file `.cs.lines` sidecars. Run with `sh run.sh`, which compiles the generated/runtime inventory with the .NET 8 SDK's C# compiler (`DOTNET_ROOT` is honored); `dotnet run` also works. The project includes additional root source adapters. Cooperative output exposes Task-returning `GoProgram.runHost()` using a dedicated monotonic owner driver and serialized executable globals; see [its lifecycle and managed-recursion limits](csharp-host-operations.md).
- **Rust**: `src/main.rs` (or `src/lib.rs` for libraries), native `src/pkg_*.rs` modules, `src/shared.rs`, `src/rt/`, `Cargo.toml`, `run.sh`, and per-file `.rs.lines` sidecars. Explicit module paths allow SDK adapters to rename the generated entry file. Run std-only output with `sh run.sh` (plain `rustc`) or `cargo run --release`; native capability output builds with Cargo and maintained dependencies. Cargo compiles explicit native targets; the SDK package helper supplies its full dependency lock. Cooperative output also exposes `run_host() -> Result<(), HostError>`, using a serialized dedicated monotonic owner and cleanup-before-return; see [Rust lifecycle and stack limits](rust-host-operations.md). Values live in a traced heap collected at safepoints; set `GOALCHEMY_HEAP_STATS=1` to print heap statistics at exit and `GOALCHEMY_GC_THRESHOLD=<n>` to collect more often.
- **C**: `main.c`, package-owned `pkg_*.c` translation units, `shared.c`, declaration-only `goalchemy_internal.h`, `rt/` (`gx.h` and one `.c` file per runtime function), `run.sh`, and per-file `.c.lines` sidecars. The scripts compile the explicit generated/runtime inventory as separate native units. Run with `sh run.sh`, which builds with `cc -std=c17` and links the Boehm-Demers-Weiser collector (bdwgc 8.x with threads): `GOALCHEMY_BDWGC` may name an install prefix, otherwise `pkg-config bdw-gc` or `-lgc` is used. Programs that use `lib/crypto` or `lib/http` also link OpenSSL (`-lssl -lcrypto`) and, for HTTP, libcurl through `pkg-config libcurl`. `CC`, `CFLAGS` and `LDLIBS` are honored, so `CC=clang CFLAGS='-fsanitize=address,undefined'` builds a sanitized program.
- **Swift**: `main.swift` (or `Generated.swift` for libraries), package-owned `pkg_*.swift` files, `shared.swift` with the canonical type table, `rt/`, `run.sh`, `LICENSE`, and per-file `.swift.lines` sidecars. Native scripts and SwiftPM explicitly list sources within one native module. Run with `sh run.sh`, which builds a native C module and Swift executable. Requires Swift 6.4, OpenSSL 3, zlib, libcurl and pkg-config even for pure-logic programs. Linux x86_64 is the verified baseline; see [Swift setup, semantics and public libraries](swift-target.md).

### Libraries

Non-`main` packages can emit value libraries for Go, TypeScript, Java, C#, Python, Rust, C and Swift:

- [Go libraries](go-library-boundary.md) expose copied values and cancellable, serialized calls.
- [TypeScript libraries](typescript-library-boundary.md) emit portable JavaScript and declarations for Node and browsers.
- [Java libraries](java-library-boundary.md) emit `Generated.java`, `build.sh` and a named-package JAR with byte arrays, typed values and cancellable asynchronous operations. Production crypto requires the pinned BC 1.86 dependency alongside JDK 21.
- [C# libraries](csharp-library-boundary.md) emit .NET 8 class libraries with owned values and cancellable asynchronous operations. Crypto and HTTP use .NET built-ins; IEEE CRC32 adds the official Microsoft `System.IO.Hashing` 8.0.0 NuGet package.
- [Swift libraries](swift-target.md) emit a `GoalchemyGenerated` SwiftPM product with typed values, lossless Go strings, and cancellable `wait()`/async `value()` calls. All current Swift packages need OpenSSL 3, zlib and libcurl development files.

Python libraries emit an importable generated module with owned byte/value
boundaries and cancellable sync/async operations. Production crypto uses
maintained cryptography; HTTP uses the standard library. See the adjacent
[Python TDF3 package and boundary evidence](../../sdk/docs/generated-python-library.md).
[Rust libraries](rust-library-boundary.md) emit owned typed Cargo Result/Future APIs
with serialized source owners and cleanup-before-publication. Native crypto/HTTP
use maintained locked crates; see [Rust TDF3 delivery](../../sdk/docs/generated-rust-library.md).
Unsupported public value shapes on supported value-library targets fail with `GCE007`.
Libraries can also return stateful [handles](library-handles.md): pointers
to exported structs with methods.

#### Package variables in libraries

Package variables in a library do not live for the process, as they do in a Go
program. Each call runs against one set of them:

- A call of an exported function that neither receives nor returns a
  [handle](library-handles.md) or a source stream starts from fresh package
  variables, runs package initialization, and discards the variables when it
  returns. Nothing it writes to a package variable is seen by a later call.
- The call that creates a handle starts an instance with fresh package
  variables. The instance keeps them: each later call on its handles loads
  them, runs, and saves them, until the last handle is released. Instances
  never share package variables.

Calls are serialized across the whole library, so this is what makes package
variables safe to use from several host threads without locks. State that must
outlive a call, such as a cache or a connection, belongs in a handle: a struct
returned by pointer from a constructor, or a package variable used by its
methods.

The compiler warns (`GCW001`) when a call of an export without handles writes a
package variable after initialization: it assigns it, or writes through an
index, field or pointer, or calls a pointer method on it. Reads, writes during
package initialization, and writes from handle methods and constructors are not
reported, nor are executables.

```go
var cache = map[string][]byte{}

func Fetch(url string) []byte {
	if v, ok := cache[url]; ok {
		return v // never hit: each Fetch call starts with an empty cache
	}
	body := download(url)
	cache[url] = body // warning GCW001
	return body
}
```

C aggregate or suspending exports emit `goalchemy.h`, `main.c`, `rt/` and
`build.sh`, producing `libgoalchemy.a`. The owned `gxc_value` API recursively
copies inputs and detaches outputs/errors, preserving explicit byte lengths and
exact integer bits. Calls serialize on a registered source owner with fresh
initialization; consumers need no collector initialization. Native callbacks
use owned data and cancellation signals; completion waits for actual cleanup
and owner joins. Production capabilities require threaded Boehm, OpenSSL 3 and
libcurl. The adjacent [C TDF3 delivery](../../sdk/docs/generated-c-library.md)
documents typed headers, submit/drive/wake/cancel/take/destroy, buffer releases,
limits and native consumer evidence.

For sequential scalar/string exports, compiling a package other than `main` for the C target produces `goalchemy.h`, `main.c`, `rt/`, and `build.sh`, which builds `libgoalchemy.a`. The host calls `goalchemy_init()` once, then the exported functions of the root package. Each export is `int pkg_Name(params..., results*...)`: it returns 0 on success and 1 after an unrecovered panic, whose report `goalchemy_panic_message()` returns. Integers cross as `int64_t` (`uint64_t` for `uint64` and `uint`), Booleans as `bool`, and strings as a pointer and a length. Exported functions must not suspend, and other parameter types are rejected.

When the working directory or a parent has a `.toolchains` directory holding `jdk-*` or `dotnet`, `goalchemy run` and the test suites use those toolchains.
On Linux x64, `scripts/fetch-toolchains.sh` installs the versions and verified
archives in `toolchains.lock`, including bdwgc and the clang used by
`tests/sanitize`. The sanitizer runner uses that clang with normal address
randomization. `scripts/fetch-toolchains.sh llvm` installs only clang and its
private `libtinfo5` dependency.

Programs write output with Go's `print` and `println` builtins, which go to standard error. An unrecovered panic prints `panic: <value>` and exits with status 2, as Go does.

## What is rejected

`goalchemy check` reports unsupported constructs with stable codes (see [diagnostics](../specs/diagnostics.md)) and a one-line remedy. Release 0.1 rejects the following:

- generics, floating-point and complex numbers, and `uintptr`
- `unsafe`, cgo, `goto`, and range over functions
- pointers to slice or array elements, including pointer-receiver calls on elements
- concurrency under the default sequential gate (compile with `-gate cooperative`)
- imports other than module source and `github.com/eugenioenko/goalchemy/std/...` or `.../lib/...` packages, including the standard library

Goalchemy replaces the standard library with packages from two roots. Both keep the standard names, so code reads as ordinary Go:

- **`github.com/eugenioenko/goalchemy/std/...`** holds pure-logic packages written once in the Goalchemy subset. The compiler translates them with your program, so they behave identically on every target and need no native dependencies.
- **`github.com/eugenioenko/goalchemy/lib/...`** holds capability packages that reach the host. Each has a native implementation per target, checked against a contract, and some need native dependencies.

The split, and the core runtime layer behind both, is explained in [the runtime library reference](library.md#three-layers).

| Import | Provides |
| --- | --- |
| `github.com/eugenioenko/goalchemy/std/strings` | `Builder`, `Reader`, `Split`, `Join`, `Fields`, `Index`, `Contains`, `Trim*`, `Cut`, `Replace`, `NewReplacer`, `ToLower`/`ToUpper`, `EqualFold`, and more |
| `github.com/eugenioenko/goalchemy/std/strconv` | `Itoa`, `Atoi`, `ParseInt`/`ParseUint`/`ParseBool`, `FormatInt`/`FormatUint`/`FormatBool`/`FormatFloat`, `ParseFloat`, `Append*`, `Quote`/`QuoteRune` and their `ToASCII` forms, `CanBackquote`, `IsPrint`, `NumError` |
| `github.com/eugenioenko/goalchemy/std/bytes` | `Buffer` (also an `io.Reader`, `io.ReaderFrom` and `io.WriterTo`), `Reader`, `Equal`, `Compare`, `Index`, `Split`, `Fields`, `TrimSpace`, and more |
| `github.com/eugenioenko/goalchemy/std/io` | `Reader`, `Writer`, `Closer`, `Seeker`, `ReaderAt`, `WriterAt`, `ReaderFrom`, `WriterTo` and their compositions, `EOF` and the other `Err*` values, `Seek*` constants, `Copy`/`CopyN`/`CopyBuffer`, `ReadAll`, `ReadFull`, `ReadAtLeast`, `WriteString`, `LimitReader`, `SectionReader`, `OffsetWriter`, `MultiReader`, `MultiWriter`, `TeeReader`, `NopCloser`, `Discard`; no `Pipe` |
| `github.com/eugenioenko/goalchemy/std/sort` | `Sort`, `Stable`, `Ints`, `Strings`, `Search`, `Reverse` over `Len`/`Less`/`Swap` |
| `github.com/eugenioenko/goalchemy/std/unicode` | `IsLetter`, `IsDigit`, `IsSpace`, `IsUpper`, `IsPrint`, `ToLower`, `ToUpper`, `SimpleFold`, and more |
| `github.com/eugenioenko/goalchemy/std/unicode/utf8` | `DecodeRune`, `EncodeRune`, `AppendRune`, `RuneCountInString`, `ValidString`, and more |
| `github.com/eugenioenko/goalchemy/std/encoding/hex` | `EncodeToString`, `DecodeString`, `Encode`, `Decode` |
| `github.com/eugenioenko/goalchemy/std/encoding/binary` | `BigEndian`/`LittleEndian` `Uint16/32/64`, `PutUint*`, `AppendUint*`, varints |
| `github.com/eugenioenko/goalchemy/std/encoding/json` | `Marshal`, `MarshalIndent`, `Unmarshal`, `Encoder` (`NewEncoder`, `Encode`, `SetIndent`, `SetEscapeHTML`), `Decoder` (`NewDecoder`, `Decode`, `Token`, `More`, `UseNumber`, `DisallowUnknownFields`, `Buffered`, `InputOffset`), `Token`, `Delim`, `Marshaler`, `Unmarshaler`, `RawMessage`, `Number`, `Valid`, `Compact`, `Indent`, `HTMLEscape`, `SyntaxError`, `UnsupportedTypeError`, `UnsupportedValueError`, `MarshalerError` (see below) |
| `github.com/eugenioenko/goalchemy/std/encoding/jsonvalue` | `Parse`/`ParseWithLimits`, `Encode`/`EncodeWithLimits`, `Value` with `Kind`, `Get`/`Lookup`/`Has`, `Index`, `Len`, `Keys`, typed `String`/`Bool`/`Int64`/`Uint64`/`Float64` and checked `As*` forms, `Object`, `Array`, `String`, `Int`, `Uint`, `Float`, `Number`, `Bool`, `Null`, `Set`, `Append`, `Limits`, `SyntaxError`, `LimitError`, `InvalidValueError` (see below) |
| `github.com/eugenioenko/goalchemy/std/encoding/protojson` | `Marshal`, `Unmarshal`, `UnmarshalValue`, `Message`, `Encoder`, `Decoder`, `Timestamp`, `Duration`, `Empty`, `FieldMask`, `Any`, `NewAny`, `Register`, for messages generated by [`protoc-gen-goalchemy`](protobuf.md) |
| `github.com/eugenioenko/goalchemy/std/http` | `Do`, `Request`, `Response`: an HTTP request whose body is an `io.Reader` and whose response body is an `io.ReadCloser`, [streamed](#streamed-http-bodies) (cooperative gate) |
| `github.com/eugenioenko/goalchemy/std/connect` | `Client` (with `SendTimeout` to send `Connect-Timeout-Ms`, off by default), `NewClient`, `CallUnary`, `HTTPClient` (a caller-supplied transport with the shape of `lib/http.Do`), `Error`, `ErrorDetail`, `Code` and its constants, `CodeOf`: unary Connect calls with the JSON codec, used by [generated service clients](protobuf.md#services) (cooperative gate) |
| `github.com/eugenioenko/goalchemy/std/time` | `Time`, `Duration`, `Now`, `Sleep` (cooperative gate), `Unix`, `Date`, `Since`, `Until`, `Add`/`Sub`/`AddDate`, `Truncate`/`Round`, `Format`/`Parse` for `RFC3339` and `RFC3339Nano`, `ParseDuration` (UTC only; see below) |
| `github.com/eugenioenko/goalchemy/std/net/url` | `Parse`, `ParseRequestURI`, `URL` with `String`, `Hostname`, `Port`, `Query`, `IsAbs`, `JoinPath`, `ResolveReference`, `Parse`, `EscapedPath`, `RequestURI`, `Redacted`, `Clone` and `MarshalBinary`/`UnmarshalBinary`; `JoinPath`, `PathEscape`/`PathUnescape`, `QueryEscape`/`QueryUnescape`, `Values` (`Get`, `Set`, `Add`, `Del`, `Has`, `Encode`, `Clone`), `ParseQuery`, `User`, `UserPassword`, `Userinfo`, `Error`, `EscapeError`, `InvalidHostError` (see below) |
| `github.com/eugenioenko/goalchemy/std/net` | `JoinHostPort`, `SplitHostPort`, `AddrError`; no dialing, listening or name resolution |
| `github.com/eugenioenko/goalchemy/std/errors` | `New`, `Is`, `As`, `Unwrap`, `Join`, `ErrUnsupported`, including `Unwrap() []error` trees |
| `github.com/eugenioenko/goalchemy/std/fmt` | `Sprintf`, `Sprint`, `Sprintln`, `Errorf` with one or more `%w`, `Append`/`Appendf`/`Appendln`, `Stringer`, `GoStringer`, `Formatter`, `State`, `FormatString` (see below) |
| `github.com/eugenioenko/goalchemy/std/os` | `ReadFile`, `WriteFile`, `PathError`, `FileMode`, `ErrNotExist`, `ErrExist`, `ErrPermission`, `ErrInvalid`, `MaxFileBytes` (see below) |
| `github.com/eugenioenko/goalchemy/std/log/slog` | `Logger`, `Default`/`SetDefault`, `New`, `Debug`/`Info`/`Warn`/`Error`, their `Context` forms, `Log`/`LogAttrs`, `With`/`WithGroup`, `Attr`, `Value`, `Level`, `LevelVar`, `Record`, `Handler`, `HandlerOptions`, `LogValuer`, `DiscardHandler` and `NewHostHandler` (see below) |
| `github.com/eugenioenko/goalchemy/lib/errors` | `New`, `Is`, `Unwrap`; the native layer under `std/errors`, whose `Is` does not follow `Unwrap() []error` |
| `github.com/eugenioenko/goalchemy/lib/sync` | `Mutex`, `WaitGroup` (cooperative gate) |
| `github.com/eugenioenko/goalchemy/lib/context` | `Context`, `CancelFunc`, `Background`, `WithCancel`, `WithTimeout`, `Canceled`, `DeadlineExceeded`, and the `Done` and `Err` methods (cooperative gate) |
| `github.com/eugenioenko/goalchemy/lib/time` | `Duration`, its unit constants, and `Sleep` (cooperative gate); a separate type from `std/time.Duration` |
| `github.com/eugenioenko/goalchemy/lib/runtime` | `Gosched` (cooperative gate) |
| `github.com/eugenioenko/goalchemy/lib/task` | `All` (cooperative gate) |
| `github.com/eugenioenko/goalchemy/lib/crypto` | `Key`, SHA-256, HMAC-SHA256, HKDF-SHA256, AES-256-GCM, RSA OAEP (2048, 3072 and 4096-bit), RS256/384/512, ES256/384/512, P-256/P-384/P-521 ECDH, key generation, PEM and JWK |
| `github.com/eugenioenko/goalchemy/lib/encoding` | `Base64Encode`, `Base64Decode`, `Base64URLEncode`, `Base64URLDecode` |
| `github.com/eugenioenko/goalchemy/lib/http` | `Do`, a bounded GET or POST exchange, `DoWith`, the same with [transport options](#http-transport-options), and `Open`, an `Exchange` whose bodies are written and read in chunks |
| `github.com/eugenioenko/goalchemy/lib/clock` | `Unix` and `UnixNano`, host wall-clock seconds and nanoseconds |
| `github.com/eugenioenko/goalchemy/lib/log` | `Enabled` and `Emit`, the host log sink behind `std/log/slog` |
| `github.com/eugenioenko/goalchemy/lib/os` | `ReadFile` and `WriteFile` with portable status codes, the host file system behind `std/os` |
| `github.com/eugenioenko/goalchemy/lib/callback` | `Request`, a bounded call to a host-registered callback |

The [runtime library reference](library.md) documents every function. For `lib/` it lists the gate, bounds, error behavior, per-target availability and native dependencies. It is generated from the `std/` and `lib/` sources and the contracts with `make spec-generate`.

`std/time` differs from Go's `time` in these ways:

- Every `Time` is UTC. `UTC` is the only `Location`; `Parse` applies a numeric offset and returns the UTC instant, so the original offset is not kept.
- `Format` and `AppendFormat` accept only `RFC3339` and `RFC3339Nano` and panic on any other layout. `Parse` returns an error for any other layout. For these two layouts, `Parse` accepts and rejects the same inputs as Go, with the same error text.
- `Now` has no monotonic reading, so `Since`, `Until` and `Sub` follow host clock adjustments. `Now` precision is the host clock's: milliseconds on TypeScript, microseconds or better elsewhere.
- `Sleep` takes a `std/time.Duration` and delegates to `lib/time.Sleep`. `lib/time.Duration`, used by `lib/context.WithTimeout`, is a separate type; convert with `libtime.Duration(d)`.

`std/net/url` is a port of the reference toolchain's `net/url`, with the same parsing rules, escaping, results and error text, checked against it by Go unit tests:

- Parsing follows the defaults Go applies to a module declaring `go 1.25`, the Goalchemy source baseline: a host with several colons, such as `https://example.com:80:`, is accepted (GODEBUG `urlstrictcolons=0`), and `ParseQuery` rejects more than 10000 parameters.
- Bracketed IPv6 hosts are validated as `net/netip.ParseAddr` does, with its error text, without depending on `net/netip`.
- `Error.Err` values carry Go's messages. Errors Go builds with `fmt.Errorf` are `errors.New` values here, and the `invalid host:` error still unwraps to its cause.

`std/fmt` and `std/errors` work without reflection:

- `fmt` formats operands that are nil, booleans, integers, floats and strings of predeclared types, `[]byte`, and values whose type implements `Formatter`, `GoStringer` (for `%#v`), `error` or `Stringer`. Verbs, flags, width, precision, argument indexes, and the `%!verb(...)`, `MISSING`, `BADINDEX` and `EXTRA` notes match Go for these operands. The compiler rejects other operand types, such as structs, maps, pointers without methods and named basic types without methods, when their static type is known (`GCS006`); behind an interface they print as `%!v(unsupported)`. Convert named basic types, as in `int(level)`, or give them a `String` method.
- `%T` and the type names in bad-verb and `EXTRA` notes are known only for the basic types above and print as `?` otherwise. `%p` prints `%!p(unsupported)`.
- A method that panics prints `%!v(PANIC=String method: ...)` as in Go, except that any nil pointer dereference prints `<nil>`, where Go does so only for a nil receiver.
- `Sprint` treats only predeclared `string` operands as strings when deciding where to add spaces; Go also treats named string types as strings.
- `errors.As` needs a target whose static type is a pointer to an interface type or to a type implementing `error`. The compiler expands each call for that type and rejects other targets, `errors.As` used as a function value, and `defer` or `go` of it (`GCS006`). A nil target pointer panics as in Go.
- `fmt.Print`, `Printf` and `Println` are not available; use the `print` and `println` builtins with `fmt.Sprintf`.

`std/log/slog` follows Go's `log/slog` API, and the host application decides where records go:

- The default logger, and any logger built with `slog.NewHostHandler`, sends records to the host log sink. A record goes out when it passes the handler's `HandlerOptions.Level`, if set, and the host's minimum level. Until the host installs a sink, that minimum is `LevelWarn` and records are written to standard error, so libraries are quiet by default.
- Records are rendered like `slog.TextHandler` without the `time` attribute, for example `level=WARN msg=retry kas=https://kas attempt=2`. The sink also receives the level, message, Unix time in nanoseconds and the attributes as key/value strings, with group names joined to keys by `.`.
- Values are rendered through `std/fmt`, so the `fmt` operand rules above apply. The compiler rejects attribute values of unsupported static types, unless they implement `LogValuer` or `MarshalText` (`GCS006`).
- Source locations are not recorded: `HandlerOptions.AddSource` is ignored, `Record.PC` is an `int` that is always zero, and `NewRecord` takes an `int` for its unused `pc` argument. `ReplaceAttr` is not called for `time`, which the host handler does not render.
- There is no `TextHandler` or `JSONHandler`, because there is no `io.Writer`; implement `Handler` for custom output. Handlers take a `lib/context.Context`, which the default methods fill with `context.Background()`.

Hosts install a sink before calling into a library. The handler runs synchronously on the calling thread and must not block or call back into the library; exceptions and panics it raises are discarded. Levels follow `log/slog`: -4 debug, 0 info, 4 warn, 8 error.

| Target | Install a sink | Record |
| --- | --- | --- |
| Go | `log.SetHandler(func(log.Record), level)` from `goalchemyout/cap/log`; `log.Slog(*slog.Logger)` adapts a Go logger | `Level`, `Time`, `Message`, `Attrs` (key/value pairs), `Text` |
| TypeScript | `setLogHandler(record => ..., level)` exported by the library | `level`, `unixNano`, `time`, `message`, `attrs` (`[key, value]` pairs), `text` |
| Python | `set_log_handler(handler, level)` exported by the library; `logging_handler(logger)` forwards to the `logging` logger `goalchemy` | `LogRecord` with `level`, `unix_nano`, `time`, `message`, `attrs`, `text` |
| Java | `io.goalchemy.runtime.Log.setHandler(record -> ..., level)`; `Log.systemLogger()` forwards to `System.Logger` `goalchemy` | `Log.Record` with `level()`, `unixNano()`, `time()`, `message()`, `attrs()`, `text()` |
| C# | `Rt.Log.SetHandler(record => ..., level)`; `Log.TraceHandler()` writes to `System.Diagnostics.Trace` | `Log.Record` with `Level`, `UnixNano`, `Time`, `Message`, `Attrs`, `Text` |
| Rust | `set_log_handler(Some(Arc::new(\|r: &LogRecord\| ...)), level)` exported by the crate | `LogRecord` with `level`, `unix_nano`, `time()`, `message`, `attrs`, `text` |
| C | `gxc_set_log_handler(handler, state, level)` in `goalchemy.h` | `gxc_log_record` with lengths and NUL-terminated copies, freed when the handler returns |
| Swift | `setLogHandler({ record in ... }, level:)` exported by the module | `GoalchemyLogRecord` with `level`, `unixNano`, `time`, `message`, `attrs`, `text` |

Passing a null handler (`nil`, `None`, `NULL`) restores the default standard error sink at `LevelWarn`. Strings reach the handler decoded as UTF-8, with invalid sequences replaced; C receives the raw bytes.

`std/encoding/json` encodes and decodes Go values exactly as the reference toolchain's `encoding/json` does, without reflection:

- The compiler describes the static type of each `Marshal` and `MarshalIndent` operand, and the type an `Unmarshal` target points to, with every type reachable from it, following Go's rules for struct tags, embedded fields, `omitempty`, the `string` option, map key ordering, `[]byte` as base64, nil slices and maps as `null`, pointer cycles, `Marshaler` and `encoding.TextMarshaler` (including pointer-receiver methods on addressable values), HTML escaping and float formatting.
- Types encoding/json cannot encode, such as channels, functions, complex numbers and maps with other key types, are rejected at compile time (`GCS006`) unless the field is excluded with `json:"-"`.
- Struct tags are limited to names made of letters, digits and the punctuation encoding/json has always accepted, and to the `omitempty` and `string` options. Other names and options, `omitzero`, the `string` option on `json.Number`, and `json` tags on unexported fields are rejected (`GCS006`), because their meaning differs between Go releases.
- Values held in interfaces, such as `any` fields, `[]any` and `map[string]any`, are encoded from their dynamic value when it is nil, a boolean, integer, float or string of a predeclared type, `[]byte`, `[]any`, `map[string]any`, or implements `Marshaler` or `encoding.TextMarshaler`, as `json.Number`, `json.RawMessage` and `jsonvalue.Value` do. Other dynamic values, such as structs and named types without these methods, are rejected when the compiler sees them stored in an interface in the `Marshal` operand, and return `*UnsupportedTypeError` otherwise; Go encodes them by reflection.
- `UnsupportedTypeError.Type` and `MarshalerError.Type` are type names (`string`) rather than `reflect.Type` values, and `UnsupportedValueError` has no `Value` field. Their `Error` text matches Go.
- `json.Number` implements `MarshalJSON` and `UnmarshalJSON`, so an invalid number literal is reported as a `*MarshalerError` when encoding, and decoding errors carry no offset or field, as Go's `Number` reports them.
- `Unmarshal` follows Go for case-insensitive field names (`strings.EqualFold`), ignored unknown members, duplicate members (the last one wins), `null` (which sets pointers, slices, maps and interfaces to nil and leaves other values unchanged), merging into existing structs, slice elements, arrays and pointed-to values, the `string` option, `Unmarshaler` and `encoding.TextUnmarshaler` (including `null` passed to `UnmarshalJSON`), base64 `[]byte`, decoding into `any` as `map[string]any`, `[]any`, `float64`, `string`, `bool` or `nil`, invalid UTF-8 replaced by U+FFFD, and continuing after a type error to return the first one. `SyntaxError` and `UnmarshalTypeError` carry Go's messages, offsets, `Struct` and `Field` values.
- `Unmarshal` needs a target whose static type is a pointer or an interface. Other targets, types it cannot decode such as channels, functions, complex numbers and map keys that are not strings, integers or `encoding.TextUnmarshaler`, and embedded pointers to unexported struct types are rejected (`GCS006`).
- An interface target, or an interface field that already holds a non-nil pointer, is decoded into when that pointer is a `*any`, `*map[string]any`, `*[]any`, `*string`, `*bool` or `*float64`, or implements `Unmarshaler` or `encoding.TextUnmarshaler`. Other pointers are rejected when the compiler sees the conversion and return `*UnsupportedTypeError` otherwise; Go decodes into them by reflection. Interface fields that hold nothing or a non-pointer are replaced by the decoded `any` value, as in Go.
- `UnmarshalTypeError.Type` and `InvalidUnmarshalError.Type` are type names (`string`). The `Err` of an `UnmarshalTypeError` for invalid base64 or a non-string `encoding.TextUnmarshaler` input has Go's text but is not a `base64.CorruptInputError`.
- `Encoder.Encode` and `Decoder.Decode` check and encode or decode their operand like `Marshal` and `Unmarshal`. `Encoder` follows Go for the trailing newline, `SetIndent`, `SetEscapeHTML` and a write error that stops later calls. `Decoder` reads from any `std/io` reader, such as a streamed HTTP body, and reproduces the reference toolchain's decoder (Go 1.27, built on `encoding/json/v2`) for `Token`, `More`, `InputOffset`, `Buffered`, buffering, syntax errors and their offsets, `io.EOF` and `io.ErrUnexpectedEOF`, `UseNumber` and `DisallowUnknownFields`.
- `Marshal`, `MarshalIndent`, `Unmarshal`, `Encoder.Encode` and `Decoder.Decode` cannot be used as function or method values or with `defer` or `go` (`GCS006`). An `Encoder` or `Decoder` called through an interface of your own encodes and decodes like an `any` operand.

`std/encoding/jsonvalue` handles JSON whose shape is not fixed, such as JWT claims or partly read responses, as a tree of values. It differs from Go's `encoding/json` in these ways:

- Build and inspect `Value` trees instead of Go types. A `Value` implements `json.Marshaler` and `json.Unmarshaler`, so it can be passed to, or be a field of a value passed to, `json.Marshal` and `json.Unmarshal`; decoding parses with `Parse`, so the rules below apply.
- Numbers keep their source text. `Number` returns it, `Int64`/`Uint64` accept only integer text that fits (`1.0` and `1e3` are rejected), and `Float64` rounds to the nearest float64.
- Duplicate object member names are a syntax error when parsing and an `InvalidValueError` when encoding; Go keeps the last one.
- Parsing and encoding are bounded by `Limits`: by default 16 MiB of input or output, nesting depth 128 (`MaxDepth`), 1Mi values and 16 MiB per string.
- Accessors never panic. A missing member, an out-of-range index or a value of another kind returns the zero result, and `Exists` reports a failed lookup.
- `String` returns the content of a JSON string and the compact encoding of any other value, so `fmt` prints values as JSON. Use `AsString` to require a string.
- Strings are encoded as `json.Marshal` encodes them, including `\u003c`, `\u003e` and `\u0026` for `<`, `>` and `&`. Invalid UTF-8 and unpaired surrogate escapes become U+FFFD, as in Go.

`std/os` reads and writes whole files:

- Errors are `*PathError` values with Go's `Op`, `Path` and Linux error text, for example `open key.pem: no such file or directory`. `errors.Is` matches `ErrNotExist`, `ErrExist`, `ErrPermission` and `errors.ErrUnsupported` as in Go, and `errors.As` finds the `*PathError`. Reading a directory fails with op `read`, as on Linux; other failures use op `open`.
- Failures are reported in these classes: not found, exists, permission denied, is a directory, not a directory, file too large, operation not supported, invalid argument (for a NUL byte in the name), and input/output error for anything else.
- Files and data larger than `MaxFileBytes` (1 GiB) are rejected with `file too large`.
- `WriteFile` creates a missing file with `perm` minus the process umask and keeps the permissions of an existing file it truncates, as in Go. Only the low nine permission bits are applied; hosts without POSIX permissions ignore them.
- Names are byte strings passed to the host unchanged on Go, TypeScript (Node), Python, Rust, C and Swift. Java and C# decode them as UTF-8, replacing invalid sequences. Relative names resolve against the process working directory.
- Calls are synchronous and never suspend the cooperative scheduler. TypeScript reads files only through the Node entry points (executables and the library package's `node` export, which install the `node:fs` adapter); in browsers and other portable hosts every call fails with `operation not supported`.

Every package is ordinary Go, so programs still build and run with the Go toolchain. Importing a standard package such as `"sync"`, `"strings"` or `"fmt"` directly is rejected with a remedy naming its `std/` or `lib/` replacement.

## Cooperative execution

With `-gate cooperative` (or `gate: cooperative` in `goalchemy.yaml`), programs may use `go`, channels, `select`, and the synchronization capabilities above. Only one task runs at a time:

- Runnable tasks run in FIFO order. A task keeps control until it blocks, yields, returns, or panics.
- `select` chooses among ready cases with a seeded xorshift32 source: `GOALCHEMY_SEED`, default 1.
- The default entry uses a virtual clock for `time.Sleep` and context deadlines,
  advancing only when every task is blocked. Generated programs that use
  `lib/crypto`, `lib/http` or `lib/callback` start on their target's host entry
  instead, with owner-local monotonic real time. TypeScript
  cooperative output can select real time through portable `host.ts`/`runHost`;
  its module-global source state is not an isolated library instance. Java
  cooperative output exposes `Main.runHost()` with the same executable-global
  limitation and `System.nanoTime` deadlines. C# cooperative output exposes
  Task-returning `GoProgram.runHost()` with `Stopwatch` deadlines and serialized
  executable globals; it is not an isolated library API. Python cooperative
  output exposes blocking `main.runHost()` with `monotonic_ns` deadlines, native
  mailbox completion and serialized executable globals; it is not an SDK library.
  C cooperative output exposes `goalchemy_run_host()` with checked monotonic
  deadlines, owned native wire records and resource ACK before return; see the
  [C executable boundary and limits](c-host-operations.md).
- A deadlock prints `fatal error: all goroutines are asleep - deadlock!` and exits with status 2.

Every target follows the same scheduler contract and the same shared lowering:

1. **Effect analysis** finds the functions that may suspend: those that use channels, `select`, or suspending capabilities, directly or through calls, function values, or interface methods.
2. **The shared IR pass** cuts each of those functions at its pause points into continuation blocks. The function becomes a resumable *frame*: an object holding its locals and the block to resume at.
3. **Each runtime** provides pause primitives (`recv`, `send`, `select`, lock, sleep, spawn) and a trampoline scheduler. Deferred calls, `panic`, and `recover` in frames are managed by the runtime, per task.

No target relies on native coroutines or threads for source tasks.

Generated Go `lib/http.Do` runs the bounded native transport on host workers.
Workers enqueue owned completions; the scheduler alone resumes source tasks,
so concurrent source cancellation remains runnable during network and body
reads. Context deadlines start at creation and an earlier parent deadline
shortens the required real HTTP timeout. Entry return cancels and cleans pending
background operations. Other targets run `lib/http.Do` through their own host
adapters; exported asynchronous libraries remain gated. See
[host lifecycle and target-port requirements](host-operations.md).

`github.com/eugenioenko/goalchemy/lib/task` provides `task.All(fns ...func())`. It runs each function as a task, in argument order and one at a time, and returns when all have finished. With the Go toolchain the same package runs the functions as goroutines.

### HTTP transport options

`http.DoWith` takes the arguments of `http.Do` plus options as alternating
names and values:

| Option | Value |
| --- | --- |
| `http.ClientCert` and `http.ClientKey` | PEM client certificate (chain) and unencrypted private key (PKCS#8, PKCS#1 or SEC1), offered when the server asks for one (mTLS). Both or neither. |
| `http.RootCAs` | PEM certificates trusted in addition to the system roots |
| `http.InsecureSkipVerify` | `"true"` disables server certificate and host name verification; `"false"` is the default |
| `http.UnixSocket` | Path of a Unix domain socket to connect to instead of the URL's host (no proxy). The URL host still sets the `Host` header and, for `https`, the server name that is verified. |

Server certificates are verified unless `InsecureSkipVerify` is `"true"`.
Odd, unknown or repeated options fail as invalid requests; certificates or
keys that do not parse fail with `http: invalid TLS option`.
Windows named pipes and plaintext HTTP/2 (h2c) are not available.

| Target | TLS options | `UnixSocket` |
| --- | --- | --- |
| Go | `crypto/tls` in `net/http` | `net.Dialer` on `unix` |
| TypeScript (Node) | `node:https` `cert`, `key` and `ca` (with `tls.rootCertificates`) | `socketPath` |
| TypeScript (browser) | Not supported: any option fails with `http: transport options are not supported in browsers` | Not supported |
| Python | `ssl.SSLContext` (`load_cert_chain` reads the credentials from a private temporary directory) | `AF_UNIX` socket under `http.client` (POSIX) |
| Java | `SSLContext` from in-memory key and trust stores in `java.net.http.HttpClient` | Not supported: `HttpClient` connects only over TCP, so the option fails with `http: unix sockets are not supported on this host` |
| C# | `SocketsHttpHandler.SslOptions` with a custom-root chain check | `ConnectCallback` with `UnixDomainSocketEndPoint` |
| Rust | reqwest with rustls: `Identity`, merged root certificates | `ClientBuilder::unix_socket` (Unix hosts) |
| C and Swift | libcurl certificate blobs, roots added to the OpenSSL store | `CURLOPT_UNIX_SOCKET_PATH` |

### Streamed HTTP bodies

`std/http.Do` sends a `Request` whose `Body` is an `io.Reader` and returns a
`Response` whose `Body` is an `io.ReadCloser`, so bodies larger than
`lib/http.MaxBytes` pass through without being held in memory:

```go
resp, err := http.Do(ctx, &http.Request{
	Method: "POST", URL: url, Body: file, ContentLength: size,
	TimeoutMillis: 600000, Options: []string{lhttp.RootCAs, ca},
})
if err != nil {
	return err
}
defer resp.Body.Close()
_, err = io.Copy(out, resp.Body)
```

- A `Body` with `ContentLength` zero or less is sent chunked.
- `TimeoutMillis` (up to 24 hours) bounds the whole exchange, including
  reading the response body, like Go's `Client.Timeout`. Canceling `ctx`
  also ends it.
- Redirects are not followed, and compression is neither requested nor
  decoded.
- `Options` are the [transport options](#http-transport-options).
- It runs on `lib/http.Open`: an `Exchange` with `Write`, `Response`, `Read`
  and `Close`, each moving at most 1 MiB.

| Target | Exchange |
| --- | --- |
| Go | `net/http` with an `io.Pipe` request body |
| TypeScript (Node) | `node:http`/`node:https`, writes waiting for each flush and the response read in paused mode |
| TypeScript (browser) | `fetch`, with the limits below |
| Python | `http.client` with chunked framing |
| Java | `java.net.http.HttpClient` with an `InputStream` body publisher fed one chunk at a time |
| C# | `HttpClient` with a custom `HttpContent` fed one chunk at a time |
| Rust | reqwest with a channel-fed `http_body::Body` on a per-exchange Tokio thread |
| C and Swift | a libcurl transfer thread per exchange, pausing its read and write callbacks |

Browsers cannot stream request bodies over HTTP/1.1, so in a browser the
request body is collected (at most 64 MiB) and sent when the response is
requested; the response body still streams. Browser `fetch` decodes
compressed responses itself.

## Behavior Go leaves open

Goalchemy fixes some behavior that Go leaves to the implementation. These choices are identical on every target:

- Map iteration follows insertion order. An entry deleted before it is visited is skipped, and entries inserted during iteration are not visited.
- When `append` exceeds capacity, the new capacity is `max(needed, max(1, 2*cap))`.
- `[]byte(s)` and `[]rune(s)` have capacity equal to their length.
- Operands are evaluated left to right wherever Go allows a choice.

## Byte storage

TypeScript byte slices and arrays use `Uint8Array`, including named types
with underlying `uint8` elements. Slice views share native storage; byte
string conversions copy and preserve arbitrary Go string bytes. Java byte
slices and arrays use native `byte[]`, with unsigned reads through
Go's existing `long` integer representation. Java preserves aliases, zeroed
capacity and arbitrary-byte string copies too. C# byte slices and arrays use
native `byte[]`, with primitive reads widened to Go `long`, zeroed capacity,
shared views and independent arbitrary-byte string copies. Python byte slices
and arrays use native `bytearray`, with fixed-length shared views, zeroed
capacity and independent conversions to/from immutable `bytes` Go strings.
Rust byte slices and arrays use traced native `Vec<u8>` backing, with typed nil
headers, zeroed capacity, shared views and independent binary string copies.
C byte slices and arrays use collector-owned native `uint8_t` backing, with
typed nil headers, zeroed capacity, shared interior views and independent
binary string copies. See the bounded [TypeScript evidence](typescript-byte-storage.md),
[Java evidence](java-byte-storage.md), [C# evidence](csharp-byte-storage.md)
[Python evidence](python-byte-storage.md), [Rust evidence](rust-byte-storage.md)
and [C evidence](c-byte-storage.md)
for coverage and remaining SDK, browser and library requirements.
