//! Tasks and resumable frames. A suspending function compiles to a Frame
//! whose locals live in an unrooted Fr traced through its task; step runs
//! it until it returns or reaches a pause point. The scheduler state lives
//! here so the collector can trace every task; the scheduling operations are
//! in core.task.spawn.
use super::*;
use std::collections::{VecDeque, BTreeMap};
use std::sync::{Arc, Mutex as NativeMutex, Condvar};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Instant, Duration};
use std::thread::ThreadId;

static OWNER_SEQUENCE: AtomicU64 = AtomicU64::new(1);

#[derive(Debug)]
pub struct HostFault(pub String);
#[derive(Debug)]
pub struct HostFatal(pub String);
#[derive(Debug)]
pub struct SourceStackFatal;

/// The only values permitted across the native boundary. No source V, heap
/// handle, descriptor, frame, closure or native key is a wire value.
#[derive(Clone, Debug, PartialEq)]
pub enum HostWire {
    Nil, Bool(bool), Int(i64), Bytes(Vec<u8>), Text(String),
    List(Vec<HostWire>), Record(BTreeMap<String, HostWire>),
}
#[derive(Clone, Debug)]
pub enum HostRecord { Result(Vec<HostWire>), Fault(String) }
pub(crate) struct MailEntry { pub(crate) task: usize, pub(crate) record: Option<(u64, HostRecord)>, pub(crate) ack: bool }
pub(crate) struct MailState {
    pub(crate) closed: bool, pub(crate) version: u64, sequence: u64, pub(crate) fault: Option<String>,
    pub(crate) live: BTreeMap<u64, MailEntry>,
}
pub struct HostMailbox {
    pub owner: u64,
    pub(crate) state: NativeMutex<MailState>, wake: Condvar,
    /// Observable without taking the mailbox lock; useful for resource owners.
    pub waiting_cleanup: std::sync::atomic::AtomicBool,
    pub waiting_driver: std::sync::atomic::AtomicBool,
}
impl HostMailbox {
    fn new(owner: u64) -> Arc<Self> {
        Arc::new(Self { owner, state: NativeMutex::new(MailState {
            closed: false, version: 0, sequence: 0, fault: None, live: BTreeMap::new(),
        }), wake: Condvar::new(), waiting_cleanup: std::sync::atomic::AtomicBool::new(false), waiting_driver: std::sync::atomic::AtomicBool::new(false) })
    }
    pub(crate) fn lock(&self) -> std::sync::MutexGuard<'_, MailState> {
        match self.state.lock() {
            Ok(s) => s,
            Err(p) => { let mut s = p.into_inner(); s.fault.get_or_insert("poisoned host mailbox".into()); s }
        }
    }
    pub fn counts(&self) -> (usize, usize, usize) {
        let s = self.lock();
        (s.live.len(), s.live.values().filter(|x| x.record.is_some()).count(), s.live.values().filter(|x| x.ack).count())
    }
    pub fn notify(&self) { let mut s = self.lock(); s.version = s.version.wrapping_add(1); self.wake.notify_all(); }
    /// Predicate and version use the same mutex as publication. Spurious wakes
    /// recheck the predicate. Cap each native timeout instead of adding i64::MAX
    /// nanoseconds to an Instant (which can overflow on some platforms).
    pub fn wait(&self, observed: u64, timeout: Option<Duration>) {
        let mut s = self.lock();
        let started = Instant::now();
        while !s.closed && s.version == observed {
            self.waiting_driver.store(true, Ordering::Release);
            let r = if let Some(timeout) = timeout {
                let elapsed = Instant::now().checked_duration_since(started).unwrap_or_default();
                let Some(left) = timeout.checked_sub(elapsed) else { break };
                if left.is_zero() { break }
                match self.wake.wait_timeout(s, left.min(Duration::from_secs(60))) {
                    Ok((s, _)) => Ok(s), Err(p) => Err(p.into_inner().0),
                }
            } else { self.wake.wait(s).map_err(|p| p.into_inner()) };
            s = match r { Ok(s) => s, Err(mut s) => { s.fault.get_or_insert("poisoned host mailbox wait".into()); break } };
        }
        self.waiting_driver.store(false, Ordering::Release);
    }
}
/// A token retains only an Arc mailbox and numeric identities. Dropping one is
/// not cleanup acknowledgement. Foreign task/operation records never consume a
/// registration or acknowledge someone else's resources.
#[derive(Clone)]
pub struct HostToken { pub mailbox: Arc<HostMailbox>, pub owner: u64, pub operation: u64, pub task: usize }
impl HostToken {
    fn applicable(&self, s: &MailState) -> bool {
        !s.closed && self.owner == self.mailbox.owner && s.live.get(&self.operation).map_or(false, |x| x.task == self.task)
    }
    pub fn publish(&self, record: HostRecord) {
        let mut s = self.mailbox.lock();
        if !self.applicable(&s) || s.live[&self.operation].record.is_some() { return }
        s.sequence = s.sequence.checked_add(1).expect("host publication sequence exhausted");
        let sequence = s.sequence;
        s.live.get_mut(&self.operation).unwrap().record = Some((sequence, record));
        s.version = s.version.wrapping_add(1); self.mailbox.wake.notify_all();
    }
    pub fn complete(&self, values: Vec<HostWire>) { self.publish(HostRecord::Result(values)) }
    pub fn fault(&self, message: impl Into<String>) { self.publish(HostRecord::Fault(message.into())) }
    /// Call only after work/transport/body/key/input cleanup, including unwind.
    pub fn acknowledge_cleanup(&self) {
        let mut s = self.mailbox.lock();
        if !self.applicable(&s) { return }
        s.live.get_mut(&self.operation).unwrap().ack = true;
        s.version = s.version.wrapping_add(1); self.mailbox.wake.notify_all();
    }
}

