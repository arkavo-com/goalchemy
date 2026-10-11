"""Owned value-library calls. Source is serialized; publication follows retirement."""
import asyncio
import threading
from concurrent.futures import Future
from .program import HostFault, SourceFatal, reserve, unreserve
from .panic import GoPanic
from .slice import Slice

class LibraryFailure(Exception):
    def __init__(self, kind, fields=None):
        super().__init__('library: ' + kind)
        self.kind = kind
        self.fields = fields or {}

LIMIT = 64 << 20

def library_bool(v):
    if type(v) is not bool: raise LibraryFailure('invalid_argument')
    return v

def library_int(v, bits, signed):
    if type(v) is not int or not (-(1 << (bits-1)) if signed else 0) <= v < (1 << (bits-1) if signed else 1 << bits):
        raise LibraryFailure('invalid_argument')
    return v

def library_string(v):
    if type(v) is not bytes or len(v) > LIMIT: raise LibraryFailure('invalid_argument')
    return v

def library_byte_size(v):
    if type(v) not in (bytes, bytearray, memoryview): raise LibraryFailure('invalid_argument')
    try:
        size = v.nbytes if type(v) is memoryview else len(v)
        if size > LIMIT: raise LibraryFailure('invalid_argument')
        return size
    except (ValueError, TypeError):
        raise LibraryFailure('invalid_argument') from None

def library_snapshot_bytes(v):
    """Bounded immutable submission snapshot; immutable bytes need no copy."""
    if v is None: return None
    library_byte_size(v)
    try: return v if type(v) is bytes else bytes(v)
    except (ValueError, TypeError): raise LibraryFailure('invalid_argument') from None

def library_bytes(v):
    if v is None: return None
    library_byte_size(v)
    try: return bytearray(v)
    except (ValueError, TypeError): raise LibraryFailure('invalid_argument') from None

def library_list(v):
    if type(v) not in (list, tuple) or len(v) > LIMIT: raise LibraryFailure('invalid_argument')
    return v

def library_fields(v, names):
    if type(v) is not dict or any(type(k) is not str or k not in names.split(',') for k in v):
        raise LibraryFailure('invalid_argument')

def library_clear_refs():
    from .ref import _refs
    _refs.clear()

class LibrarySequence:
    """Frame-compatible sequence without running source during construction."""
    def __init__(self, init, call):
        from ..runtime.task_spawn import Frame
        self.pc=0; self.defers=[]; self.parent=None; self.panicking=None
        self.init=init; self.call=call; self.res=[]
    def step(self,t):
        from ..runtime.task_spawn import call, ret
        if self.pc==0:
            self.pc=1; call(t,self.init); return
        if self.pc==1:
            self.pc=2; call(t,self.call()); return
        self.res=t.rv;ret(t,self)
    def results(self): return self.res

_condition = threading.Condition()
_queue = []

class LibraryOperation:
    def __init__(self):
        self._future=Future(); self._cancel=threading.Event(); self._mailbox=None; self._user_cancel=False
    def cancel(self):
        if self._future.done(): return False
        self._user_cancel=True
        self._internal_cancel()
        return True
    def _internal_cancel(self):
        self._cancel.set()
        with _condition: _condition.notify_all()
        m=self._mailbox
        if m is not None:
            with m.condition: m.condition.notify_all()
        return True
    def result(self, timeout=None): return self._future.result(timeout)
    def done(self): return self._future.done()
    async def wait(self):
        wrapped=asyncio.wrap_future(self._future)
        try: return await asyncio.shield(wrapped)
        except asyncio.CancelledError:
            self.cancel()
            # Python task cancellation is a request. Wait through repeated requests
            # until native cleanup has actually completed before propagating it.
            while not wrapped.done():
                try: await asyncio.shield(wrapped)
                except asyncio.CancelledError: continue
                except BaseException: break
            if wrapped.done() and not wrapped.cancelled():
                try: wrapped.exception()
                except BaseException: pass
            raise
    def __await__(self): return self.wait().__await__()

def _library_warn_default(message):
    import sys
    try: sys.stderr.write('goalchemy: warning: '+message+'\n')
    except BaseException: pass

library_warn = _library_warn_default

def set_library_warn(fn):
    """Replaces the sink for boundary warnings such as leaked handles."""
    global library_warn
    library_warn = fn

