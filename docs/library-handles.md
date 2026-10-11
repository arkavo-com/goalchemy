# Library handles

Exported functions and methods can return and accept pointers to exported
struct types of the root package. The host receives an opaque handle with the
type's exported methods, on every target.

```go
package counter

type Counter struct{ n int }

func New() *Counter                      { return &Counter{} }
func (c *Counter) Add(d int) int         { c.n += d; return c.n }
func (c *Counter) Close() error          { return nil } // optional
```

| Target | Handle | Release |
|---|---|---|
| Go | `*counter.Counter` with methods taking `ctx` and returning `error` | `Close() error` |
| TypeScript | `class Counter` with Promise-returning methods | `close()`, `await using` (`Symbol.asyncDispose`) |
| Python | `class Counter` with methods returning a library operation | `close()`, `with` and `async with` |
| Java | `Generated.Counter` with methods returning `Library.Operation` | `close()` (`AutoCloseable`), `closeAsync()` |
| C# | `GoProgram.Counter` with methods returning `Library.Operation` | `Dispose()`, `DisposeAsync()`, `CloseAsync()` |
| Rust | `Counter` (cloneable, `Eq` by identity) with methods returning `Operation` | `close()`; dropping the last clone releases |
| C | `gxc_handle *` in `GXC_HANDLE` values; methods are `goalchemy_invoke("Counter.Add", ...)` with the receiver first | `gxc_handle_close`, `gxc_handle_free` |
| Swift | `final class Counter` with methods returning `Operation` | `close()`; `deinit` releases |

Rust renames a method named `Self` to `Self_`. Error kinds for closed,
poisoned and mixed handles are `closed`, `poisoned` and `instance_mismatch`
(Rust `ErrorKind::Closed`, `Poisoned`, `InstanceMismatch`; C kinds 7, 8, 9).

## What crosses

- A handle type is an exported root struct `T` whose pointer `*T` appears
  directly in an export signature, or in the signature of an exposed method of
  another handle.
- Exported methods of `T` and `*T` are exposed when every parameter and result
  is a copied value, a handle, a leading `context.Context` or a final `error`.
  Other methods are not exposed. A method named `Close` must be `Close() error`.
- Handles are direct parameters and results only, not fields of structs or
  elements of slices. A nil pointer crosses as the host's null.

## Instances

