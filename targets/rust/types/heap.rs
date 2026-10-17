//! The traced heap. Objects are addressed by handle; collection is
//! mark-and-sweep from explicit roots and runs only at safepoints, where
//! every live value is held in a registered root: function frames (Fr),
//! globals, task frames, temporary runtime roots, and in-flight panics.
use super::*;
use std::collections::HashMap;

pub enum Obj {
    Free,
    /// Owner-associated native registry identity; never private key material.
    NativeKey(u64,u64),
    /// Struct fields, array elements, slice backing arrays, and cells.
    Vals(Vec<V>),
    /// One byte per element, a traced leaf with no child handles.
    Bytes(Vec<u8>),
    Map(GoMap),
    Box(&'static TypeDesc, V),
    Func(Func),
    Chan(Chan),
    Mutex(Mutex),
    WaitGroup(WaitGroup),
    Context(Context),
    Iter(MapIter),
    Panic(PanicObj),
}

pub struct Heap {
    objs: Vec<Obj>,
    marks: Vec<bool>,
    free: Vec<H>,
    live: usize,
    since: usize,
    threshold: usize,
    stress: bool,
    pub peak: usize,
    pub collections: usize,
}

/// Allocations between collections, at least; GOALCHEMY_GC_THRESHOLD
/// overrides it (1 collects at every safepoint).
fn min_threshold() -> usize {
    std::env::var("GOALCHEMY_GC_THRESHOLD").ok().and_then(|v| v.parse().ok()).unwrap_or(50_000)
}

thread_local! {
    static HEAP: RefCell<Heap> = RefCell::new(Heap {
        objs: vec![Obj::Free],
        marks: vec![false],
        free: Vec::new(),
        live: 0,
        since: 0,
        threshold: min_threshold(),
        stress: min_threshold() == 1,
        peak: 0,
        collections: 0,
    });
    static ROOTS: RefCell<Vec<*mut FrData>> = RefCell::new(Vec::new());
    static TEMP: RefCell<Vec<V>> = RefCell::new(Vec::new());
    static PERM: RefCell<Vec<V>> = RefCell::new(Vec::new());
    /// Source objects referenced by library handles of this owner thread.
    pub static LIBRARY_ROOTS: RefCell<std::collections::BTreeMap<u64, V>> = RefCell::new(std::collections::BTreeMap::new());
}

/// Keeps a runtime-owned value alive for the rest of the program.
pub fn permanent_root(v: V) {
    PERM.with(|p| p.borrow_mut().push(v))
}

pub fn alloc(o: Obj) -> H {
    // Byte storage counts one allocation per KiB, so large buffers that
    // become garbage quickly still trigger collections.
    let weight = match &o {
        Obj::Bytes(b) => b.len() >> 10,
        _ => 0,
    };
    HEAP.with(|hp| {
        let mut hp = hp.borrow_mut();
        hp.live += 1;
        hp.since += 1 + weight;
        if hp.live > hp.peak {
            hp.peak = hp.live;
        }
        if let Some(h) = hp.free.pop() {
            hp.objs[h as usize] = o;
            h
        } else {
            hp.objs.push(o);
            hp.marks.push(false);
            (hp.objs.len() - 1) as H
        }
    })
}

/// Runs f on an object. f must not allocate, call generated code, or
/// otherwise reenter the heap.
#[inline]
pub fn with<R>(h: H, f: impl FnOnce(&mut Obj) -> R) -> R {
    if h == 0 {
        panic_nil_deref();
    }
    HEAP.with(|hp| f(&mut hp.borrow_mut().objs[h as usize]))
}

pub fn vals(v: Vec<V>) -> V {
    V::Obj(alloc(Obj::Vals(v)))
}

/// Slot i of a Vals object.
#[inline]
pub fn slot(h: H, i: usize) -> V {
    with(h, |o| match o {
        Obj::Vals(v) => v[i].clone(),
        Obj::Bytes(v) => V::Int(v[i] as i64),
        _ => fault("slot of non-storage object"),
    })
}

#[inline]
pub fn set_slot(h: H, i: usize, x: V) {
    let old = with(h, |o| match o {
        Obj::Vals(v) => std::mem::replace(&mut v[i], x),
        Obj::Bytes(v) => { v[i] = x.i() as u8; V::Nil },
        _ => fault("slot of non-storage object"),
    });
    drop(old);
}

pub fn vals_len(h: H) -> usize {
    with(h, |o| match o {
        Obj::Vals(v) => v.len(),
        Obj::Bytes(v) => v.len(),
        _ => fault("length of non-storage object"),
    })
}

pub fn vals_copy(h: H) -> Vec<V> {
    with(h, |o| match o {
        Obj::Vals(v) => v.clone(),
        _ => fault("copy of non-storage object"),
    })
}

pub struct Deferred {
    pub f: V,
    pub args: Vec<V>,
    pub fid: i64,
    pub start: bool,
}

pub struct FrData {
    pub v: Vec<V>,
    pub d: Vec<Deferred>,
}

/// Storage for one function activation's locals and deferred calls. A
/// rooted Fr is registered with the collector for its lifetime; a frame
/// Fr belongs to a resumable Frame traced through its task.
pub struct Fr {
    p: *mut FrData,
    idx: usize,
    rooted: bool,
    task: Option<std::rc::Weak<Task>>,
}

impl Fr {
    pub fn new(n: usize) -> Fr {
        let p = Box::into_raw(Box::new(FrData { v: vec![V::Nil; n], d: Vec::new() }));
        let idx = ROOTS.with(|r| {
            let mut r = r.borrow_mut();
            r.push(p);
            r.len() - 1
        });
        Fr { p, idx, rooted: true, task: None }
    }