class Instance:
    """Persistent source state behind a family of handles; only touched while
    holding the library reservation."""
    def __init__(self):
        self.globals=None; self.native_keys={}; self.native_next_key=0; self.domain=object()
        self.objects={}; self.live=0; self.poisoned=False; self.retired=False
        self.streams={}; self.next_stream=0
    def retire_now(self):
        if self.retired: return
        self.retired=True; self.globals=None; self.objects.clear(); self.native_keys.clear(); self.streams.clear()

class Handle:
    """The host's reference to one source object of an instance."""
    def __init__(self, inst, obj, name):
        self.inst=inst; self.obj=obj; self.name=name
        self.closed=False; self.released=False; self.active=None
    def release(self):
        if self.released: return
        self.released=True
        inst=self.inst
        e=inst.objects.get(id(self.obj))
        if e is not None and e[0] is self: del inst.objects[id(self.obj)]
        inst.live-=1; self.obj=None
        if inst.live==0: inst.retire_now()

def _finalize(h):
    with _condition:
        if h.closed: return
        h.closed=True
    def worker():
        reserve()
        try:
            if not h.released:
                library_warn(h.name+' handle was not closed; releasing it without running Close')
                h.release()
        finally: unreserve()
    threading.Thread(target=worker,name='goalchemy-finalize',daemon=True).start()

def library_wrap(inst, obj, name, make):
    """Returns the host wrapper for obj, reusing the live wrapper of the same
    source object so host identity follows source identity."""
    import weakref
    if obj is None: return None
    e=inst.objects.get(id(obj))
    if e is not None and not e[0].closed:
        w=e[1]()
        if w is not None: return w
    h=Handle(inst,obj,name); w=make(h)
    inst.objects[id(obj)]=(h,weakref.ref(w)); inst.live+=1
    weakref.finalize(w,_finalize,h)
    return w

def library_failed(e):
    op=LibraryOperation(); op._future.set_exception(e); return op

def library_obj(h):
    return None if h is None else h.obj

def library_handle(v, cls):
    if v is None: return None
    if type(v) is not cls: raise LibraryFailure('invalid_argument')
    return v._h

def library_close(h, state, factory, failure):
    """Fails calls queued on h, cancels its active call, runs the source Close
    when factory is given and the instance is healthy, then releases h."""
    with _condition:
        if h.closed:
            op=LibraryOperation(); op._future.set_result(None); return op
        h.closed=True
        active=h.active
        _condition.notify_all()
    if active is not None: active._internal_cancel()
    if factory is None:
        from ..runtime.task_spawn import sync
        factory=lambda ctx,owned,fresh: sync(lambda: [None])
    def output(rv, inst):
        err=failure(rv[0]) if rv else None
        if err is not None: raise err
        return None
    return library_submit(factory, output, state, [], None, [h], closing=h)

