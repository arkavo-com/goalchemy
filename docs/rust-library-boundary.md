# Rust owned value-library boundary

Rust library emission generates Cargo `src/lib.rs`, typed owned value exports,
and `Operation<T>: Future<Output = Result<T, LibraryError>>`. Native string
values in the generic boundary are `Vec<u8>`, preserving arbitrary Go string
bytes; the SDK facade accepts Rust UTF-8 Strings. Native integer types retain
the exact source width. Public struct trees, primitive arrays/slices and final
error results are supported. Contexts are supplied by the boundary; borrowed
pointers, native keys, arbitrary callbacks, channels and maps are rejected with
GCE007. Native token callbacks use the owned provider registry instead.

Submission moves recursively owned native arguments and callback snapshots into
one call. Calls serialize source initialization, execution and retirement. The
coordinator acquires a process-local entry reservation before starting a dedicated
16 MiB source owner; it holds the reservation through owner join and TLS teardown.
Only native owned Rust values/errors leave the owner. No source Rc/RefCell/V/H,
descriptor or frame gains Send/Sync or crosses a native thread.

Each acquired call initializes fresh source globals and packages once. The
owner constructs source contexts and frames, drives native monotonic scheduling,
formats source failures and panics, and detaches outputs. Cancellation polls a
native flag on that owner and cancels source contexts. Native pending operations
retain the accepted separate publication/cleanup-ACK protocol. Retirement drains
true ACKs, clears provider registrations and native key material, and detaches
source roots before source-thread teardown. Publication follows coordinator join.
Calls on [handles](library-handles.md) use their instance's globals instead. See [package variables in libraries](usage.md#package-variables-in-libraries).

Queued cancellation releases owned arguments without waiting for an active
call. Active cancellation and native faults settle after resource release.
Committed success is final. Operation Drop requests cancellation only while
unsettled; completed calls sharing CallOptions do not cancel each other. Drop
alone is not a cleanup acknowledgment. Native continuations/wakers execute after
queue/entry reservations are released. Providers run outside source locks and
must return only after native resources release. A provider must not synchronously
wait for a reentrant call to this serialized library.

Unrecovered source panic, source fatal, adapter fault, cancellation and declared
SDK errors remain distinct native kinds. Structured error fields and arbitrary
source diagnostic bytes detach before retirement. Library entry never invokes
run_large or replaces a process panic hook, thread policy or TLS crypto provider.
Rust panic=unwind and the bounded generated-source stack checks are prerequisites;
native stack exhaustion, abort/OOM and catastrophic native failures are outside
managed cleanup guarantees.

IEEE CRC32 uses pinned `crc32fast` 1.5.2 with runtime CPU detection enabled.
The adapter hashes native byte-slice views without copying or mutating input.
See [CRC32 host mappings](checksum.md).

Production crypto uses pinned vendored OpenSSL through maintained openssl APIs.
Opaque registry IDs are tagged with the source owner; aliases share Close state.
Crypto runs synchronously on the owner and releases native snapshots before the
source continuation. Close invalidates registrations and follows held snapshots;
retirement releases any remaining keys. Native HTTP snapshots bytes/headers into
owned worker arguments and uses reqwest/rustls with dedicated Tokio runtimes,
verified roots/hostnames, no redirects/retries, bounds and cancellation. Runtime,
response and socket release precede ACK. Providers receive owned bytes and a
native cancellation flag; no callback drives source frames.

The generic std-only virtual executable entry and host-conformance behavior stay
available. Native capability programs select Cargo and pinned native dependencies;
the native Cargo harness runs capability cases, while structural std-only host
and byte probes exclude production modules. SDK package/import and real-KAS
receipts are documented in [Rust SDK delivery](../../sdk/docs/generated-rust-library.md).

Native slice and array inputs check their length against the source u32
collection descriptor before converting elements or allocating source storage.
An unrepresentable length returns InvalidArgument, including nested collections
and zero-sized records. The check retains every descriptor-representable length;
existing operation-specific bounds still apply. The independent native fixture
rejects a valid zero-sized record vector of length u32::MAX+1 on 64-bit hosts
and verifies ordinary byte/recursive-value operations recover afterward.

Imported native private keys are checked by maintained OpenSSL RSA/EC validity
APIs before public extraction. RSA public exponents are positive odd integers
at least three and fit the native Go integer width. Encoded private D/Q
inconsistency is rejected while omitted-Q valid PKCS8 remains supported. Native
Go's decoder normalization of some D/CRT and encoded-Q representations is
recorded in the SDK delivery document; no Go/reference behavior is changed.

Pure value libraries link the native crypto cleanup dependency independently
of source capability use. Native-only module reexports follow feature guards,
so pure library exports also build with disabled default features. Std-only
executables/capabilities remain available; default native SDK bindings are
unchanged. An independent pure byte Echo consumer verifies both feature modes.

## Logging

`std/log/slog` records reach the host through the crate's
`set_log_handler(Some(handler), level)`, where the handler is a `LogHandler`
(`Arc<dyn Fn(&LogRecord) + Send + Sync>`). Handler panics are discarded. `None`
restores standard error at warn and above. See [the logging section of the usage guide](usage.md).