pub struct HostBoundary { pub context: V, pub deadline: Option<i64>, pub error: V }
pub struct HostPending {
    pub task: Rc<Task>, pub boundary: HostBoundary, pub roots: Vec<V>,
    pub canceled: bool,
    pub cancel: Option<Box<dyn FnOnce()>>,
    pub cleanup: Option<Box<dyn FnOnce()>>,
    pub decode: Option<Box<dyn FnOnce(Vec<HostWire>) -> Vec<V>>>,
    pub cancel_result: fn(V) -> Vec<V>,
}


pub type Step = fn(&Rc<Task>, &Rc<Frame>);
pub type Results = fn(&Frame) -> Vec<V>;

pub struct Frame {
    pub l: Fr,
    pub pc: Cell<u32>,
    pub parent: RefCell<Option<Rc<Frame>>>,
    pub panicking: RefCell<V>,
    pub step: Step,
    pub results: Results,
    /// Links used by runtime frames (defer runners).
    pub a: RefCell<Option<Rc<Frame>>>,
    pub b: RefCell<Option<Rc<Frame>>>,
    /// A host primitive run by harness frames.
    pub prim: RefCell<Option<Box<dyn FnOnce(&Rc<Task>)>>>,
}

fn no_results(_: &Frame) -> Vec<V> {
    Vec::new()
}

impl Frame {
    pub fn new(n: usize, step: Step, results: Option<Results>) -> Rc<Frame> {
        Rc::new(Frame {
            l: Fr::unrooted(n),
            pc: Cell::new(0),
            parent: RefCell::new(None),
            panicking: RefCell::new(V::Nil),
            step,
            results: results.unwrap_or(no_results),
            a: RefCell::new(None),
            b: RefCell::new(None),
            prim: RefCell::new(None),
        })
    }
}

pub struct Task {
    pub id: usize,
    pub owner: Cell<u64>,
    pub thread: ThreadId,
    pub retired: Cell<bool>,
    /// Explicitly traced captures for blocked cleanup callbacks.
    pub cleanup_roots: RefCell<Vec<V>>,
    pub frame: RefCell<Option<Rc<Frame>>>,
    pub rv: RefCell<Vec<V>>,
    pub blocked: Cell<bool>,
    pub done: Cell<bool>,
    pub resume_panic: RefCell<V>,
    pub cleanup: RefCell<Option<Box<dyn FnOnce()>>>,
    /// Recover state: the panic being handled by running deferred calls
    /// and the identity of the deferred function running.
    pub cur_panic: RefCell<V>,
    pub defer_target: Cell<i64>,
}

impl Task {
    pub fn new(id: usize, frame: Option<Rc<Frame>>) -> Rc<Task> {
        Rc::new(Task {
            id,
            owner: Cell::new(0), thread: std::thread::current().id(), retired: Cell::new(false),
            cleanup_roots: RefCell::new(Vec::new()),
            frame: RefCell::new(frame),
            rv: RefCell::new(Vec::new()),
            blocked: Cell::new(false),
            done: Cell::new(false),
            resume_panic: RefCell::new(V::Nil),
            cleanup: RefCell::new(None),
            cur_panic: RefCell::new(V::Nil),
            defer_target: Cell::new(-1),
        })
    }

    pub fn set_rv(&self, v: Vec<V>) {
        let old = std::mem::replace(&mut *self.rv.borrow_mut(), v);
        drop(old);
    }

    pub fn rv(&self, k: usize) -> V {
        self.rv.borrow()[k].clone()
    }
}

pub struct Timer {
    pub at: i64,
    pub seq: i64,
    pub task: Option<Rc<Task>>,
    pub f: Option<(fn(V), V)>,
}

