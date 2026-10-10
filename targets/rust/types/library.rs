//! Owned native library operations. The owner never publishes source objects.
use super::*;
use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::task::{Context as FutureContext, Poll, Waker};
use std::time::Duration;
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ErrorKind {
    Source,
    Canceled,
    DeadlineExceeded,
    SourcePanic,
    SourceFatal,
    HostFault,
    InvalidArgument,
    /// The handle was closed, or closed while the call was queued.
    Closed,
    /// An earlier call on the instance panicked or faulted.
    Poisoned,
    /// Handles from different instances were passed to one call.
    InstanceMismatch,
}
#[derive(Clone, Debug)]
pub struct LibraryError {
    pub kind: ErrorKind,
    pub message: String,
    pub diagnostic: Vec<u8>,
    pub fields: std::collections::BTreeMap<String, HostWire>,
}
impl LibraryError {
    pub fn new(kind: ErrorKind, message: impl Into<String>) -> Self {
        let message = message.into();
        Self {
            kind,
            diagnostic: message.as_bytes().to_vec(),
            message,
            fields: Default::default(),
        }
    }
    pub fn from_bytes(kind: ErrorKind, diagnostic: Vec<u8>) -> Self {
        Self {
            kind,
            message: String::from_utf8_lossy(&diagnostic).into_owned(),
            diagnostic,
            fields: Default::default(),
        }
    }
    pub fn text(&self, name: &str) -> String {
        match self.fields.get(name) {
            Some(HostWire::Bytes(b)) => String::from_utf8_lossy(b).into_owned(),
            _ => String::new(),
        }
    }
    pub fn integer(&self, name: &str) -> i64 {
        match self.fields.get(name) {
            Some(HostWire::Int(i)) => *i,
            _ => 0,
        }
    }
    pub fn normalize_cause(mut self) -> Self {
        self.kind = match self.text("CauseCategory").as_str() {
            "canceled" => ErrorKind::Canceled,
            "deadline_exceeded" => ErrorKind::DeadlineExceeded,
            _ => self.kind,
        };
        self
    }
}
impl std::fmt::Display for LibraryError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{:?}: {}", self.kind, self.message)
    }
}
impl std::error::Error for LibraryError {}
#[derive(Clone, Default)]
pub struct Cancellation(Arc<AtomicBool>);
impl Cancellation {
    pub fn cancel(&self) {
        self.0.store(true, Ordering::Release)
    }
    pub fn is_canceled(&self) -> bool {
        self.0.load(Ordering::Acquire)
    }
}
#[derive(Clone)]
pub struct ProviderRequest {
    pub payload: Vec<u8>,
    pub cancellation: Cancellation,
}
#[derive(Debug)]
pub enum ProviderError {
    Rejected(String),
    Fault(String),
}
/// Return only after all provider resources are released, including cancellation.
pub type Provider =
    Arc<dyn Fn(ProviderRequest) -> Result<Vec<u8>, ProviderError> + Send + Sync + 'static>;
