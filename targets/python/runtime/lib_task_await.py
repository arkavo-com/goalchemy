from .lib_task_start import copy_future_panic
from .task_spawn import sched


def lib_task_await(t, future):
    if future is None:
        sched().block(t)
        return
    future.observed = True
    sched().unawaited.pop(future, None)
    if future.done:
        if future.panic is not None:
            raise copy_future_panic(future.panic)
        t.rv = [future.results]
        return
    future.waiters.append(t)
    sched().block(t)
