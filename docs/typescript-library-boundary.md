# TypeScript value libraries

The cooperative TypeScript target emits importable ESM value libraries from Go
packages without `main`. Exported functions return Promises; source execution,
initialization, suspension, cancellation and cleanup use the generated runtime.
The output bundles the reachable runtime, emits declarations, and has no production
package dependencies. Its separate Node entry uses the standard `node:zlib.crc32`
API; the portable browser entry has no Node imports. Other unsupported target
library and capability gates remain in force.

Compile with `goalchemy compile -gate cooperative -target typescript -out DIR
./package`, then run `tsc -p DIR`. Install and import the emitted package to select
its Node or browser entry automatically. Node callers can also import
`DIR/dist/node.js` directly. Direct `DIR/dist/main.js` imports select the portable
entry and CRC fallback even in Node. Compilation needs TypeScript with
`rewriteRelativeImportExtensions` (verified with 6.0.3 and 7.0.2). Node requires 22.6 or
later with WebCrypto, fetch and AbortSignal; current evidence uses Node24.15.0
and Chromium147.0.7727.15. Browser applications bundle the ESM entry normally.

## Values and initialization

This boundary supports bounded value trees: structs, arrays, slices and scalar
values. Pointers to exported root structs cross as persistent
[handles](library-handles.md). Other pointers, opaque key handles, interfaces as
arguments and variadic exports remain unsupported. Struct
properties use their exported source spelling. Omitted properties have source
zero values. Source int and int64 use signed64 bigint; narrower integers use
checked numbers. Conversion rejects rounding, overflow and malformed values.
Byte slices use Uint8Array or null; null preserves source nil, and zero-length
Uint8Array preserves present empty. Binary data never uses UTF-8 conversion.
Raw source Go strings use JS strings whose code units are bytes0..255.

Each call snapshots its arguments synchronously before entering a serialized
reservation queue. Typed-array elements are copied into a plain Uint8Array,
including Buffer and subclasses whose slice returns a view. Nonbyte arrays
copy indexed elements into plain arrays without invoking caller map/iterator
hooks; nested values are copied recursively before queueing. Outputs and public
structured source errors are copied while the source owner is live. Each call
freshly initializes its globals; cleanup precedes reset and release. Retained
fixed-global-array slices and errors remain valid across overlapping calls.
Calls on [handles](library-handles.md) use their instance's globals instead. See [package variables in libraries](usage.md#package-variables-in-libraries).

The optional final CallOptions has AbortSignal and a copied, scoped callback
registry. Queued cancellation removes that reservation. Active cancellation
requests native stop and waits for actual resources to settle. Neither a timeout
nor cancellation invents completion or ACK. Native callbacks publish owned wires;
the driver alone decodes values and resumes source frames. An uncancellable
WebCrypto operation must finish and discard any late acquired key before owner
retirement. A provider that never settles can keep its operation pending.

LibraryError distinguishes invalid_argument, canceled, source, source_panic and
host_fault. Supported public source error structs appear as owned `fields`.
Unexpected submission/cleanup faults become host_fault without leaking native
messages. This reusable driver does not exit the importing process.

## Logging

`std/log/slog` records reach the host through `setLogHandler(handler, level)`,
exported with the `LogRecord` and `LogHandler` types. The handler receives
decoded strings, `[key, value]` attribute pairs and the rendered `text`. Passing
`null` restores the default: records at warn and above are written to the
runtime's stderr, or to `console.error` in browsers and other portable hosts.
See [the logging section of the usage guide](usage.md).

## Providers and native keys

Callback receives `(signal, copiedRequest, settle)` and returns a Promise, a
nonblocking cancellation hook, or void. Explicit settle snapshots reply bytes
synchronously at publication; first settlement wins and late/duplicate replies
cannot revive retired state. A Promise reply is snapshotted when fulfillment is
observed, so its provider must retain ownership until then. Provider rejection
is a declared source error; a submission throw is a host fault. A cancellation
hook throw still waits for terminal resource settlement before returning a fault.

Native keys use private owned native state with immutable logical wire IDs.
Driver decoding transfers ownership once. WebCrypto keys are lazily imported
for their actual uses: RSA signing and SHA1 OAEP, or P256 signing and ECDH.
Close invalidates all aliases immediately, waits for every acquired snapshot,
drops retained native state, and only then resumes the parked source frame.
Browser native memory release follows the engine's garbage collection; no
claim of deterministic native memory zeroization is made. The shared Close
contract is may-suspend; generated Go uses a Task wrapper and NativeFrame for
deferred/go calls while native Go Close retains its original semantics.

