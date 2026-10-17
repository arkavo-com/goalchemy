"""core.task.spawn and the cooperative scheduler.

Suspending functions are compiled to resumable frames: a frame holds the
function's locals and the block to resume at, and step runs it until it
returns or reaches a pause point. A task is a stack of frames driven by a
trampoline; exactly one task runs at a time and runnable tasks are
dispatched in FIFO order. Pause primitives either complete immediately,
leaving their results in task.rv, or block the task until another task or
a timer readies it. Deferred calls, panics, and recover are managed per task
by the runtime. Under the native pause model a task's frame is a NativeFrame
running a coroutine body; the same scheduler picks the next task.
"""

import os
import time
import threading
import types
from collections import deque
from concurrent.futures import Future

from ..types.panic import GoPanic, nil_deref
from ..types.program import catch_panic, format_chain, panic_state, report_panic, _run_large_stack, source_guard_state, SourceFatal, reserve, unreserve, HostFault
from ..types.print import write_stderr
from ..types.slice import Slice


class Frame:
    def __init__(self):
        self.pc = 0
        self.defers = []
        self.parent = None
        self.panicking = None

    def step(self, t):
        raise NotImplementedError

    def results(self):
        return []


class Task:
    def __init__(self, tid, frame):
        self.id = tid
        self.frame = frame
        self.rv = []
        self.blocked = False
        self.done = False
        self.resume_panic = None
        self.cur_panic = None
        self.defer_target = None
        self.cleanup = None
        self.owner = None
        self.depth = 1


class Blocked(Exception):
    """Raised out of a harness case when its task blocks with nothing runnable."""


class _FatalPanic(Exception):
    def __init__(self, p):
        super().__init__("fatal panic")
        self.p = p


def _seed():
    try:
        s = int(os.environ.get("GOALCHEMY_SEED", "1"))
    except ValueError:
        return 1
    return s if 0 < s < 2**32 else 1


def fatal(msg):
    raise SourceFatal(b"fatal error: " + msg.encode() + b"\n")