#[derive(Clone, Default)]
pub struct CallOptions {
    pub cancellation: Cancellation,
    pub providers: std::collections::BTreeMap<String, Provider>,
}
struct Completion<T> {
    result: Option<Result<T, LibraryError>>,
    waker: Option<Waker>,
}
struct OperationState<T> {
    completion: Mutex<Completion<T>>,
    wake: Condvar,
    settled: AtomicBool,
}
pub struct Operation<T> {
    state: Arc<OperationState<T>>,
    cancel: Cancellation,
}
impl<T> Operation<T> {
    pub fn cancel(&self) {
        self.cancel.cancel()
    }
    pub fn cancellation(&self) -> Cancellation {
        self.cancel.clone()
    }
    pub fn wait(self) -> Result<T, LibraryError> {
        let mut c = self
            .state
            .completion
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        loop {
            if let Some(r) = c.result.take() {
                return r;
            };
            c = self.state.wake.wait(c).unwrap_or_else(|e| e.into_inner())
        }
    }
}
static LIBRARY_SERIAL: Mutex<()> = Mutex::new(());
impl<T: Send + 'static> Operation<T> {
    pub fn submit(
        options: CallOptions,
        body: impl FnOnce(CallOptions) -> Result<T, LibraryError> + Send + 'static,
    ) -> Self {
        Self::submit_handles(options, vec![], None, move |options, _inst, _fresh| body(options))
    }
    fn submit_raw(
        options: CallOptions,
        body: impl FnOnce(CallOptions) -> Result<T, LibraryError> + Send + 'static,
    ) -> Self {
        let state = Arc::new(OperationState {
            completion: Mutex::new(Completion {
                result: None,
                waker: None,
            }),
            wake: Condvar::new(),
            settled: AtomicBool::new(false),
        });
        let cancel = options.cancellation.clone();
        let output = state.clone();
        let launch = std::thread::Builder::new()
            .name("goalchemy-library-call".into())
            .spawn(move || {
                let result = body(options);
                let waker = {
                    let mut c = output.completion.lock().unwrap_or_else(|e| e.into_inner());
                    c.result = Some(result);
                    output.settled.store(true, Ordering::Release);
                    c.waker.take()
                };
                output.wake.notify_all();
                if let Some(w) = waker {
                    w.wake()
                }
            });
        if let Err(e) = launch {
            state.settled.store(true, Ordering::Release);
            state.completion.lock().unwrap().result = Some(Err(LibraryError::new(
                ErrorKind::HostFault,
                format!("call submission: {e}"),
            )))
        };
        Self { state, cancel }
    }
}
impl<T> Future for Operation<T> {
    type Output = Result<T, LibraryError>;
    fn poll(self: Pin<&mut Self>, cx: &mut FutureContext<'_>) -> Poll<Self::Output> {
        let mut c = self
            .state
            .completion
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        if let Some(r) = c.result.take() {
            Poll::Ready(r)
        } else {
            c.waker = Some(cx.waker().clone());
            Poll::Pending
        }
    }
}
/// Dropping a future requests stop. Use wait/await to observe actual cleanup.
impl<T> Drop for Operation<T> {
    fn drop(&mut self) {
        if !self.state.settled.load(Ordering::Acquire) {
            self.cancel.cancel()
        }
    }
}
pub fn library_bytes(v: Vec<u8>) -> V {
    let n = library_length(v.len());
    V::ByteSlice(byte_array(v).h(), 0, n, n)
}
pub fn library_length(n: usize) -> u32 {
    u32::try_from(n)
        .unwrap_or_else(|_| library_invalid("native collection length exceeds source descriptor"))
}
pub fn library_byte_output(v: &V) -> Vec<u8> {
    let (h, o, n, _, _) = slice_parts(v);
    if h == 0 {
        vec![]
    } else {
        byte_snapshot(h, o as usize, n as usize)
    }
}
pub fn library_strings(v: V) -> Vec<Vec<u8>> {
    let (h, o, n, _, _) = slice_parts(&v);
    (0..n as usize)
        .map(|i| slot(h, o as usize + i).bytes().to_vec())
        .collect()
}
fn sequence_step(t: &Rc<Task>, f: &Rc<Frame>) {
    match f.pc.get() {
        0 => {
            f.pc.set(1);
            call(t, f.l.g(0));
        }
        1 => {
            f.pc.set(2);
            let make = f.prim.borrow_mut().take().unwrap();
            make(t);
        }
        _ => {
            let rv = t.rv.borrow().clone();
            f.l.s(1, tuple(rv));
            ret(t, f);
        }
    }
}
fn sequence_results(f: &Frame) -> Vec<V> {
    match f.l.g(1) {
        V::Tuple(x) => x.to_vec(),
        _ => vec![],
    }
}
fn call_step(t: &Rc<Task>, f: &Rc<Frame>) {
    match f.pc.get() {
        0 => {
            f.pc.set(1);
            let make = f.prim.borrow_mut().take().unwrap();
            make(t);
        }
        _ => {
            let rv = t.rv.borrow().clone();
            f.l.s(1, tuple(rv));
            ret(t, f);
        }
    }
}
/// A root frame that calls entry and keeps its results, without initialization.
pub fn library_call(entry: impl FnOnce() -> V + 'static) -> V {
    let f = Frame::new(2, call_step, Some(sequence_results));
    f.prim.replace(Some(Box::new(move |t| call(t, entry()))));
    V::Frame(f)
}
pub fn library_sequence(init: V, entry: impl FnOnce() -> V + 'static) -> V {
    let f = Frame::new(2, sequence_step, Some(sequence_results));
    f.l.s(0, init);
    f.prim.replace(Some(Box::new(move |t| call(t, entry()))));
    V::Frame(f)
}
thread_local! {pub static LIBRARY_PROVIDERS:RefCell<std::collections::BTreeMap<String,Provider>>=RefCell::new(Default::default());}
pub fn library_owner<T>(
    globals: usize,
    zero: fn(),
    options: CallOptions,
    factory: impl FnOnce(V) -> V,
    output: impl FnOnce(Vec<V>) -> Result<T, LibraryError>,
) -> Result<T, LibraryError> {
    let mut options = Some(options);
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        library_install();
        init_globals(globals);
        zero();
        let c = new_child(background());
        let _context_root = temp_root(&[c.clone()]);
        let options = options.take().unwrap();
        LIBRARY_PROVIDERS.with(|p| *p.borrow_mut() = options.providers);
        let cancel = options.cancellation;
        let publication_cancel = cancel.clone();
        let ctx = c.clone();
        set_host_poll(Some(Rc::new(move || {
            if cancel.is_canceled() {
                cancel_ctx(ctx.clone(), context_canceled())
            }
        })));
        let frame = factory(c);
        let main = sched(|s| s.cur.clone());
        main.frame.replace(Some(frame.frame()));
        ready(&main);
        library_run(&main);
        let rv = main.rv.borrow().clone();
        let output = output(rv);
        if publication_cancel.is_canceled() {
            match output {
                Err(e) if e.kind == ErrorKind::HostFault => Err(e),
                Err(e) if e.kind == ErrorKind::Canceled => Err(e),
                _ => Err(LibraryError::new(
                    ErrorKind::Canceled,
                    "active call canceled",
                )),
            }
        } else {
            output
        }
    }))
    .map_err(library_classify)
    .and_then(|r| r);
    set_host_poll(None);
    let retirement = retire_owner();
    LIBRARY_PROVIDERS.with(|p| p.borrow_mut().clear());
    #[cfg(feature = "native")]
    crypto_retire();
    match retirement {
        Err(e) => Err(LibraryError::new(ErrorKind::HostFault, e)),
        Ok(()) => result,
    }
}

