#include "gx.h"

gx_Panic *gx_copy_future_panic(gx_Panic *p) {
    if (!p) return NULL;
    gx_Panic *copy = gx_new_panic(p->value);
    copy->prev = gx_copy_future_panic(p->prev);
    return copy;
}
static int future_results(gx_Frame *f, gx_V *out) { out[0] = f->l[2]; return 1; }
static void future_step(gx_Task *t, gx_Frame *f) {
    if (f->pc == 0) {
        f->pc = 1;
        gx_V result = gx_callv(gx_unboxed(f->l[0]), 0, NULL);
        if (result.t == GX_FRAME) { gx_call(t, result); return; }
        f->l[2] = result;
    } else f->l[2] = gx_rv(t, 0);
    gx_ret(t, f);
}
static void future_complete(gx_Task *t, gx_Frame *f, gx_Panic *panic) {
    (void)t;
    gx_Future *future = f->l[1].u.p;
    future->done = true;
    future->panic = panic;
    if (!panic) future->results = f->l[2];
    if (panic && !future->observed) {
        gx_Unawaited *entry = GC_MALLOC(sizeof *entry);
        entry->key = future;
        entry->panic = panic;
        if (gx_sched->unawaited_tail) gx_sched->unawaited_tail->next = entry;
        else gx_sched->unawaited = entry;
        gx_sched->unawaited_tail = entry;
    }
    for (gx_FutureWaiter *w = future->head; w; w = w->next) {
        gx_set_rv(w->task, 1, &future->results);
        w->task->resume_panic = gx_copy_future_panic(panic);
        gx_ready(w->task);
    }
    future->head = future->tail = NULL;
}
gx_V gx_lib_task_start(gx_V function) {
    gx_Future *future = GC_MALLOC(sizeof *future);
    gx_Frame *child = gx_new_frame(3, future_step, future_results);
    child->l[0] = function;
    child->l[1] = gx_obj(future);
    child->completion = future_complete;
    gx_spawn(gx_vframe(child));
    return gx_obj(future);
}
