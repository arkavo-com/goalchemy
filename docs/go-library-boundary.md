# Bounded importing Go library boundary

The Go emitter accepts source library roots and exports real lowered IR through
native context-aware functions. Library parameters/results admit bool, integer,
string, arrays/slices and public struct value trees. Source context parameters
map to one native context.Context. Source final error results are preserved;
exports without a source error gain a boundary error result. Pointers to
exported root structs cross as [handles](library-handles.md); other pointer,
map, channel, callback and opaque-key public parameters/results are rejected.
This deliberately avoids arbitrary source type/method ABI parity.

Each wrapper snapshots value inputs before cancellation-aware reservation and
runs initialization plus its export in a fresh host-clock scheduler. Source
mutable globals are zeroed before and after this owner. Reservations serialize
all exports of this emitted package; queued cancellation cannot reset another
caller. Init runs once per fresh runtime, rather than repeatedly against
persistent mutable globals. Source panic/deadlock and host implementation fault
return typed LibraryError after retirement; existing executable behavior and
other-target library/adapter gates remain unchanged. Calls on
[handles](library-handles.md) use their instance's globals instead. See [package variables in libraries](usage.md#package-variables-in-libraries).

The Go library runtime is injected only into library output. It installs an
owner-only native-context observation hook and a wake-only watcher; native
threads never mutate source contexts/frames. Native HTTP/provider contexts keep
the earliest inherited deadline. Retirement awaits host workers and cleanup,
closes the scheduler-scoped generated/imported crypto key leases, resets globals,
then releases the reservation. A submitted crypto acquisition may finish before
cancellation releases its key. Collector roots and callbacks do not survive
retirement. Public successful value trees are independently copied.

The reusable lib/callback.Request capability explicitly declares suspension. A
source closure calling it causes effect lowering to convert the configured
provider dynamic call to a pause; tests/audit must prove the actual dynamic call
and bridge pause, rather than infer that a callback is async. Request resolves
its name within the active scheduler's copied registry; no credentials/provider
slot is shared across calls. Native callbacks settle owned copied bytes through
host mailboxes. A stop-hook panic is withheld until true provider settlement
acknowledges resource release, then becomes a host fault. Submission that panics
before returning a hook must clean any unreported acquisitions itself. Providers
must honor cancellation and terminal resource acknowledgment; the boundary does
not force cleanup of undocumented external resources. Only Go implements this
capability; all missing other-target capability mappings remain rejected.

Generated modules are locally importable as module goalchemyout, package
generated. Output bundles standard-library-only crypto/encoding/HTTP/clock
capabilities and runtime; it never imports original source SDK implementations.
The SDK build script adds a small native token wire adapter. See the shared
[SDK public boundary and build instructions](../../sdk/docs/generated-go-library.md)
for configuration, metadata, int64/error fields and precise ownership guarantees.

Tests: `tests/integration/go_library_test.go` compiles actual exports and creates
an independent importing consumer with race detection. `targets/go/runtime/
library_test.go` verifies key retirement and constructor/init failures. Native
`lib/callback/callback_test.go` checks scoped registration, rejection, copying and
cancellation. These prerequisites do not establish full Go profile delivery or
all-target library support.

Successful value/error ownership conversion runs inside the active owner before
source-global reset, runtime retirement or reservation release. This includes
slices backed by source-global fixed arrays. Public struct value-tree errors
retain their concrete type with copied slice fields; immutable native
errors.New/context sentinel identity is preserved. Arbitrary error layouts
containing private fields or opaque/pointer/interface state are outside the
bounded export ABI and become LibraryError with Kind `unsupported_error`.
The shared SDK façade always converts declared failures to its supported public
Failure record before reaching this boundary.

Logging: when a library uses `std/log/slog`, the output bundles the native
`goalchemyout/cap/log` package. Hosts call `log.SetHandler(handler, level)`,
or pass `log.Slog(logger)` to route records to a `*slog.Logger`. See
[the logging section of the usage guide](usage.md).