class Scheduler:
    def __init__(self, main, real=False, clock=None):
        self.real = real
        self.native_clock = clock or time.monotonic_ns
        self.epoch = self.native_clock() if real else 0
        self.thread = threading.get_ident()
        self.active = True
        self.tasks = set()
        self.contexts = set()
        # Native resources, such as HTTP exchanges, released on retirement.
        self.resources = set()
        self.operations = {}
        self.next_op = 0
        self.mailbox = Mailbox()
        self.retiring_wait = None
        self.runq = deque()
        self.cur = main
        self.main = main
        self.next_id = 1
        self.rng = _seed()
        self.clock = 0
        self.timers = []
        self.seq = 0
        self.harness = False
        self.library_poll = None
        self.library_results = False
        # Identity shared by the schedulers of one library instance; native
        # objects created by any of its calls remain usable by later calls.
        self.domain = self
        self.handoff = None
        self.fault = None

    def choose(self, n):
        """xorshift32 choice source, identical on every target."""
        self.check()
        x = self.rng
        x ^= (x << 13) & 0xFFFFFFFF
        x ^= x >> 17
        x ^= (x << 5) & 0xFFFFFFFF
        self.rng = x
        return x % n

    def check(self):
        if self.thread != threading.get_ident():
            raise HostFault("source access from native callback")

    def now(self):
        self.check()
        if self.real:
            self.clock = max(self.clock, min(MAX_TIME, max(0, self.native_clock() - self.epoch)))
        return self.clock

    def ready(self, t):
        self.check()
        if not self.active or t.done or (t.owner is not None and t.owner is not self):
            return
        t.owner = self
        self.tasks.add(t)
        if t not in self.runq:
            self.runq.append(t)

    def block(self, t):
        self.check()
        if not self.active or t.done or t.owner not in (None, self):
            raise HostFault("invalid blocked task owner")
        t.owner = self
        self.tasks.add(t)
        t.blocked = True

    def add_timer(self, d, task, fn):
        return self.add_timer_at(deadline(self.now(), d), task, fn)

    def add_timer_at(self, at, task, fn):
        self.check()
        if type(at) is not int or not 0 <= at <= MAX_TIME:
            raise HostFault("absolute deadline outside signed clock range")
        self.seq += 1
        timer = [at, self.seq, task, fn]
        self.timers.append(timer)
        return timer

    def remove_timer(self, timer):
        self.check()
        if timer in self.timers:
            self.timers.remove(timer)
        timer[2:] = [None, None]

    def dispatch(self):
        self.now()
        if self.real:
            self.fire_timers(False)
        self.drain()

    def next(self):
        while True:
            if self.library_poll is not None:
                self.library_poll()
            self.dispatch()
            if self.runq:
                return self.runq.popleft()
            if not self.real and self.timers:
                self.fire_timers(True)
                continue
            if self.operations or (self.real and self.timers):
                with self.mailbox.condition:
                    # Recheck inside the publication lock to avoid lost wakeups.
                    if self.mailbox.applicable():
                        continue
                    delay = None
                    if self.timers:
                        delay = max(0, min(t[0] for t in self.timers) - self.now()) / 1e9
                        delay = min(delay, threading.TIMEOUT_MAX)
                    if self.library_poll is not None:
                        delay = 0.05 if delay is None else min(delay, 0.05)
                    self.mailbox.condition.wait(delay)
                continue
            if self.harness:
                raise Blocked()
            fatal("all goroutines are asleep - deadlock!")

    def fire_timers(self, advance=True):
        self.check()
        if not self.timers:
            return
        if advance:
            self.clock = min(t[0] for t in self.timers)
        due = sorted((t for t in self.timers if t[0] <= self.clock), key=lambda t: (t[0], t[1]))
        for timer in due:
            if timer not in self.timers:
                continue
            self.timers.remove(timer)
            _, _, task, fn = timer
            timer[2:] = [None, None]
            if fn is not None:
                fn()
            if task is not None:
                self.ready(task)

    def drain(self):
        self.check()
        with self.mailbox.condition:
            records = [(oid, record) for oid, record in self.mailbox.records.items()
                       if oid in self.mailbox.acks]
        for oid, (tid, wire, fault_record) in records:
            op = self.operations.get(oid)
            if op is None or tid != op.task.id:
                continue
            self.operations.pop(oid)
            self.mailbox.forget(oid)
            try:
                op.cleanup()
            except BaseException as e:
                raise HostFault("registration cleanup: " + fault_text(e)) from e
            if fault_record is not None:
                raise HostFault(fault_record)
            err = op.context.err if op.context is not None else None
            try:
                op.task.rv = op.cancel_result(err) if err is not None else op.decode(wire)
            except BaseException as e:
                raise HostFault("driver decode: " + fault_text(e)) from e
            self.ready(op.task)

    def retire(self):
        self.check()
        errors = []
        self.active = False
        self.library_poll = None
        for release in list(self.resources):
            try:
                release()
            except BaseException as e:
                errors.append(e)
        self.resources.clear()
        for op in list(self.operations.values()):
            try:
                op.cancel()
            except BaseException as e:
                errors.append(e)
        with self.mailbox.condition:
            if len(self.mailbox.acks) < len(self.operations) and self.retiring_wait is not None:
                try:
                    self.retiring_wait()
                except BaseException as e:
                    errors.append(e)
            while len(self.mailbox.acks) < len(self.operations):
                self.mailbox.condition.wait()
        for op in list(self.operations.values()):
            try:
                op.cleanup()
            except BaseException as e:
                errors.append(e)
        self.operations.clear()
        self.mailbox.close()
        for c in list(self.contexts):
            try:
                c.detach()
            except BaseException as e:
                errors.append(e)
        self.contexts.clear()
        for t in list(self.tasks):
            try:
                if t.cleanup is not None:
                    t.cleanup()
            except BaseException as e:
                errors.append(e)
            t.cleanup = None
            t.frame = None
            t.rv = []
            t.cur_panic = t.resume_panic = t.defer_target = None
            t.done = True
            t.owner = None
        self.tasks.clear()
        self.runq.clear()
        self.handoff = None
        for timer in self.timers:
            timer[2:] = [None, None]
        self.timers.clear()
        self.cur = self.main = None
        if errors:
            raise HostFault("retirement cleanup: " + "; ".join(fault_text(e) for e in errors))

    def run(self, t):
        """Drives a task's frames until it blocks, finishes, or hands the
        turn to its native body."""
        self.check()
        self.enter(t)
        while not t.blocked and t.frame is not None:
            p = t.resume_panic
            if p is not None and type(t.frame) is not NativeFrame:
                t.resume_panic = None
                self.exit(t, t.frame, p)
                continue
            f = t.frame
            try:
                f.step(t)
            except GoPanic as e:
                self.exit(t, f, e)
            if self.handoff is not None:
                return

    def enter(self, t):
        self.cur = t
        t.owner = self
        self.tasks.add(t)
        t.blocked = False
        if t.cleanup is not None:
            c, t.cleanup = t.cleanup, None
            c()

    def turn_back(self):
        """Runs the native bodies the turn was handed to until it comes back
        to the driver: nothing is runnable, main is done, or the scheduler
        failed on a body's side, which is raised here."""
        while self.handoff is not None:
            n = self.handoff
            self.handoff = None
            n.resume()
            e = self.fault
            if e is not None:
                self.fault = None
                raise e

    def turn(self, me):
        """Runs the scheduler for a native body that blocked (me) or finished
        (None) while holding the turn: frame tasks run inline and the next
        native body is handed the turn. Reports whether me runs next. Host
        drivers take every turn back. Scheduler failures never unwind a task
        body: the driver raises them."""
        if self.real:
            return False
        q = self.runq
        try:
            while q and not self.main.done:
                u = q.popleft()
                if u is me:
                    self.enter(u)
                    return True
                self.run(u)
                if self.handoff is not None:
                    return False
        except BaseException as e:
            self.fault = e
        return False

    def native_done(self, t, n, failure):
        """Delivers a finished native body like a frame return, then continues
        the task's remaining frames and the scheduler."""
        try:
            self.exit(t, n, None if failure is None else catch_panic(failure))
            if t.frame is not None:
                self.run(t)
                if self.handoff is not None:
                    return
        except BaseException as e:
            self.fault = e
            return
        self.turn(None)

    def exit(self, t, f, p):
        """Leaves frame f (panicking when p is set); deferred calls run first."""
        self.check()
        if p is not None:
            if p.prev is None and f.panicking is not None and f.panicking is not p:
                p.prev = f.panicking
            f.panicking = p
        t.frame = f
        if f.defers:
            r = _DeferRunner(f)
            r.parent = f
            t.frame = r
            return
        self.finish(t, f)

    def finish(self, t, f):
        """Pops f and delivers its results, or its panic, to the caller."""
        self.check()
        p = f.panicking
        parent = f.parent
        t.frame = parent
        t.depth = max(0, t.depth - 1)
        if parent is None:
            t.done = True
            self.tasks.discard(t)
            t.rv = f.results() if self.library_results and p is None else []
            t.cur_panic = t.resume_panic = t.defer_target = None
            if p is not None:
                raise _FatalPanic(p)
            return
        if isinstance(parent, _DeferRunner) and parent.child is f:
            parent.child_done(t, p)
            return
        if p is not None:
            self.exit(t, parent, p)
            return
        t.rv = f.results()


