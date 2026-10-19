/* Bounded sequential C runtime. No value survives a call to micro_run. */
#ifndef GX_MICRO_H
#define GX_MICRO_H
#include <stddef.h>
#include <stdint.h>

#ifndef GX_MICRO_ARENA_BYTES
#define GX_MICRO_ARENA_BYTES (64 * 1024)
#endif
#ifndef GX_MICRO_MAX_DEPTH
#define GX_MICRO_MAX_DEPTH 16
#endif
typedef void (*gx_micro_output)(void *state, const void *bytes, size_t length);
typedef struct gx_micro_stats {
    size_t arena_capacity_bytes;
    size_t arena_peak_bytes; /* includes alignment padding */
    size_t allocations;
    size_t peak_source_depth;
    const char *fault; /* static text, valid after return; NULL on success/panic */
} gx_micro_stats;
enum { GX_MICRO_OK, GX_MICRO_INVALID, GX_MICRO_PANIC, GX_MICRO_FAULT, GX_MICRO_BUSY };
/* Arena must be aligned to max_align_t. Entry is globally serialized, including
 * callbacks. BUSY/INVALID leave stats untouched. Output bytes are borrowed only
 * during the callback. Callbacks must return normally; they may not longjmp.
 * Source globals initialize afresh each time. Panics run defers; faults (OOM,
 * depth limit) abort directly to this boundary without allocating or unwinding.
 * This limit is NOT a C stack bound; size the host task stack separately. */
int goalchemy_micro_run(void *arena, size_t capacity, gx_micro_output output,
                       void *state, gx_micro_stats *stats);
int gx_micro_run(void (*init)(void), void (*entry)(void), void *arena,
                 size_t capacity, gx_micro_output output, void *state,
                 gx_micro_stats *stats);
void *gx_micro_alloc(size_t n);
#endif
