# Generated Java libraries

The cooperative Java target exports `io.goalchemy.generated.Generated` from a
JAR. A separately named package imports it using only the JAR and the declared
native dependency classpath. Executable emission retains `Main` and its existing
virtual entry; native capabilities use `Main.runHost()`. Library imports do not
run package initialization or call `System.exit`.

Each classloader supports one generated graph. Its runtime lives in
`io.goalchemy.runtime`, separate from executable `rt`. Independently generated
graphs require separate classloaders. This is serialized per-call execution, not
persistent source clients or arbitrary shared source references.

## Value boundary

Exports admit booleans, integer bit patterns, byte strings, arrays/slices and
structs whose fields are exported admitted values. Context parameters are
created by the owner; a final source error becomes `Library.Failure`. Pointers,
native handles, maps, functions, channels, variadic signatures and inaccessible
fields are rejected with GCE007. Public type names that collide with generated,
runtime or referenced Java classes are rejected before emission.

Integers use `long`, including source `int`; narrower input ranges are checked.
Unsigned 64-bit integers retain their bits in signed Java `long`. Generic source
strings are binary `String` values with one U+0000–U+00FF character per source
byte. The SDK façade explicitly converts application text using strict UTF-8.
A leading UTF-8 BOM is retained, including in a URL; conversion cannot silently
change the trusted resource.

Public structs and arrays are copied synchronously into native value trees
before queueing. Only the acquired owner constructs source structs, slice
headers, Boxes, contexts and key shells. Snapshot traversal rejects cycles,
depth beyond 128 and more than one million aggregate nodes. Individual buffer,
string and collection lengths have no additional byte-size cap; native storage
and source descriptor ranges still apply. Nil and empty slices remain distinct. Successful
results and recursively structured error maps are copied while the owner is
alive. Repeated `Failure.fields()` calls return independent mutable leaves.

The owner resets source globals, executes initialization exactly once, runs the
export and copies its result. It retires all native resources and callback
registrations and clears owner-only canonical field-pointer caches before
publishing the result. Executable field identity is unchanged. Canonical source
field aliases remain identical during an acquired call. Calls on
[handles](library-handles.md) use their instance's globals instead. See [package variables in libraries](usage.md#package-variables-in-libraries).

## Cancellation and providers

`Library.Operation<T>.completion()` returns a read-only CompletionStage view;
`cancel()` requests cancellation. Removing a queued call performs no source
work. Its completion is published outside the queue monitor. Active completion
waits for actual resource settlement; Future cancellation is not cleanup ACK.
Normal result publication uses a native completion thread so a continuation can
submit and wait for another call without blocking the owner.

`Callback.Provider.start(Request)` is a suspending bridge. `retain()` or
`onStop()` marks acquired native resources. A stop hook requests stop; only
`resolve`, `reject` or `fault` after release supplies terminal settlement. Input
and successful response bytes are copied. First terminal settlement wins;
late/duplicate results cannot reach a retired owner. Result decoding waits for
`start` to return: a synchronous throw after resolve/reject still wins as
`host_fault`. A pre-acquisition throw fails immediately; a post-acquisition throw
requests stop and waits for terminal resource release. Stop-hook faults also
wait and retain fault precedence.

`Callback.fromStage` maps asynchronous stage failure to declared provider
rejection. Applying the stage adapter or attaching its continuation can fail as
an adapter fault. A returned live stage is retained; an attachment failure must
still reach explicit terminal Request settlement after releasing resources.
Callers must not signal settlement before their own held resources are released.

Owner thread launch rejection rolls back the unacquired queue item and returns a
typed host fault. Completion-thread launch rejection publishes a host fault on
the JDK common-pool executor after retirement. If both independent dispatch paths
reject, the retired driver relinquishes queue/running ownership, faults detached
calls outside the monitor and publishes synchronously as an emergency path.
Source gate/native resources are released and fresh submission can recover;
arbitrary blocking host callback graphs cannot be guaranteed parallel progress
when no independent native dispatcher is available.

Errors distinguish `source`, `canceled`, `source_panic`, `host_fault` and
`invalid_argument`. The library does not route failures through executable
stderr/report/exit behavior. Unexpected adapter/linkage/cleanup failures remain
host faults; they are not disguised as invalid crypto inputs.

## Logging

`std/log/slog` records reach the host through
`io.goalchemy.runtime.Log.setHandler(handler, level)`. `Log.systemLogger()`
forwards records to the `System.Logger` named `goalchemy`. A null handler
restores standard error at warn and above. See [the logging section of the usage guide](usage.md).

## Native implementations

JDK 21 JCA supplies SecureRandom, SHA-256, HmacSHA256, AES/GCM, RSA OAEP with
explicit SHA-1/MGF1-SHA1/empty label, RSA PKCS1 SHA-256, P-256 ECDSA and ECDH.
ECDSA DER and PEM/JOSE framing is bounded; ES256 exports exactly 64 bytes.
ECDH returns the native 32-byte pre-KDF x coordinate. HMAC verification uses
`MessageDigest.isEqual`; a RAW SecretKey wrapper supports native empty keys.
No cryptographic algorithm is implemented in generated or adapter source.

The initial design proposed only JDK built-ins. JDK 21 lacks an HKDF API and
public-point derivation from standards-valid standalone P-256 private PKCS8
without SEC1 Q (including SunEC's own output). The reviewed dependency supplement
pins Bouncy Castle bcprov-jdk18on 1.86, SHA-256
`2af190b300cbb0b35e248ccf5f4a06b6072030aeb3da7a98ec73abe5b4cb371f`.
Only maintained public `HKDFBytesGenerator` and `FixedPointCombMultiplier` APIs
supply these operations. Bounded reflection preserves JDK-only noncrypto target
builds; a production crypto SDK always ships the pinned dependency. Missing or
incompatible APIs are host faults. No provider is globally registered and no JVM
crypto defaults are changed. Private scalar bounds and present-Q consistency
are checked, and absent Q is derived with maintained native crypto. SPKI,
PKCS8, RSA PKCS1 private/public and RSA/P-256 certificate imports are supported.

Native key aliases share one material record. Close immediately rejects new
acquisitions, waits for existing actual native snapshots, and clears key roots.
Staged late/canceled generation is discarded before retirement. Mutable worker
holders drop input/lease roots; a native observer ACKs only after worker
termination. Direct deferred/go external calls use the existing Task-style
AwaitFrame protocol. Receiver-aware capability wrappers also support bound
native method values and method expressions without changing shared IR/effects.

`java.net.http.HttpClient` uses trusted JVM TLS, HTTP/1.1 and NEVER redirects.
Dedicated reader/client leases close all registered native/wrapped readers and
await client and stop-helper termination before worker ACK. Request cancellation
and deadlines stop actual native work. Declared read failures remain transport
errors when close repeats that exact failure; a distinct cleanup failure is a
host fault after release. The package-private identity body-wrapper seam exists
only for isolated reflected resource-fault tests, with no SDK API/options.

JVM ProxySelector/system proxy configuration applies rather than Go environment
proxy variables. Request header values are ASCII-only: JDK 21 otherwise silently
writes `?` for U+00FF, so non-ASCII input is rejected before contact. Response
header obs-text is preserved as Latin-1 bytes. Canonical names sort by name and
duplicate values preserve native order. The exposed header limit is 64 KiB and
decoded body limit is checked after automatic gzip; native parser ceilings may
reject earlier. Native whitespace/HTTP/1 parser observations are documented
rather than advertised as byte-identical Go transport behavior.