class _DeferRunner(Frame):
    """Runs a frame's deferred calls in reverse order."""

    def __init__(self, target):
        super().__init__()
        self.target = target
        self.child = None
        self.saved = (None, None)

    def step(self, t):
        tf = self.target
        while tf.defers:
            d = tf.defers.pop()
            self.saved = (t.cur_panic, t.defer_target)
            t.cur_panic, t.defer_target = tf.panicking, d.fid
            if d.start:
                try:
                    if d.f is None:
                        raise nil_deref()
                    child = d.f(*d.args)
                except GoPanic as e:
                    t.cur_panic, t.defer_target = self.saved
                    self.after(tf, e)
                    continue
                self.child = child
                child.parent = self
                t.frame = child
                return
            p = None
            try:
                if d.f is None:
                    raise nil_deref()
                d.f(*d.args)
            except GoPanic as e:
                p = e
            t.cur_panic, t.defer_target = self.saved
            self.after(tf, p)
        t.frame = tf
        sched().finish(t, tf)

    def child_done(self, t, p):
        t.cur_panic, t.defer_target = self.saved
        self.child = None
        t.frame = self
        self.after(self.target, p)

    def after(self, tf, p):
        if p is not None:
            if p.prev is None and tf.panicking is not None and tf.panicking is not p:
                p.prev = tf.panicking
            tf.panicking = p
            return
        if tf.panicking is not None and tf.panicking.recovered:
            tf.panicking = None


MAX_TIME = (1 << 63) - 1


def deadline(now, duration):
    # Go duration is signed int64 even though Python integers are unbounded.
    if type(duration) is not int or not -(1 << 63) <= duration <= MAX_TIME:
        raise HostFault("duration outside signed int64")
    return min(MAX_TIME, now + max(0, duration))