def library_submit(factory, output, state, inputs, options=None, handles=(), closing=None, streams=(), ids=None):
    from ..runtime.task_spawn import snapshot, _drive, sched
    from ..runtime.std_context_background import std_context_background
    from ..runtime.std_context_with_cancel import std_context_with_cancel
    op=LibraryOperation()
    try:
        owned=snapshot(inputs, library=True)
        if options is None: options={}
        if type(options) is not dict or any(k not in ('callbacks',) for k in options): raise LibraryFailure('invalid_argument')
        raw_callbacks=options.get('callbacks', {})
        if type(raw_callbacks) is not dict: raise LibraryFailure('invalid_argument')
        callbacks=dict(raw_callbacks)
        if any(type(k) is not str or not callable(v) for k,v in callbacks.items()): raise LibraryFailure('invalid_argument')
    except LibraryFailure as e:
        op._future.set_exception(e); return op
    except BaseException as e:
        op._future.set_exception(LibraryFailure('invalid_argument')); return op
    handles=list(handles)
    if getattr(_stream_thread,'active',False):
        op._future.set_exception(LibraryFailure('reentrant')); return op
    me=next((h for h in handles if h is not None), None)
    if me is closing: me=None
    with _condition:
        if me is not None and me.closed:
            op._future.set_exception(LibraryFailure('closed')); return op
        _queue.append(op)
    def worker():
        failure=None; result=None; acquired=False; state_s=None; inst=None; fresh=True; keep=False; left=[0]
        try:
            with _condition:
                while _queue[0] is not op:
                    if op._cancel.is_set(): raise LibraryFailure('canceled', {'CauseCategory':b'canceled'})
                    if me is not None and me.closed: raise LibraryFailure('closed')
                    _condition.wait()
            if op._cancel.is_set(): raise LibraryFailure('canceled', {'CauseCategory':b'canceled'})
            reserve(); acquired=True
            for h in handles:
                if h is None: continue
                if h.released or (h is not closing and h.closed): raise LibraryFailure('closed')
                if inst is not None and h.inst is not inst: raise LibraryFailure('instance_mismatch')
                inst=h.inst
            fresh=inst is None
            if fresh: inst=Instance()
            elif inst.poisoned: raise LibraryFailure('poisoned')
            keep=not fresh
            if me is not None:
                with _condition:
                    if me.closed: raise LibraryFailure('closed')
                    me.active=op
            if not fresh and state.load is not None: state.load(inst.globals)
            def start():
                nonlocal state_s
                s=sched();state_s=s;s.native_keys=inst.native_keys;s.native_next_key=inst.native_next_key;s.domain=inst.domain;s.library_callbacks=_register_streams(inst,callbacks,streams,ids)
                op._mailbox=s.mailbox
                ctx,cancel=std_context_with_cancel(std_context_background())
                s.library_poll=lambda: cancel() if op._cancel.is_set() else None
                s.library_poll()
                return factory(ctx,owned,fresh)
            def owned_output(rv):
                nonlocal keep
                if not fresh and op._cancel.is_set(): raise LibraryFailure('canceled', {'CauseCategory':b'canceled'})
                value=output(rv,inst)
                if fresh and inst.live>0: keep=True
                s=state_s
                left[0]=sum(1 for t in s.tasks if t is not s.main and not t.done)
                if op._cancel.is_set(): raise LibraryFailure('canceled', {'CauseCategory':b'canceled'})
                return value
            result=_drive(start,real=True,library=True,convert=owned_output)
        except LibraryFailure as e:
            failure=e.with_traceback(None);failure.__context__=None;failure.__cause__=None
        except GoPanic as e: failure=LibraryFailure('source_panic')
        except SourceFatal as e: failure=LibraryFailure('source_fatal')
        except BaseException:
            failure=LibraryFailure('host_fault')
        finally:
            try:
                poisoned=failure is not None and failure.kind in ('source_panic','source_fatal','host_fault')
                if poisoned and not fresh and inst is not None: inst.poisoned=True
                if state_s is not None:
                    state_s.check()
                    if keep and not inst.poisoned:
                        inst.native_next_key=state_s.native_next_key
                        if state.save is not None: inst.globals=state.save()
                        if left[0]>0: library_warn('%d goroutine(s) still running when a handle call returned were abandoned' % left[0])
                    else:
                        state_s.native_keys.clear()
                    state_s.native_keys={};state_s.library_callbacks={};state_s.domain=state_s
                if acquired:
                    if keep: state.zero()
                    else: state.reset()
                    if closing is not None: closing.release()
                    elif keep and inst.live==0: inst.retire_now()
            except BaseException as e: failure=LibraryFailure('host_fault')
            finally:
                owned.clear();callbacks.clear();op._mailbox=None
                if me is not None and me.active is op: me.active=None
                if acquired: unreserve()
                with _condition:
                    _queue.remove(op);_condition.notify_all()
        # Native continuations execute outside reservation and queue locks.
        if failure is not None:
            if closing is not None and failure.kind=='poisoned':
                reserve()
                try: closing.release()
                finally: unreserve()
                op._future.set_result(None); return
            if me is not None and me.closed and failure.kind=='canceled' and not op._user_cancel:
                failure=LibraryFailure('canceled', {'CauseCategory':b'closed'})
            op._future.set_exception(failure)
        else: op._future.set_result(result)
    try: threading.Thread(target=worker,name='goalchemy-library',daemon=True).start()
    except BaseException:
        owned.clear();callbacks.clear()
        with _condition: _queue.remove(op);_condition.notify_all()
        op._future.set_exception(LibraryFailure('host_fault'))
    return op

class LibraryState:
    """Saves, restores and zeroes the generated source globals. zero leaves
    canonical field references intact for a persistent instance."""
    def __init__(self, reset, zero=None, save=None, load=None):
        self.reset=reset; self.zero=zero or reset; self.save=save; self.load=load