    pub fn unrooted(n: usize) -> Fr {
        let p = Box::into_raw(Box::new(FrData { v: vec![V::Nil; n], d: Vec::new() }));
        Fr { p, idx: 0, rooted: false, task: None }
    }

    /// Async activations belong to their task, not the synchronous TLS stack.
    /// Registrations are removed by identity: dropping a parked future need
    /// not follow construction order, even within a single task.
    pub fn task(t: &Rc<Task>, n: usize) -> Fr {
        let mut fr = Fr::unrooted(n);
        t.native_roots.borrow_mut().push(fr.p);
        fr.task = Some(Rc::downgrade(t));
        fr
    }

    #[inline]
    pub fn len(&self) -> usize { unsafe { (*self.p).v.len() } }

    pub fn g(&self, i: usize) -> V {
        unsafe { (&(*self.p).v)[i].clone() }
    }

    #[inline]
    pub fn s(&self, i: usize, x: V) {
        let old = unsafe { std::mem::replace(&mut (&mut (*self.p).v)[i], x) };
        drop(old);
    }

    pub fn defer(&self, d: Deferred) {
        unsafe { (*self.p).d.push(d) }
    }

    pub fn pop_defer(&self) -> Option<Deferred> {
        unsafe { (*self.p).d.pop() }
    }

    pub fn clear(&self) {
        unsafe { for v in &mut (*self.p).v { *v = V::Nil; } (*self.p).d.clear(); }
    }

    pub fn has_defers(&self) -> bool {
        unsafe { !(*self.p).d.is_empty() }
    }

    pub fn trace(&self, out: &mut Vec<V>) {
        trace_data(self.p, out)
    }