pub struct Sched {
    pub runq: VecDeque<Rc<Task>>,
    pub cur: Rc<Task>,
    pub tasks: Vec<Rc<Task>>,
    pub next_id: usize,
    pub rng: i64,
    pub clock: i64,
    pub timers: Vec<Timer>,
    pub seq: i64,
    pub harness: bool,
    pub owner: u64, pub active: bool, pub retiring: bool,
    /// Identity shared by the schedulers of one library instance; native keys
    /// created by any of its calls remain usable by later calls.
    pub domain: u64,
    pub thread: ThreadId, pub epoch: Option<Instant>,
    pub mailbox: Arc<HostMailbox>, pub pending: BTreeMap<u64, HostPending>, pub operation: u64,
    pub contexts: Vec<V>,
    /// Independently removable roots also cover a decoder while it runs outside
    /// the scheduler borrow. Never hold a LIFO TempRoots for operation lifetime.
    pub host_roots: BTreeMap<u64, Vec<V>>,
}

thread_local! {
    pub static SCHED: RefCell<Sched> = RefCell::new(Sched::new(Task::new(0, None), false));
}

impl Sched {
    pub fn new(main: Rc<Task>, harness: bool) -> Sched {
        let owner = OWNER_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        main.owner.set(owner);
        Sched {
            runq: VecDeque::new(),
            cur: main.clone(),
            tasks: vec![main],
            next_id: 1,
            rng: seed(),
            clock: 0,
            timers: Vec::new(),
            seq: 0,
            harness,
            owner, domain: owner, active: true, retiring: false, thread: std::thread::current().id(), epoch: None,
            mailbox: HostMailbox::new(owner), pending: BTreeMap::new(), operation: 0,
            contexts: Vec::new(), host_roots: BTreeMap::new(),
        }
    }
}

pub fn seed() -> i64 {
    match std::env::var("GOALCHEMY_SEED").ok().and_then(|s| s.parse::<i64>().ok()) {
        Some(v) if v > 0 && v < (1i64 << 32) => v,
        _ => 1,
    }
}

/// Runs f with the scheduler state; f must not call generated code.
pub fn sched<R>(f: impl FnOnce(&mut Sched) -> R) -> R {
    SCHED.with(|s| f(&mut s.borrow_mut()))
}

/// The running task: the holder of recover state.
pub fn cur_task() -> Rc<Task> {
    sched(|s| s.cur.clone())
}

pub fn trace_sched(out: &mut Vec<V>, frames: &mut Vec<Rc<Frame>>) {
    SCHED.with(|s| {
        let s = s.borrow();
        trace_task(&s.cur, out, frames);
        for t in s.tasks.iter().chain(s.runq.iter()) {
            trace_task(t, out, frames);
        }
        out.extend(s.contexts.iter().cloned());
        for roots in s.host_roots.values() { out.extend(roots.iter().cloned()); }
        for p in s.pending.values() {
            trace_task(&p.task, out, frames);
            out.push(p.boundary.context.clone()); out.push(p.boundary.error.clone());
            out.extend(p.roots.iter().cloned());
        }
        for t in s.timers.iter() {
            if let Some(task) = &t.task {
                trace_task(task, out, frames);
            }
            if let Some((_, v)) = &t.f {
                out.push(v.clone());
            }
        }
    })
}

/// Pure checked conversion, shared by real sampling and anomalous-clock tests.
pub fn elapsed_nanos(nanos: u128, previous: i64) -> i64 { previous.max(nanos.min(i64::MAX as u128) as i64) }
pub fn deadline_after(now: i64, duration: i64) -> i64 { if duration <= 0 { now } else { now.saturating_add(duration) } }
pub fn clock_now() -> i64 {
    sched(|s| {
        if let Some(epoch) = s.epoch {
            let nanos = Instant::now().checked_duration_since(epoch).unwrap_or_default().as_nanos();
            s.clock = elapsed_nanos(nanos, s.clock);
        }
        s.clock
    })
}
/// Safe Rust cannot send Rc<Task> to a worker. The check still rejects stale or
/// foreign tasks on their creating thread before changing any source fields.
pub fn task_live(t: &Task) -> bool {
    if t.thread != std::thread::current().id() { host_fault("source task used on native thread") }
    if t.retired.get() { return false }
    SCHED.with(|s| { let s = s.borrow(); s.active && !s.retiring && t.owner.get() == s.owner && !t.done.get() })
}
pub fn check_task(t: &Task) { if !task_live(t) { host_fault("foreign or retired source task") } }
pub fn host_fault(message: &str) -> ! { std::panic::resume_unwind(Box::new(HostFault(message.into()))) }