def library_float(v, bits):
    if type(v) not in (float, int):
        raise LibraryFailure('invalid_argument')
    from .float import round_float
    try:
        return round_float(float(v), bits)
    except (OverflowError, ValueError):
        raise LibraryFailure('invalid_argument') from None


_stream_thread = threading.local()

def _register_streams(inst, callbacks, streams, ids):
    for i, provider in enumerate(streams):
        if provider is None: continue
        inst.next_stream += 1
        ids[i] = inst.next_stream
        inst.streams['goalchemy.stream.%d' % inst.next_stream] = provider
    merged = dict(callbacks)
    merged.update(inst.streams)
    return merged

def _stream_of(v):
    import io
    if type(v) in (bytes, bytearray, memoryview): return io.BytesIO(bytes(v))
    return v

def _int(b, at):
    return int.from_bytes(b[at:at+8], 'big', signed=True)

def _serve(s, req):
    op = req[0:1]
    if op == b'r':
        n = _int(req, 1)
        if n < 0 or n >= 128 << 10: raise ValueError('hostio: malformed stream request')
        if hasattr(s, 'readinto'):
            buf = bytearray(n)
            k = s.readinto(buf)
            data = None if k is None else bytes(buf[:k])
        else:
            data = s.read(n)
        if data is None: data = b''
        if type(data) not in (bytes, bytearray) or len(data) > n: raise ValueError('hostio: invalid read result')
        return (b'\x01' if n and not data else b'\x00') + bytes(data)
    if op == b'w':
        k = s.write(bytes(req[1:]))
        return b'\x00' + (len(req) - 1 if k is None else k).to_bytes(8, 'big', signed=True)
    if op == b's':
        pos = s.seek(_int(req, 1), req[9])
        return b'\x00' + int(pos).to_bytes(8, 'big', signed=True)
    if op == b'c':
        s.close()
        return b'\x00'
    raise ValueError('hostio: malformed stream request')

def library_stream(v, kind):
    """Adapts a host file-like object, or bytes, to a std/io parameter.
    Reads and writes copy their buffers; an empty read is io.EOF."""
    if v is None: return None
    s = _stream_of(v)
    need = [m for m, k in (('read', 'Read'), ('write', 'Write'), ('seek', 'Seek'), ('close', 'Close')) if k in kind]
    if any(not callable(getattr(s, m, None)) for m in need): raise LibraryFailure('invalid_argument')
    def provider(request):
        _stream_thread.active = True
        try:
            return _serve(s, bytes(request.payload))
        except BaseException as e:
            return b'\x02' + str(e).encode('utf-8', 'replace')
        finally:
            _stream_thread.active = False
    return provider


import io as _io

class SourceStream(_io.RawIOBase):
    """The host view of a source std/io reader or writer: a binary,
    unbuffered file-like object. Each operation is a blocking library call
    on its handle; close runs the source Close, if any, and releases it.
    Wrap it in io.BufferedReader or io.BufferedWriter for buffering."""
    def __init__(self, h, ops):
        super().__init__()
        self._h = h; self._ops = ops
    def __del__(self):
        pass
    def readable(self): return 'read' in self._ops
    def writable(self): return 'write' in self._ops
    def seekable(self): return 'seek' in self._ops
    def _op(self, name):
        op = self._ops.get(name)
        if op is None: raise _io.UnsupportedOperation(name)
        return op
    def readinto(self, b):
        read = self._op('read')
        view = memoryview(b).cast('B')
        if len(view) == 0: return 0
        data, eof = read(self._h, len(view))
        view[:len(data)] = data
        return len(data)
    def read1(self, size=-1):
        return self.read(size)
    def readall(self):
        read = self._op('read')
        parts = []
        while True:
            data, eof = read(self._h, 1 << 20)
            parts.append(data)
            if not data: return b''.join(parts)
    def write(self, b):
        write = self._op('write')
        data = memoryview(b).cast('B')
        n = 0
        while True:
            n += write(self._h, bytes(data[n:n + (1 << 20)]))
            if n >= len(data): return n
    def seek(self, offset, whence=0):
        return self._op('seek')(self._h, offset, whence)
    def tell(self):
        return self.seek(0, 1)
    def close(self):
        if self.closed: return
        try: self._ops['close'](self._h)
        finally: super().close()
