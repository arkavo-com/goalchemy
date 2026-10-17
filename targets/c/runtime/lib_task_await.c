#include "gx.h"

void gx_lib_task_await(gx_Task *t, gx_V value) {
    if (gx_is_nil(value)) { gx_block(t); return; }
    gx_Future *future = value.u.p;
    future->observed = true;
    for (gx_Unawaited *p = gx_sched->unawaited; p; p = p->next)
        if (p->key == future) { p->key = NULL; p->panic = NULL; }
    if (future->done) {
        if (future->panic) gx_raise(gx_copy_future_panic(future->panic));
        gx_set_rv(t, 1, &future->results);
        return;
    }
    gx_FutureWaiter *waiter = GC_MALLOC(sizeof *waiter);
    waiter->task = t;
    if (future->tail) future->tail->next = waiter;
    else future->head = waiter;
    future->tail = waiter;
    gx_block(t);
}