def fault_text(value):
    try:
        return str(value)
    except BaseException:
        return "unprintable native fault (" + type(value).__name__ + ")"


def snapshot(value, seen=None, library=False):
    kind = type(value)
    if library and kind in (bytes, bytearray, memoryview):
        from ..types.library import library_snapshot_bytes
        return library_snapshot_bytes(value)
    if kind in (type(None), bool, int, float, str, bytes):
        return value
    if kind is bytearray:
        return bytes(value)
    if kind not in (list, tuple, dict):
        raise HostFault("unsupported native wire value")
    seen = set() if seen is None else seen
    if id(value) in seen:
        raise HostFault("cyclic native wire value")
    seen.add(id(value))
    try:
        if kind is dict:
            if any(type(k) is not str for k in value):
                raise HostFault("wire dictionary keys must be exact strings")
            return {k: snapshot(v, seen, library) for k, v in value.items()}
        values = [snapshot(v, seen, library) for v in value]
        return tuple(values) if kind is tuple else values
    finally:
        seen.remove(id(value))


class Mailbox:
    def __init__(self):
        self.condition = threading.Condition()
        self.live = {}
        self.records = {}  # Python insertion order preserves publication FIFO across holes.
        self.acks = set()
        self.closed = False

    def applicable(self):
        return any(oid in self.acks for oid in self.records)

    def forget(self, oid):
        with self.condition:
            self.live.pop(oid, None)
            self.records.pop(oid, None)
            self.acks.discard(oid)

    def close(self):
        with self.condition:
            self.closed = True
            self.live.clear()
            self.records.clear()
            self.acks.clear()
            self.condition.notify_all()


class HostToken:
    __slots__ = ("mailbox", "operation", "task")

    def __init__(self, mailbox, operation, task):
        self.mailbox, self.operation, self.task = mailbox, operation, task

    def publish(self, value=None, fault=None):
        # Never carry an exception object/traceback/source root across the boundary.
        try:
            wire = snapshot(value)
            fault = None if fault is None else fault_text(fault)
        except BaseException as e:
            wire, fault = None, "native publication: " + fault_text(e)
        m = self.mailbox
        with m.condition:
            if m.closed or type(self.operation) is not int or type(self.task) is not int or m.live.get(self.operation) != self.task or self.operation in m.records:
                return False
            m.records[self.operation] = (self.task, wire, fault)
            m.condition.notify_all()
            return True

    def acknowledge_cleanup(self):
        m = self.mailbox
        with m.condition:
            if m.closed or type(self.operation) is not int or type(self.task) is not int or m.live.get(self.operation) != self.task:
                return False
            m.acks.add(self.operation)
            m.condition.notify_all()
            return True


class HostOperation:
    def __init__(self, task, context, cancel, cleanup, decode, cancel_result):
        self.task, self.context = task, context
        self.cancel, self.cleanup = cancel, cleanup
        self.decode, self.cancel_result = decode, cancel_result


def register_host(t, context=None, cancel=lambda: None, cleanup=lambda: None,
                  decode=lambda wire: wire, cancel_result=lambda err: [None, err]):
    s = sched()
    s.check()
    if not s.real or not s.active or t.done or t.owner not in (None, s):
        raise HostFault("invalid host task owner")
    if context is not None:
        from .std_context_err import check_context
        check_context(context)
    s.next_op += 1
    oid = s.next_op
    detach = context._add_hook(cancel)[1] if context is not None else lambda: None
    def release():
        try:
            detach()
        finally:
            cleanup()
    s.operations[oid] = HostOperation(t, context, cancel, release, decode, cancel_result)
    with s.mailbox.condition:
        s.mailbox.live[oid] = t.id
    t.owner = s
    s.tasks.add(t)
    s.block(t)
    return HostToken(s.mailbox, oid, t.id)


def launch_host(t, submit, context=None, inputs=None, cancel=lambda: None,
                cleanup=lambda: None, decode=lambda wire: wire,
                cancel_result=lambda err: [None, err]):
    copied = snapshot(inputs)
    token = register_host(t, context, cancel, cleanup, decode, cancel_result)
    try:
        future = submit(copied)
        if type(future) is not Future:
            raise HostFault("launch_host requires an exact concurrent.futures.Future")
        # Contract: submit returns/raises and this Future settles only after the
        # adapter has released native leases; Future state alone proves no cleanup.
        def done(f):
            try:
                token.publish(f.result())
            except BaseException as e:
                token.publish(fault="native Future: " + fault_text(e))
            finally:
                token.acknowledge_cleanup()
        future.add_done_callback(done)
    except BaseException as e:
        token.publish(fault="native submission: " + fault_text(e))
        token.acknowledge_cleanup()
    return token