The first call that returns a handle without receiving one creates an
instance. The instance owns its source globals, runs package initialization
once, and keeps native resources such as crypto keys until it retires. Handles
created by later calls on the instance (`c.Child()`) join it. Free functions
without handle arguments still run in a fresh owner with fresh globals, and
the compiler warns when they write a package variable (`GCW001`); see
[package variables in libraries](usage.md#package-variables-in-libraries).

Handles from different instances cannot be mixed in one call: that fails with
`instance_mismatch`. Returning the same pointer twice returns the same host
object while that object is alive.

## Concurrency

Calls are serialized across the whole library, including calls on different
instances: each call loads its instance's globals, runs, and saves them. Calls
from several host threads are safe. Source `sync.Mutex` keeps Go semantics
inside a call.

Goroutines still running when a handle call returns are abandoned with a
warning. A host callback that calls back into the library deadlocks, as for
free functions.

## Close and failures

- `Close` fails calls queued on the handle with `closed`, cancels its active
  call (kind `canceled`), runs the source `Close` if there is one, then releases
  the handle. Later calls fail with `closed`; `Close` is idempotent. A source
  `Close` error is returned after the handle is released.
- The instance retires when its last handle is released: its globals and native
  resources are dropped.
- A handle collected without `Close` is released by a finalizer (Rust `Drop`,
  Swift `deinit`), which never runs source code and logs a warning. C has no
  finalizer: `gxc_handle_free` releases an open handle without Close.
- A source panic, fatal error or host fault during an instance call poisons the
  instance: later calls fail with `poisoned`, and `Close` only releases.

Rust and C run each instance's calls on owner threads that keep its heap;
the other targets swap the instance's globals in and out around each call.

Warnings go to standard error. Hosts can replace the sink: Go `rt.LibraryWarn`,
TypeScript `setLibraryWarn` in `rt/runtime/library.js`, Python
`rt.set_library_warn`, Java `Library.warn`, C# `Library.Warn`, Rust
`set_library_warn`, C `gxc_set_warning_handler` and Swift
`setGoalchemyWarningHandler`.

## Host streams

Exported functions and handle methods can take `std/io` interfaces as
parameters: `io.Reader`, `io.Writer`, `io.ReadCloser`, `io.WriteCloser`,
`io.ReadSeeker`, `io.ReadSeekCloser`, `io.WriteSeeker` and `io.ReadWriter`.
The host passes its native stream.

| Target | Host passes |
|---|---|
| Go | any value implementing the matching standard `io` interface |
| TypeScript | an object with `read(size)` (null at end), `write(data)`, `seek(offset, whence)` and `close()` as needed; Node `Readable`/`Writable`, web `ReadableStream`/`WritableStream`, `AsyncIterable<Uint8Array>`, `Blob` (seekable) or `Uint8Array` (seekable) |
| Python | a file-like object (`readinto` or `read`, `write`, `seek`, `close`) or `bytes` |
| Java | `InputStream`, `OutputStream`, a `ReadableByteChannel`/`WritableByteChannel` (seekable when a `SeekableByteChannel`) or `byte[]` (seekable); the parameter type is `Object` |
| C# | a `System.IO.Stream` (its `CanRead`/`CanWrite`/`CanSeek` must match) or `byte[]` (seekable); the parameter type is `object` |
| Rust | `Stream::reader`, `writer`, `read_seeker`, `write_seeker` or `bytes`, with `with_close` for a Close action |
| C | a `gxc_stream` of callbacks in a `GXC_STREAM` value; error kind 10 is `reentrant` |
| Swift | `GoalchemyStream` with `read`, `write`, `seek` and `close` closures, or built from bytes, an `InputStream` or an `OutputStream` |

- Each Read, Write, Seek and Close is a host operation through the callback
  mailbox: it suspends the calling source task, and reads and writes copy
  their buffers. One operation moves at most 128 KiB; `io.Copy` and
  `io.ReadAll` loop as usual.
- Host end of stream (an empty read, `null`, or `io.EOF`) becomes `io.EOF`.
  A host error becomes a source error carrying its message.
- A stream passed to an instance stays usable by later calls on the instance
  until it retires, so a handle can keep a reader.
- A host stream method that calls back into the library fails that call with
  kind `reentrant` instead of deadlocking. Go detects this per goroutine,
  TypeScript through `AsyncLocalStorage` in Node (the portable browser entry
  cannot detect it) and the other targets per thread.
- Stream operations use a background context: canceling a call does not
  interrupt a host read already in progress.

The adapter is the source package `std/io/hostio`, which the compiler adds to
libraries whose exports take `std/io` interfaces.

## Source streams

Exported functions and handle methods can return the same `std/io`
interfaces: `io.Reader`, `io.Writer`, `io.ReadCloser`, `io.WriteCloser`,
`io.ReadSeeker`, `io.ReadSeekCloser`, `io.WriteSeeker` and `io.ReadWriter`.
The host receives a native stream. A handle type whose pointer has
`Read([]byte) (int, error)` or `Write([]byte) (int, error)` (and optionally
`Seek(int64, int) (int64, error)`) is also usable as a native stream.

| Target | Returned stream | Handle that is a reader or writer |
|---|---|---|
| Go | a value of the standard `io` interface type; it also has `Close` (and `WriteTo` when it reads) | `h.IO()`, an `io` interface with `Close` |
| TypeScript | `SourceStream`, typed as `SourceReader`, `SourceWriter`, `SourceSeeker` and `SourceCloser` as the kind allows: `read(size)` (null at end), `write(data)`, `seek(offset, whence)`, `stream()` (a web `ReadableStream`), `writable()` (a web `WritableStream`), `for await`, `close()` and `await using` | `h.io()`, `h.stream()`, `h.writable()` and `for await (const chunk of h)` |
| Python | `rt.SourceStream`, an unbuffered binary file (`io.RawIOBase`): `read`, `readinto`, `readall`, `write`, `seek`, `tell`, `close` and `with` | `h.as_file()` |
| Java | `Library.SourceStream` (`AutoCloseable`): `asInputStream()`, `asOutputStream()`, blocking `read`/`write`/`seek`, and `readAsync`/`writeAsync`/`seekAsync`/`closeAsync` returning `Library.Operation` | `h.io()`, `h.asInputStream()`, `h.asOutputStream()` |
| C# | `Library.SourceStream`, a `System.IO.Stream` (`CanRead`/`CanWrite`/`CanSeek` follow the kind, `ReadAsync`/`WriteAsync`, `Dispose`/`DisposeAsync`, `CloseAsync()`) | `h.AsStream()` |
| Rust | `SourceStream` (cloneable): `std::io::Read`, `Write` and `Seek`, `read_chunk(n)` and `close()` returning `Operation` | `h.io()` |
| C | a `gxc_handle` of type `"io.Reader"`, `"io.ReadCloser"` and so on: `gxc_stream_read`, `gxc_stream_write`, `gxc_stream_seek`, `gxc_handle_close` and `gxc_handle_free` | the same functions on the handle |
| Swift | `GoalchemySourceStream`: `read(_:)` (nil at end), `read(into:)`, `write(_:)`, `seek(_:_:)`, `close()`, `for try await chunk in stream` (`Data` chunks); `deinit` releases | `h.io()` |

- A returned stream is a handle of the call's instance, with the same rules:
  calls on it are serialized with the instance's other calls, a nil interface
  crosses as the host's null, `Close` runs the source `Close` when the kind
  has one and then releases the handle, later operations fail with `closed`,
  and a stream collected without `Close` is released with a warning. Each
  returned stream is a new host object.
- A handle's stream view shares the handle: closing the view closes the
  handle, and the view does not need its own Close.
- Each read, write or seek is one library call through the `std/io/hostio`
  functions `ReadView`, `WriteView` and `SeekView`. Buffers are copied; one
  read returns at most 1 MiB, and writes are split into 1 MiB calls. A read
  returns data, end of stream or an error: source `io.EOF` becomes the host's
  end of stream, and other source errors fail with kind `source`.
- Operations a kind does not have fail with kind `unsupported` (Rust
  `ErrorKind::Unsupported` or `io::ErrorKind::Unsupported`, C kind 11, C#
  `NotSupportedException`). TypeScript types, Python
  `readable()`/`writable()`/`seekable()`, Java `canRead()`, C# `CanRead` and
  so on reflect the kind.
- Java `InputStream`/`OutputStream` and C# `Stream` operations report
  failures as `IOException` whose cause is the library failure, and closing
  them closes the handle. Rust `Read`/`Write`/`Seek` errors wrap the
  `LibraryError`.
- In TypeScript, ending or canceling `stream()` does not close the handle;
  closing `writable()` does. Use `Readable.from(stream)` or
  `Readable.fromWeb(stream.stream())` for a Node `Readable`.
- Python, Rust, C and Swift stream operations, and the Java and C#
  `InputStream`/`OutputStream`/`Stream` methods, block until their call
  completes. Wrap Python streams in `io.BufferedReader` or
  `io.BufferedWriter` for buffering.
- A Swift view does not own its handle: its `deinit` does not release the
  handle. A Rust view holds a clone of the handle, so the handle is released
  when the last clone, view or not, is dropped.

Copying 256 MiB host → source → host (a host stream passed to an export that
returns it as an `io.ReadCloser`) runs with bounded memory; the integration
tests log the rate per target.
