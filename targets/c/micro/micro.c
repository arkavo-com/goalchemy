#include "gx.h"
#include <stdatomic.h>
#include <stdlib.h>

/* All state is owned by the one serialized invocation. The arena has no
 * allocation metadata, collection pauses, reallocations or per-object frees. */
static atomic_flag busy = ATOMIC_FLAG_INIT;
static uint8_t *storage;
static size_t used;
static gx_micro_stats metrics;
static gx_micro_output sink;
static void *sink_state;
static jmp_buf escape;
static gx_Task task;
size_t gx_source_depth;

gx_Task *gx_cur_task(void) { return &task; }
void gx_stderr(const void *b, size_t n) { if (sink) sink(sink_state, b, n); }

_Noreturn void gx_fault(const char *message) {
    metrics.fault = message;
    longjmp(escape, GX_MICRO_FAULT);
}

void *gx_micro_alloc(size_t n) {
    const size_t align = _Alignof(max_align_t);
    const size_t padding = (align - used % align) % align;
    if (!n) n = 1;
    size_t remaining = metrics.arena_capacity_bytes - used;
    if (padding > remaining || n > remaining - padding)
        gx_fault("arena exhausted");
    uint8_t *p = storage + used + padding;
    used += padding + n;
    memset(p, 0, n); /* GC_MALLOC's zero-initialization contract */
    metrics.arena_peak_bytes = used;
    metrics.allocations++;
    return p;
}

void gx_source_enter(void) {
    if (gx_source_depth >= GX_MICRO_MAX_DEPTH) gx_fault("source call depth exceeded");
    gx_source_depth++;
    if (gx_source_depth > metrics.peak_source_depth) metrics.peak_source_depth = gx_source_depth;
}
void gx_source_leave(void) { gx_source_depth--; }

_Noreturn void gx_report_panic(gx_Panic *p) {
    gx_Buf b = gx_panic_report(p);
    gx_stderr(b.b, b.n);
    longjmp(escape, GX_MICRO_PANIC);
}

int gx_micro_run(void (*init)(void), void (*entry)(void), void *arena,
                 size_t capacity, gx_micro_output output, void *state,
                 gx_micro_stats *stats) {
    if (!arena || !capacity || capacity > UINT32_MAX / 2 ||
        (uintptr_t)arena % _Alignof(max_align_t)) return GX_MICRO_INVALID;
    if (atomic_flag_test_and_set(&busy)) return GX_MICRO_BUSY;
    storage = arena; used = 0;
    metrics = (gx_micro_stats){.arena_capacity_bytes = capacity};
    sink = output; sink_state = state;
    task = (gx_Task){0};
    gx_handler = NULL; gx_thrown = NULL; gx_source_depth = 0;
    int status = setjmp(escape);
    if (!status) {
        gx_Handler handler;
        if (GX_TRY(handler)) gx_report_panic(gx_thrown);
        init(); entry();
        GX_END(handler);
    }
    if (stats) *stats = metrics;
    gx_handler = NULL; gx_thrown = NULL; gx_source_depth = 0;
    task = (gx_Task){0};
    storage = NULL; sink = NULL; sink_state = NULL;
    atomic_flag_clear(&busy);
    return status;
}