_sched = [Scheduler(Task(0, None))]


def sched():
    _sched[0].check()
    return _sched[0]


def call(t, child):
    """Pushes a callee frame; the caller resumes with its results in t.rv."""
    owner = sched()
    if t.owner is not owner or not owner.active or t.done:
        raise HostFault("invalid source call owner")
    if owner.real and t.depth >= 512:
        raise SourceFatal(b"runtime: goroutine stack exceeds limit\nfatal error: stack overflow\n")
    t.depth += 1
    child.parent = t.frame
    t.frame = child


def ret(t, f):
    sched().exit(t, f, None)


class _SyncFrame(Frame):
    def __init__(self, fn):
        super().__init__()
        self.fn = fn
        self.res = []

    def step(self, t):
        self.res = self.fn()
        ret(t, self)

    def results(self):
        return self.res


def sync(fn):
    """Runs an ordinary call as a frame."""
    return _SyncFrame(fn)


def adapt(f, n):
    """Adapts an ordinary function value with n results to the resumable form."""
    if f is None:
        return None

    def g(*a):
        def run():
            r = f(*a)
            return [] if n == 0 else [r] if n == 1 else list(r)

        return sync(run)

    g._fid = getattr(f, "_fid", None)
    return g


def adapt_slice(s, n):
    if s.a is None:
        return s
    a = [adapt(s.a[s.o + i], n) for i in range(s.l)]
    return Slice(a, 0, len(a), len(a))


def spawn(f):
    """go f(args): starts a task running frame f."""
    s = sched()
    t = Task(s.next_id, f)
    s.next_id += 1
    s.ready(t)


def _install(main, harness):
    s = Scheduler(main)
    s.harness = harness
    _sched[0] = s
    panic_state.get = lambda: _sched[0].cur
    return s


def _drive(factory, real=False, harness=False, library=False, convert=lambda rv: rv):
    s = None
    main = None
    failure = None
    result = None
    prior_guard = getattr(source_guard_state, "enabled", False)
    source_guard_state.enabled = real
    source_guard_state.depth = 0
    try:
        old = _sched[0]
        old.thread = threading.get_ident()
        old.retire()
        main = Task(0, None)
        s = Scheduler(main, real)
        s.harness = harness
        s.library_results = library
        _sched[0] = s
        panic_state.get = lambda: s.cur
        # Construct emitted frames and source init on the owner, after reservation.
        main.frame = factory()
        s.ready(main)
        while not main.done:
            s.run(s.next())
            if s.handoff is not None:
                s.turn_back()
        result = convert(main.rv)
        main.rv = []
    except RecursionError:
        failure = SourceFatal(b"runtime: goroutine stack exceeds limit\nfatal error: stack overflow\n")
    except BaseException as e:
        if library and isinstance(e, _FatalPanic):
            from ..types.library import LibraryFailure
            failure = LibraryFailure("source_panic", {"Message": format_chain(e.p)})
        elif library and isinstance(e, SourceFatal):
            from ..types.library import LibraryFailure
            failure = LibraryFailure("source_fatal", {"Message": bytes(e.report)})
        else:
            failure = e
    finally:
        try:
            if s is not None:
                s.retire()
        except BaseException as e:
            if failure is None or library:
                failure = e
        if main is not None:
            main.rv = []; main.frame = None
            main.cur_panic = main.resume_panic = main.defer_target = None
            main.owner = None
        from ..types.program import _main_state
        panic_state.get = lambda: _main_state
        source_guard_state.enabled = prior_guard
        source_guard_state.depth = 0
    if isinstance(failure, _FatalPanic):
        if harness:
            raise failure.p
        if library:
            raise GoPanic(failure.p.value)
        report_panic(failure.p)
    if isinstance(failure, SourceFatal):
        if library:
            raise failure
        write_stderr(failure.report)
        os._exit(2)
    if failure is not None:
        raise failure
    return result