    /// Leaks the frame data as a permanent root (globals).
    pub fn leak(self) -> *mut FrData {
        let p = self.p;
        std::mem::forget(self);
        p
    }
}

impl Drop for Fr {
    fn drop(&mut self) {
        if self.rooted {
            let idx = self.idx;
            ROOTS.with(|r| r.borrow_mut().truncate(idx));
        }
        if let Some(t) = self.task.as_ref().and_then(|t| t.upgrade()) {
            t.native_roots.borrow_mut().retain(|p| *p != self.p);
        }
        unsafe { drop(Box::from_raw(self.p)) }
    }
}

fn trace_data(p: *mut FrData, out: &mut Vec<V>) {
    unsafe {
        out.extend((*p).v.iter().cloned());
        for d in (*p).d.iter() {
            out.push(d.f.clone());
            out.extend(d.args.iter().cloned());
        }
    }
}

/// Keeps runtime temporaries alive across calls into generated code.
pub struct TempRoots(usize);

pub fn temp_root(vs: &[V]) -> TempRoots {
    TEMP.with(|t| {
        let mut t = t.borrow_mut();
        let n = t.len();
        t.extend(vs.iter().cloned());
        TempRoots(n)
    })
}

impl Drop for TempRoots {
    fn drop(&mut self) {
        let n = self.0;
        let old: Vec<V> = TEMP.with(|t| t.borrow_mut().drain(n..).collect());
        drop(old);
    }
}

/// A collection point: collects when enough has been allocated since the
/// last collection.
#[inline]
pub fn safepoint() {
    let due = HEAP.with(|hp| {
        let hp = hp.borrow();
        hp.stress || hp.since >= hp.threshold
    });
    if due {
        collect();
    }
}

pub fn collect() {
    let mut stack: Vec<V> = Vec::new();
    let mut frames: Vec<Rc<Frame>> = Vec::new();
    ROOTS.with(|r| {
        for p in r.borrow().iter() {
            trace_data(*p, &mut stack);
        }
    });
    TEMP.with(|t| stack.extend(t.borrow().iter().cloned()));
    PERM.with(|t| stack.extend(t.borrow().iter().cloned()));
    LIBRARY_ROOTS.with(|t| stack.extend(t.borrow().values().cloned()));
    trace_globals(&mut stack);
    trace_sched(&mut stack, &mut frames);
    let mut seen_frames: Vec<*const Frame> = Vec::new();
    let garbage = HEAP.with(|hp| {
        let mut hp = hp.borrow_mut();
        let hp = &mut *hp;
        for m in hp.marks.iter_mut() {
            *m = false;
        }
        loop {
            while let Some(v) = stack.pop() {
                let h = match &v {
                    V::Obj(h) | V::Ptr(h, _) | V::Slice(h, _, _, _) | V::ByteSlice(h, _, _, _) => *h,
                    V::Tuple(t) => {
                        stack.extend(t.iter().cloned());
                        continue;
                    }
                    V::Frame(f) => {
                        frames.push(f.clone());
                        continue;
                    }
                    _ => continue,
                };
                if h == 0 || hp.marks[h as usize] {
                    continue;
                }
                hp.marks[h as usize] = true;
                trace_obj(&hp.objs[h as usize], &mut stack, &mut frames);
            }
            let Some(f) = frames.pop() else { break };
            let fp = Rc::as_ptr(&f);
            if seen_frames.contains(&fp) {
                continue;
            }
            seen_frames.push(fp);
            f.trace(&mut stack, &mut frames);
        }
        let mut garbage = Vec::new();
        for h in 1..hp.objs.len() {
            if !hp.marks[h] && !matches!(hp.objs[h], Obj::Free) {
                garbage.push(std::mem::replace(&mut hp.objs[h], Obj::Free));
                hp.free.push(h as H);
                hp.live -= 1;
            }
        }
        hp.since = 0;
        hp.threshold = if min_threshold() < 50_000 { min_threshold() } else { min_threshold().max(hp.live) };
        hp.collections += 1;
        garbage
    });
    drop(garbage);
}

pub(crate) fn trace_obj(o: &Obj, out: &mut Vec<V>, frames: &mut Vec<Rc<Frame>>) {
    match o {
        Obj::Free | Obj::Bytes(_) | Obj::NativeKey(_,_) => {}
        Obj::Vals(v) => out.extend(v.iter().cloned()),
        Obj::Map(m) => {
            for e in m.entries.iter() {
                let e = e.borrow();
                out.push(e.k.clone());
                out.push(e.v.clone());
            }
        }
        Obj::Box(_, v) => out.push(v.clone()),
        Obj::Func(f) => out.extend(f.env.iter().cloned()),
        Obj::Chan(c) => {
            out.extend(c.buf.iter().cloned());
            for w in c.recvq.iter().chain(c.sendq.iter()) {
                out.push(w.val.borrow().clone());
                if let Some(t) = w.task.upgrade() { trace_task(&t, out, frames); }
            }
        }
        Obj::Mutex(m) => {
            for t in m.waiters.iter() {
                trace_task(t, out, frames);
            }
        }
        Obj::WaitGroup(w) => {
            for t in w.waiters.iter() {
                trace_task(t, out, frames);
            }
        }
        Obj::Context(c) => {
            out.push(c.done.clone());
            out.push(c.err.clone());
            out.extend(c.children.iter().cloned());
            out.push(c.parent.clone());
            for h in c.hooks.values() { out.extend(h.roots.iter().cloned()); }
        }
        Obj::Iter(it) => {
            for e in it.entries.iter() {
                let e = e.borrow();
                out.push(e.k.clone());
                out.push(e.v.clone());
            }
            out.push(it.k.clone());
            out.push(it.v.clone());
        }
        Obj::Panic(p) => {
            out.push(p.value.clone());
            out.push(p.prev.clone());
        }
    }
}

impl Frame {
    fn trace(&self, out: &mut Vec<V>, frames: &mut Vec<Rc<Frame>>) {
        self.l.trace(out);
        out.push(self.panicking.borrow().clone());
        for link in [&self.parent, &self.a, &self.b] {
            if let Some(f) = link.borrow().as_ref() {
                frames.push(f.clone());
            }
        }
    }
}

pub fn trace_task(t: &Rc<Task>, out: &mut Vec<V>, frames: &mut Vec<Rc<Frame>>) {
    for p in t.native_roots.borrow().iter() { trace_data(*p, out); }
    if let Some(f) = t.frame.borrow().as_ref() {
        frames.push(f.clone());
    }
    out.extend(t.cleanup_roots.borrow().iter().cloned());
    out.extend(t.rv.borrow().iter().cloned());
    out.push(t.resume_panic.borrow().clone());
    out.push(t.cur_panic.borrow().clone());
}

/// Peak live objects and collections so far, for memory tests.
pub fn heap_stats() -> (usize, usize) {
    HEAP.with(|hp| {
        let hp = hp.borrow();
        (hp.peak, hp.collections)
    })
}

/// Live objects now.
pub fn heap_live() -> usize {
    HEAP.with(|hp| hp.borrow().live)
}

pub type KeyIndex = HashMap<Key, usize>;