## Portable transport observations

Node entries (executables and the package's `node` export) install a
`node:http`/`node:https` adapter with Go's transport semantics: it sends
`Accept-Encoding: gzip` unless the caller sets a non-empty `Accept-Encoding` or
`Range`, decodes `Content-Encoding: gzip` with `node:zlib` (multi-member streams;
truncated or corrupt streams are transport errors), applies the body limit to
decoded bytes, removes the decoded `Content-Encoding`/`Content-Length` headers,
and leaves responses without a body (`Content-Length: 0`, 204, 304, 1xx)
untouched. A caller's `Accept-Encoding` returns the raw bytes, as in Go.

Browsers and other portable entries use native fetch with manual redirects and
omitted ambient credentials. GET/POST input is bounded to64MiB, URL8192 bytes, header list32768
entries/64KiB and timeout1..300000ms; parent cancellation/deadline can shorten
work. Inputs reject framing/proxy restrictions and the Fetch Standard's forbidden
request headers except Accept-Encoding, including Proxy-/Sec- prefixes and method overrides containing
CONNECT/TRACE/TRACK, before contacting the network. Authorization, DPoP,
Content-Type and Connect-Protocol-Version remain supported. Native Headers trims
leading/trailing HTTP whitespace and merges repeated fields.

Raw Go URL bytes are strictly decoded as UTF-8 before native URL construction.
Malformed UTF-8, controls/space/backslash/fragment, malformed percent escapes,
credentials, non-ASCII or percent-escaped authority, noncanonical numeric hosts
or ports, and dot-segment normalization ambiguity are rejected. Unicode path
and query text is percent-encoded by native URL. Hosts may use explicit ASCII
punycode. Default ports80/443 and case-insensitive ASCII authorities are accepted.
Shared source retains all trusted-route decisions.

Node manual redirects expose their status; browser manual redirects produce
opaque redirects and a declared transport error. Browser opaque/CORS failures
also reject safely. Response headers are the native exposed merged header list,
sorted with canonical names; browser CORS filtering and Set-Cookie restrictions
apply. The64KiB limit covers exposed headers, not inaccessible wire headers.
The response body limit covers decoded stream bytes. Browser fetch negotiates
and decodes compression itself; for `Content-Encoding: gzip` responses with a
body, the adapter removes `Content-Encoding`/`Content-Length` as Go does. Browsers
forbid setting `Accept-Encoding`, so a caller that sets it gets
`http: Accept-Encoding is not supported in browsers`. A truncated gzip payload
inside an otherwise complete HTTP response may not error, Chromium decodes only
the first member of a multi-member gzip body, and `Range` does not turn off
decoding; these differ from Go and cannot be controlled through fetch. Native TLS verification remains enabled. Reader
cancel/release and alarms settle before ACK. An expected AbortError during
cancel, or the identical stored stream error already classified as a failed
read, preserves the declared transport or cancellation result. A distinct
cleanup error remains host_fault. Reader locks are released before completion
in both cases. Connection pooling is managed by the host, independently of a
retired request.

The policy follows the [Fetch Standard](https://fetch.spec.whatwg.org/#forbidden-request-header)
reviewed on2026-10-02 (standard updated2026-09-21). Production cryptography uses
WebCrypto RSA2048 OAEP SHA1/MGF1SHA1 empty label, RS256, P256 ES256 JOSE64,
ECDH32, SHA256, HMAC native verification, HKDFSHA256 and AES256GCM. PEM imports
support SPKI/PKCS8, RSA PKCS1 and certificate SPKI extraction through bounded DER
framing. Native algorithms validate key material; no cryptographic math is
implemented in the framing code.

Tests use TypeScript7.0.2 (Apache2), esbuild0.25.12 (MIT), and Playwright1.58.2
(Apache2). These are test/build tooling only; emitted production needs no npm
crypto, HTTP, Node polyfill or reference SDK dependency.

## Files

`std/os` uses the `node:fs` adapter that the package's `node` export (`dist/node.js`)
installs alongside the CRC adapter. The browser and default exports leave it out,
so `ReadFile` and `WriteFile` fail with `operation not supported` there and the
browser graph keeps no Node dependency.
