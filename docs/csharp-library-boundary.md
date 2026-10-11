# C# copied value libraries

C# library emission builds a .NET8 class library from actual shared source. An
independent consumer references `lib/main.dll` and imports
`Goalchemy.Generated.GoProgram`. It needs neither the Goalchemy compiler nor
source Go at runtime. `sh build.sh` builds the generated `main.csproj`; building
requires the .NET8 SDK on PATH. Executables retain their existing `run.sh` entry.

Exported value trees use `long`, `bool`, binary `string`, native `byte[]`, arrays
and generated public field classes. Generic strings contain one character per
source byte, including arbitrary nonUTF8 data. The SDK facade separately uses
strict UTF8 text. Signed and unsigned source integer widths are checked without
host floating-point conversions; unsigned64 values retain their bits in `long`.
Nil slices remain null, empty slices remain empty arrays. Maps, pointers, native
handles, functions, private fields, variadic exports and runtime-name collisions
are rejected with GCE007. A final source error becomes `Rt.Library.Failure` with
copied nested fields; panic, fatal, invalid_argument, canceled and host_fault
remain distinct categories. Panic failures use an owned stable message without
exposing the source GoPanic, Box, chained panic or frame through InnerException.

Each call synchronously validates and copies mutable inputs and the callback
registry before enqueueing. Public generated field classes are native value
containers; the acquired owner alone builds source structs, slices, boxes and
key shells. One assembly's static source graph serializes calls. Each acquired
call resets globals, initializes packages once and owns a fresh source context,
scheduler, native registry and callbacks. A retained entry Frame captures its
results before executable root cleanup clears task.rv. Results and nested error
fields are copied while the owner lives. Shutdown waits all native cleanup ACKs,
then clears source globals, field-reference caches, callback registrations and
native keys while the entry guard remains active. No source reference escapes
the public value boundary. Calls on [handles](library-handles.md) use their
instance's globals instead. See [package variables in libraries](usage.md#package-variables-in-libraries).

`Operation<T>.Completion` is a Task; `Cancel()` requests cancellation and a
CancellationToken can be supplied in Options. Queued cancellation does no source
work, removes its registration and copied roots, and publishes outside the queue
monitor. Active cancellation wakes the driver and cancels its source context;
completion waits provider/native resource settlement. TaskCompletionSource uses
RunContinuationsAsynchronously, so user continuations cannot run under a queue
monitor or block the source owner. A provider which retains resources and never
settles deliberately leaves its operation pending. Native owner Thread.Start
failure rolls back queue/running state under the monitor, releases registration
outside it and returns typed host_fault. Successful retirement releases the
entry guard before Task publication.

Provider registrations contain native delegates. `Callback.Request.Retain` and
`OnStop` declare outstanding provider work. `Resolve`, `Reject` and `Fault` are
terminal resource-settlement statements; duplicate/late replies are ignored.
Terminal settlement seals late Retain/OnStop immediately. User stop hooks run
outside the request monitor; publication/ACK waits all acquired stop invocations
and preserves their faults. Fault publication uses a fixed message without
invoking provider exception formatting. Starting/pending staging preserves
host_fault if synchronous provider start settles and then throws. Retained
submission or stop faults await settlement.
Tasks supplied through FromTask must complete only after provider resources are
released. Provider code never drives source frames and must not synchronously
wait for a call queued behind its own active source operation.

Native crypto uses maintained .NET8 System.Security.Cryptography: native random,
SHA256/HMACSHA256 including empty keys, HKDFSHA256, AES256GCM with nonce12/tag16,
RSA2048 OAEP with SHA1/MGF1SHA1/empty label, RS256 PKCS1 signatures, ES256 exact
P1363, and P256 DeriveRawSecretAgreement32. Bounded PKCS1 RSA, SPKI, PKCS8 and
certificate PEM imports reject other algorithms, curves and malformed keys.
Native .NET8.0.31 imports P256 private PKCS8 with omitted public point and derives
matching Q; no handwritten crypto or additional package is needed. Present Q is
checked against native derivation from D. JWK fields are canonical base64url.
Keys are owner-associated opaque aliases. Closing invalidates new acquisitions;
acquired leases survive until their native operation completes. Native key
instances are disposed before lease release; private DER is zeroed on final
retirement. Worker termination precedes cleanup acknowledgement.

HTTP uses a dedicated SocketsHttpHandler/HttpClient per request, trusted default
TLS, HTTP1.1, no redirects, bounded bodies/headers, explicit source deadlines and
active cancellation. Dedicated Latin1 header selectors preserve accepted source
header bytes; the importing consumer verifies 80/ff request bytes and ff response
bytes on an actual TCP wire. Controls, forbidden framing headers, invalid UTF8
URLs, BOM, credentials, fragments and invalid schemes/ports reject before contact.
Default .NET system proxy discovery remains native behavior. All original, gzip
and wrapper streams, response/request, client/handler and cancellation work are
retired despite individual cleanup faults. Distinct cleanup faults become
host_fault after release; the same already-declared read failure stays transport
failure. Stop tasks are registered atomically and cleanup seals their registry
before waiting. No process-wide crypto, TLS, parser or threading setting changes.

Tests in `tests/contracts/csharp_crypto_test.go` verify published vectors and
independent native Go signatures/OAEP/ECDH/HKDF/JWK/certificates. The separate
assembly consumer in `tests/integration/csharp_library_test.go` executes generated
values, init/repetition, nested input/output/error copies, exact longs, malformed
arguments, panic/fault recovery, provider/queue/active cancellation, real native
key alias races, HTTP wire/resource ordering, weak source collection and native
launch rejection. Existing C# host, byte and target-scoped language/conformance
checks preserve executable behavior. This boundary does not complete Python,
Rust, C or the seven-target SDK delivery goal.

## Logging

`std/log/slog` records reach the host through `Rt.Log.SetHandler(handler, level)`,
an `Action<Log.Record>`. `Log.TraceHandler()` writes records to
`System.Diagnostics.Trace`. A null handler restores standard error at warn and
above. See [the logging section of the usage guide](usage.md).

## IEEE CRC32 package dependency

Libraries that link `lib/checksum.CRC32IEEE` add the official Microsoft
`System.IO.Hashing` 8.0.0 NuGet package with an exact version and content-hash
lock. Reference the generated `main.csproj` to inherit the package transitively.
Assembly-only consumers must reference that package and deploy its DLL alongside
`lib/main.dll`. Crypto and HTTP retain their .NET built-in mappings. See
[the host mappings and acceleration limits](checksum.md).
