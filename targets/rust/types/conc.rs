//! Channel and synchronization objects.
use super::*;
use std::collections::VecDeque;

pub type Waiter = Rc<WaiterData>;
pub struct WaiterData {
    pub task: std::rc::Weak<Task>,
    pub val: RefCell<V>,
    pub sel: Option<Rc<Cell<bool>>>,
    pub idx: usize,
    pub retired: Cell<bool>,
}
impl WaiterData {
    pub fn live(&self) -> bool {
        !self.retired.get() && self.task.upgrade().map_or(false, |t| task_live(&t)) && self.sel.as_ref().map_or(true, |s| !s.get())
    }
    pub fn clear(&self) { self.retired.set(true); self.val.replace(V::Nil); }
}
pub fn new_waiter(t: &Rc<Task>, val: V, sel: Option<Rc<Cell<bool>>>, idx: usize) -> Waiter {
    check_task(t);
    Rc::new(WaiterData { task: Rc::downgrade(t), val: RefCell::new(val), sel, idx, retired: Cell::new(false) })
}
pub struct Chan {
    pub buf: VecDeque<V>,
    pub size: usize,
    pub closed: bool,
    pub recvq: VecDeque<Waiter>,
    pub sendq: VecDeque<Waiter>,
    pub zero: fn() -> V,
}

pub struct Mutex {
    pub locked: bool,
    pub waiters: VecDeque<Rc<Task>>,
}

pub struct WaitGroup {
    pub n: i64,
    pub waiters: Vec<Rc<Task>>,
}

pub struct ContextHook { pub roots: Vec<V>, pub call: Box<dyn FnOnce()> }
pub struct Context {
    pub owner: u64, pub thread: std::thread::ThreadId,
    pub parent: V, pub deadline: Option<i64>, pub timer: Option<i64>,
    pub hooks: std::collections::BTreeMap<u64, ContextHook>, pub hook_sequence: u64,
    pub done: V,
    pub err: V,
    pub children: Vec<V>,
}

pub fn with_chan<R>(c: &V, f: impl FnOnce(&mut Chan) -> R) -> R {
    with(c.h(), |o| match o {
        Obj::Chan(x) => f(x),
        _ => fault("channel expected"),
    })
}

pub fn new_mutex() -> V {
    V::Obj(alloc(Obj::Mutex(Mutex { locked: false, waiters: VecDeque::new() })))
}

pub fn new_waitgroup() -> V {
    V::Obj(alloc(Obj::WaitGroup(WaitGroup { n: 0, waiters: Vec::new() })))
}

/// Copies an opaque value object (sync.Mutex, sync.WaitGroup).
pub fn opaque_clone(x: &V) -> V {
    let o = with(x.h(), |o| match o {
        Obj::Mutex(m) => Obj::Mutex(Mutex { locked: m.locked, waiters: m.waiters.clone() }),
        Obj::WaitGroup(w) => Obj::WaitGroup(WaitGroup { n: w.n, waiters: w.waiters.clone() }),
        _ => fault("opaque value expected"),
    });
    V::Obj(alloc(o))
}

pub fn opaque_set(d: &V, src: &V) {
    let o = with(src.h(), |o| match o {
        Obj::Mutex(m) => Obj::Mutex(Mutex { locked: m.locked, waiters: m.waiters.clone() }),
        Obj::WaitGroup(w) => Obj::WaitGroup(WaitGroup { n: w.n, waiters: w.waiters.clone() }),
        _ => fault("opaque value expected"),
    });
    let old = with(d.h(), |x| std::mem::replace(x, o));
    drop(old);
}
