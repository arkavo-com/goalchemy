from ..types.panic import GoPanic, nil_deref
from .task_spawn import Frame, call, ret, sched, spawn


class FutureState:
    def __init__(self):
        self.done = self.observed = False
        self.results = self.panic = None
        self.waiters = []


def copy_future_panic(p):
    if p is None:
        return None
    result = GoPanic(p.value)
    result.prev = copy_future_panic(p.prev)
    return result


class _FutureChild(Frame):
    def __init__(self, fn, future):
        super().__init__()
        self.fn = fn
        self.future = future
        self.values = []
        self.completion = self.complete

    def results(self):
        return self.values

    def step(self, t):
        if self.pc == 0:
            self.pc = 1
            if self.fn is None:
                raise nil_deref()
            result = self.fn()
            if isinstance(result, Frame):
                call(t, result)
                return
            self.values = [result]
        else:
            self.values = t.rv
        ret(t, self)

    def complete(self, t, results, p):
        future = self.future
        future.done, future.panic = True, p
        if p is None:
            future.results = results[0]
        if p is not None and not future.observed:
            sched().unawaited[future] = p
        for waiter in future.waiters:
            waiter.rv = [future.results]
            waiter.resume_panic = copy_future_panic(p)
            sched().ready(waiter)
        future.waiters.clear()


def lib_task_start(value):
    fn = value if callable(value) else value.v if value is not None else None
    future = FutureState()
    spawn(_FutureChild(fn, future))
    return future