fn library_classify(e: Box<dyn std::any::Any + Send>) -> LibraryError {
    if let Some(e) = e.downcast_ref::<LibraryInvalid>() {
        return LibraryError::new(ErrorKind::InvalidArgument, e.0.clone());
    };
    match classify(e) {
        HostError::Fault(s) => LibraryError::new(ErrorKind::HostFault, s),
        HostError::Fatal(s) => LibraryError::new(ErrorKind::SourceFatal, s),
        HostError::SourcePanic(b) => LibraryError::from_bytes(ErrorKind::SourcePanic, b),
        HostError::Blocked => LibraryError::new(ErrorKind::SourceFatal, "blocked source"),
    }
}

struct LibraryInvalid(String);
pub fn library_invalid(s: &str) -> ! {
    std::panic::resume_unwind(Box::new(LibraryInvalid(s.into())))
}

/// Boundary warnings such as leaked handles and abandoned goroutines.
pub type WarningHandler = Arc<dyn Fn(&str) + Send + Sync + 'static>;
static LIBRARY_WARN: Mutex<Option<WarningHandler>> = Mutex::new(None);
pub fn set_library_warn(handler: Option<WarningHandler>) {
    *LIBRARY_WARN.lock().unwrap_or_else(|e| e.into_inner()) = handler;
}
fn library_warn(message: &str) {
    let handler = LIBRARY_WARN.lock().unwrap_or_else(|e| e.into_inner()).clone();
    match handler {
        Some(h) => h(message),
        None => eprintln!("goalchemy: warning: {message}"),
    }
}
type Job = Box<dyn FnOnce() -> bool + Send + 'static>;
#[derive(Default)]
struct InstanceState {
    live: usize,
    poisoned: bool,
    retired: bool,
    next: u64,
}
/// Persistent source state behind a family of handles. Its source heap,
/// globals and native keys live on one owner thread, which runs every call on
/// the instance and exits when the last handle is released.
pub struct Instance {
    jobs: Mutex<Option<std::sync::mpsc::Sender<Job>>>,
    owner: Mutex<Option<std::thread::JoinHandle<()>>>,
    state: Mutex<InstanceState>,
    domain: std::sync::atomic::AtomicU64,
}
impl Instance {
    fn state(&self) -> std::sync::MutexGuard<'_, InstanceState> {
        self.state.lock().unwrap_or_else(|e| e.into_inner())
    }
    fn start(self: &Arc<Self>) -> Result<(), LibraryError> {
        let (tx, rx) = std::sync::mpsc::channel::<Job>();
        let owner = std::thread::Builder::new()
            .name("goalchemy-library-owner".into())
            .stack_size(16 * 1024 * 1024)
            .spawn(move || {
                while let Ok(job) = rx.recv() {
                    if job() {
                        break;
                    }
                }
            })
            .map_err(|e| LibraryError::new(ErrorKind::HostFault, format!("owner submission: {e}")))?;
        *self.jobs.lock().unwrap_or_else(|e| e.into_inner()) = Some(tx);
        *self.owner.lock().unwrap_or_else(|e| e.into_inner()) = Some(owner);
        Ok(())
    }
    /// Runs job on the owner thread and waits for it; requires the library
    /// reservation. The owner thread is joined once it exits.
    fn run<R: Send + 'static>(&self, job: impl FnOnce() -> (R, bool) + Send + 'static) -> Result<R, LibraryError> {
        let (tx, rx) = std::sync::mpsc::channel();
        let sent = self.jobs.lock().unwrap_or_else(|e| e.into_inner()).as_ref().map(|jobs| {
            jobs.send(Box::new(move || {
                let (r, exit) = job();
                let _ = tx.send(r);
                exit
            }))
        });
        if !matches!(sent, Some(Ok(()))) {
            return Err(LibraryError::new(ErrorKind::HostFault, "instance owner exited"));
        }
        let result = rx.recv();
        if self.state().retired {
            self.jobs.lock().unwrap_or_else(|e| e.into_inner()).take();
            if let Some(owner) = self.owner.lock().unwrap_or_else(|e| e.into_inner()).take() {
                if let Err(e) = owner.join() {
                    return Err(LibraryError::new(ErrorKind::HostFault, native_fault_message(&e)));
                }
            }
        }
        result.map_err(|_| LibraryError::new(ErrorKind::HostFault, "instance owner failed"))
    }
}
/// The host's reference to one source object of an instance. Dropping the
/// last reference without close releases it with a warning; source Close
/// never runs from Drop.
pub struct Handle {
    inst: Arc<Instance>,
    id: u64,
    name: &'static str,
    closed: AtomicBool,
    active: Mutex<Option<Cancellation>>,
}
impl Handle {
    pub fn same(a: &Arc<Handle>, b: &Arc<Handle>) -> bool {
        Arc::ptr_eq(a, b)
    }
}
impl Drop for Handle {
    fn drop(&mut self) {
        if self.closed.swap(true, Ordering::AcqRel) {
            return;
        }
        let inst = self.inst.clone();
        let (id, name) = (self.id, self.name);
        let _ = std::thread::Builder::new()
            .name("goalchemy-library-release".into())
            .spawn(move || {
                let _lock = LIBRARY_SERIAL.lock().unwrap_or_else(|e| e.into_inner());
                if inst.state().retired {
                    return;
                }
                let owner = inst.clone();
                let _ = inst.run(move || {
                    let present = LIBRARY_ROOTS.with(|r| r.borrow().contains_key(&id));
                    if present {
                        library_warn(&format!("{name} handle was not closed; releasing it without running Close"));
                    }
                    ((), library_release(&owner, id))
                });
            });
    }
}
thread_local! {static LIBRARY_OBJECTS:RefCell<std::collections::BTreeMap<(H,u32),(u64,std::sync::Weak<Handle>)>>=RefCell::new(Default::default());}
fn identity(v: &V) -> (H, u32) {
    match v {
        V::Ptr(h, o) => (*h, *o),
        _ => (v.h(), u32::MAX),
    }
}
/// Releases a handle on the owner thread; returns true when the instance retired.
fn library_release(inst: &Arc<Instance>, id: u64) -> bool {
    let removed = LIBRARY_ROOTS.with(|r| r.borrow_mut().remove(&id));
    if removed.is_none() {
        return inst.state().retired;
    }
    LIBRARY_OBJECTS.with(|o| o.borrow_mut().retain(|_, (x, _)| *x != id));
    let mut state = inst.state();
    state.live -= 1;
    if state.live == 0 {
        state.retired = true;
        drop(state);
        LIBRARY_ROOTS.with(|r| r.borrow_mut().clear());
        LIBRARY_OBJECTS.with(|o| o.borrow_mut().clear());
        #[cfg(feature = "native")]
        crypto_retire();
        return true;
    }
    false
}
/// Returns the host handle for a source pointer, reusing the live handle of
/// the same pointer so host identity follows source identity.
pub fn library_wrap(inst: &Arc<Instance>, v: V, name: &'static str) -> Option<Arc<Handle>> {
    if v.is_nil() {
        return None;
    }
    let key = identity(&v);
    if let Some(h) = LIBRARY_OBJECTS.with(|o| o.borrow().get(&key).and_then(|(_, w)| w.upgrade())) {
        if !h.closed.load(Ordering::Acquire) {
            return Some(h);
        }
    }
    let id = {
        let mut state = inst.state();
        state.next += 1;
        state.live += 1;
        state.next
    };
    LIBRARY_ROOTS.with(|r| r.borrow_mut().insert(id, v));
    let h = Arc::new(Handle { inst: inst.clone(), id, name, closed: AtomicBool::new(false), active: Mutex::new(None) });
    LIBRARY_OBJECTS.with(|o| o.borrow_mut().insert(key, (id, Arc::downgrade(&h))));
    Some(h)
}
/// The source object of a handle argument, or nil.
pub fn library_obj(h: &Option<Arc<Handle>>) -> V {
    h.as_ref().and_then(|h| LIBRARY_ROOTS.with(|r| r.borrow().get(&h.id).cloned())).unwrap_or(V::Nil)
}
/// One call on the owner thread: fresh calls initialize globals, instance
/// calls keep them, the heap and native keys.
pub fn library_owner_call<T>(
    globals: usize,
    zero: fn(),
    options: CallOptions,
    inst: &Arc<Instance>,
    fresh: bool,
    factory: impl FnOnce(V) -> V,
    output: impl FnOnce(Vec<V>, &Arc<Instance>) -> Result<T, LibraryError>,
) -> Result<T, LibraryError> {
    let mut options = Some(options);
    let mut left = 0;
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        library_install();
        if fresh {
            init_globals(globals);
            zero();
            inst.domain.store(sched(|s| s.owner), Ordering::Release);
        } else {
            let domain = inst.domain.load(Ordering::Acquire);
            sched(|s| s.domain = domain);
        }
        let c = new_child(background());
        let _context_root = temp_root(&[c.clone()]);
        let options = options.take().unwrap();
        LIBRARY_PROVIDERS.with(|p| *p.borrow_mut() = options.providers);
        let cancel = options.cancellation;
        let publication_cancel = cancel.clone();
        let ctx = c.clone();
        set_host_poll(Some(Rc::new(move || {
            if cancel.is_canceled() {
                cancel_ctx(ctx.clone(), context_canceled())
            }
        })));
        let frame = factory(c);
        let main = sched(|s| s.cur.clone());
        main.frame.replace(Some(frame.frame()));
        ready(&main);
        library_run(&main);
        let rv = main.rv.borrow().clone();
        if !fresh && publication_cancel.is_canceled() {
            return Err(LibraryError::new(ErrorKind::Canceled, "active call canceled"));
        }
        let output = output(rv, inst);
        left = sched(|s| s.tasks.iter().filter(|t| !Rc::ptr_eq(t, &main) && !t.done.get()).count());
        if publication_cancel.is_canceled() {
            match output {
                Err(e) if e.kind == ErrorKind::HostFault => Err(e),
                Err(e) if e.kind == ErrorKind::Canceled => Err(e),
                _ => Err(LibraryError::new(ErrorKind::Canceled, "active call canceled")),
            }
        } else {
            output
        }
    }))
    .map_err(library_classify)
    .and_then(|r| r);
    set_host_poll(None);
    let retirement = retire_owner();
    LIBRARY_PROVIDERS.with(|p| p.borrow_mut().clear());
    let keep = { inst.state().live > 0 };
    if !keep {
        #[cfg(feature = "native")]
        crypto_retire();
    } else if left > 0 && result.is_ok() {
        library_warn(&format!("{left} goroutine(s) still running when a handle call returned were abandoned"));
    }
    if !fresh {
        if let Err(e) = &result {
            if matches!(e.kind, ErrorKind::SourcePanic | ErrorKind::SourceFatal | ErrorKind::HostFault) {
                inst.state().poisoned = true;
            }
        }
    }
    match retirement {
        Err(e) => Err(LibraryError::new(ErrorKind::HostFault, e)),
        Ok(()) => result,
    }
}
impl<T: Send + 'static> Operation<T> {
    /// Submits a call with handle arguments, receiver first. A call without a
    /// live handle creates a fresh owner, which becomes an instance when its
    /// results include handles. A closing call runs body on the closing
    /// handle's instance and then releases that handle.
    pub fn submit_handles(
        options: CallOptions,
        handles: Vec<Option<Arc<Handle>>>,
        closing: Option<Arc<Handle>>,
        body: impl FnOnce(CallOptions, Arc<Instance>, bool) -> Result<T, LibraryError> + Send + 'static,
    ) -> Self {
        let first = handles.iter().flatten().next().cloned();
        let me = match (&first, &closing) {
            (Some(f), Some(c)) if Arc::ptr_eq(f, c) => None,
            _ => first,
        };
        if let Some(me) = &me {
            if me.closed.load(Ordering::Acquire) {
                return Self::settled(Err(LibraryError::new(ErrorKind::Closed, "handle is closed")));
            }
        }
        let caller_options = options.clone();
        Self::submit_raw(caller_options, move |_| {
            let cancellation = options.cancellation.clone();
            let lock = loop {
                if let Some(me) = &me {
                    if me.closed.load(Ordering::Acquire) {
                        return Err(LibraryError::new(ErrorKind::Closed, "handle is closed"));
                    }
                }
                if cancellation.is_canceled() {
                    return Err(LibraryError::new(ErrorKind::Canceled, "queued call canceled"));
                }
                match LIBRARY_SERIAL.try_lock() {
                    Ok(lock) => break lock,
                    Err(std::sync::TryLockError::WouldBlock) => std::thread::sleep(Duration::from_millis(2)),
                    Err(_) => return Err(LibraryError::new(ErrorKind::HostFault, "poisoned library reservation")),
                }
            };
            let reservation = reserve_entry().map_err(|e| LibraryError::new(ErrorKind::HostFault, e.0))?;
            let mut inst: Option<Arc<Instance>> = None;
            for h in handles.iter().flatten() {
                let is_closing = closing.as_ref().map_or(false, |c| Arc::ptr_eq(c, h));
                if !is_closing && h.closed.load(Ordering::Acquire) {
                    return Err(LibraryError::new(ErrorKind::Closed, "handle is closed"));
                }
                if h.inst.state().retired {
                    return Err(LibraryError::new(ErrorKind::Closed, "handle is closed"));
                }
                match &inst {
                    Some(i) if !Arc::ptr_eq(i, &h.inst) => {
                        return Err(LibraryError::new(ErrorKind::InstanceMismatch, "handles of different instances"))
                    }
                    _ => inst = Some(h.inst.clone()),
                }
            }
            let fresh = inst.is_none();
            let inst = match inst {
                Some(i) => i,
                None => {
                    let i = Arc::new(Instance {
                        jobs: Mutex::new(None),
                        owner: Mutex::new(None),
                        state: Mutex::new(InstanceState::default()),
                        domain: Default::default(),
                    });
                    i.start()?;
                    i
                }
            };
            let poisoned = inst.state().poisoned;
            if poisoned && closing.is_none() {
                return Err(LibraryError::new(ErrorKind::Poisoned, "instance is poisoned"));
            }
            if let Some(me) = &me {
                *me.active.lock().unwrap_or_else(|e| e.into_inner()) = Some(cancellation.clone());
                if me.closed.load(Ordering::Acquire) {
                    cancellation.cancel();
                }
            }
            let owner = inst.clone();
            let closing_id = closing.as_ref().map(|c| c.id);
            let result = inst.run(move || {
                let r = body(options, owner.clone(), fresh);
                let exit = match closing_id {
                    Some(id) => library_release(&owner, id),
                    None => {
                        let mut state = owner.state();
                        if state.live == 0 {
                            state.retired = true;
                        }
                        state.retired
                    }
                };
                (r, exit)
            });
            if let Some(me) = &me {
                me.active.lock().unwrap_or_else(|e| e.into_inner()).take();
            }
            drop(reservation);
            drop(lock);
            result.and_then(|r| r)
        })
    }
    fn settled(result: Result<T, LibraryError>) -> Self {
        let state = Arc::new(OperationState {
            completion: Mutex::new(Completion { result: Some(result), waker: None }),
            wake: Condvar::new(),
            settled: AtomicBool::new(true),
        });
        Self { state, cancel: Cancellation::default() }
    }
}
/// Fails calls queued on h, cancels its active call, runs the source Close
/// through body when the instance is healthy, then releases h.
pub fn library_close(h: &Arc<Handle>, body: impl FnOnce(CallOptions, Arc<Instance>, bool) -> Result<(), LibraryError> + Send + 'static) -> Operation<()> {
    if h.closed.swap(true, Ordering::AcqRel) {
        return Operation::settled(Ok(()));
    }
    if let Some(active) = h.active.lock().unwrap_or_else(|e| e.into_inner()).as_ref() {
        active.cancel();
    }
    Operation::submit_handles(CallOptions::default(), vec![Some(h.clone())], Some(h.clone()), move |options, inst, fresh| {
        if inst.state().poisoned {
            return Ok(());
        }
        body(options, inst, fresh)
    })
}
