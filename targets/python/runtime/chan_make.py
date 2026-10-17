"""core.chan.make: make(chan T, size), and the channel state shared by the
channel and select runtime functions."""

from collections import deque

from ..types.panic import plain_panic
from .task_spawn import sched, HostFault


class SelectState:
    __slots__ = ("done",)

    def __init__(self):
        self.done = False


class Waiter:
    __slots__ = ("task", "val", "sel", "idx", "owner")

    def __init__(self, task, val, sel=None, idx=0):
        self.owner = sched()
        self.task = task
        self.val = val
        self.sel = sel
        self.idx = idx

    def live(self):
        return self.task is not None and self.owner.active and not self.task.done and (self.sel is None or not self.sel.done)

    def detach(self):
        if self.task is None:
            return
        self.owner.check()
        self.task = self.val = None

    def recv_done(self, val, ok):
        """Completes a waiting receiver with a value or closure."""
        if not self.live():
            return
        self.owner.check()
        if self.sel is not None:
            self.sel.done = True
            self.task.rv = [self.idx, val, ok]
        else:
            self.task.rv = [val, ok]
        self.owner.ready(self.task)

    def send_done(self, closed):
        """Completes a waiting sender; closed makes it panic when it resumes."""
        if not self.live():
            return
        self.owner.check()
        if self.sel is not None:
            self.sel.done = True
            self.task.rv = [self.idx, None, False]
        else:
            self.task.rv = []
        if closed:
            self.task.resume_panic = plain_panic("send on closed channel")
        self.owner.ready(self.task)


class Chan:
    __slots__ = ("buf", "size", "closed", "recvq", "sendq", "zero", "__weakref__")

    def __init__(self, size, zero):
        self.buf = deque()
        self.size = size
        self.closed = False
        self.recvq = deque()
        self.sendq = deque()
        self.zero = zero


def dequeue(q):
    sched().check()
    while q:
        w = q.popleft()
        if w.live():
            return w
        w.detach()
    return None


def has_live(q):
    return any(w.live() for w in q)


def try_recv(ch):
    """Receives without blocking: (value, ok, done)."""
    sched().check()
    if ch.buf:
        v = ch.buf.popleft()
        w = dequeue(ch.sendq)
        if w is not None:
            ch.buf.append(w.val)
            w.send_done(False)
        return v, True, True
    w = dequeue(ch.sendq)
    if w is not None:
        v = w.val
        w.send_done(False)
        return v, True, True
    if ch.closed:
        return ch.zero(), False, True
    return None, False, False


def make_chan(size, zero=lambda: None):
    if size < 0 or size > 2**53:
        raise plain_panic("makechan: size out of range")
    return Chan(size, zero)


def attach_waiter(t, queue, waiter):
    owner = sched()
    if not owner.active or waiter.owner is not owner or waiter.task is not t or t.done or t.owner not in (None, owner):
        raise HostFault("invalid channel waiter owner")
    queue.append(waiter)
    def cleanup():
        if waiter in queue:
            queue.remove(waiter)
        waiter.detach()
    t.cleanup = cleanup