def run_main(entry):
    """Legacy virtual executable. Reservation precedes the owner-thread start."""
    reserve()
    try:
        _run_large_stack(lambda: _drive(lambda: entry))
    finally:
        unreserve()


def run_main_host(factory):
    """Blocking real-monotonic executable entry; native callbacks only publish.

    factory constructs compiler frames on this calling owner thread. This is
    neither a returned Future nor an exported SDK async/library wrapper.
    """
    reserve()
    try:
        return _drive(factory, real=True)
    finally:
        unreserve()


def yield_task(t):
    """Requeues the running task: a pause primitive."""
    sched().ready(t)
    sched().block(t)


class _AwaitFrame(Frame):
    def __init__(self, fn):
        super().__init__()
        self.fn = fn
        self.res = []

    def step(self, t):
        if self.pc == 0:
            self.pc = 1
            self.fn(t)
            return
        self.res = t.rv
        ret(t, self)

    def results(self):
        return self.res


def run_isolated(fn):
    reserve()
    try:
        h = _AwaitFrame(fn)
        _drive(lambda: h, harness=True)
        return h.res
    finally:
        unreserve()


def reset_scheduler():
    reserve()
    try:
        old = _sched[0]
        old.thread = threading.get_ident()
        old.retire()
        _install(Task(0, None), True)
    finally:
        unreserve()


def _sequential_start():
    old = _sched[0]
    old.thread = threading.get_ident()
    old.retire()
    _sched[0] = Scheduler(Task(0, None))


def _sequential_end():
    _sched[0].retire()


# types/program is always linked; sequential programs link this runtime only
# when a source primitive needs it. No optional scheduler import from program.
from ..types import program as _program
_program.sequential_start = _sequential_start
_program.sequential_end = _sequential_end


def await_native(fn):
    """Wrap a suspending capability for defer/go first-class invocation."""
    return lambda *args: _AwaitFrame(lambda t: fn(t, *args))


class NativeFrame(Frame):
    """The frame of a native coroutine body with n results, for go, defer,
    function values and entries. The scheduler hands it the turn and the
    driver's trampoline (turn_back) starts or resumes the coroutine, so
    bodies never nest."""

    def __init__(self, body, n):
        super().__init__()
        self.body = body
        self.n = n
        self.task = None
        self.coro = None
        self.res = []

    def step(self, t):
        self.task = t
        t.owner.handoff = self

    def results(self):
        return self.res

    def pack(self, v):
        n = self.n
        return [] if n == 0 else [v] if n == 1 else list(v)

    def resume(self):
        t = self.task
        try:
            c = self.coro
            if c is None:
                c = self.coro = self.body(t)
            c.send(None)
            return
        except StopIteration as e:
            self.res = self.pack(e.value)
            failure = None
        except BaseException as e:
            failure = e
        self.coro = None
        t.owner.native_done(t, self, failure)


def native_task(body, n):
    return NativeFrame(body, n)


@types.coroutine
def wait(t):
    """Completes a pause primitive that left native task t blocked: the
    scheduler runs other tasks until it chooses t again. A panic the waker
    left for the task is raised here."""
    if not t.owner.turn(t):
        yield
    p = t.resume_panic
    if p is not None:
        t.resume_panic = None
        raise p


async def await_frame(t, f):
    """Runs a function value's or method's frame on native task t and leaves
    its results in t.rv."""
    k = type(f)
    if k is NativeFrame:
        t.rv = f.pack(await f.body(t))
    elif k is _SyncFrame:
        t.rv = f.fn()
    elif k is _AwaitFrame:
        f.fn(t)
        if t.blocked:
            await wait(t)
    else:
        raise HostFault("synchronous call paused")


async def run_defers_async(t, ds, p):
    """Runs a native function's deferred calls, awaiting those that pause;
    recover and the panic chain behave as in run_defers."""
    panicking = p
    while ds:
        d = ds.pop()
        saved_panic, saved_target = t.cur_panic, t.defer_target
        t.cur_panic, t.defer_target = panicking, d.fid
        try:
            if d.f is None:
                raise nil_deref()
            if d.start:
                await await_frame(t, d.f(*d.args))
            else:
                d.f(*d.args)
        except GoPanic as np:
            if np is not panicking and np.prev is None:
                np.prev = panicking
            panicking = np
            continue
        finally:
            t.cur_panic, t.defer_target = saved_panic, saved_target
        if panicking is not None and panicking.recovered:
            panicking = None
    if panicking is not None:
        raise panicking
