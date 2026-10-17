//! core.task.spawn and the cooperative scheduler. A task is a stack of
//! frames driven by a trampoline; exactly one task runs at a time and
//! runnable tasks are dispatched in FIFO order. Pause primitives either
//! complete immediately, leaving their results in the task's rv, or block
//! the task until another task or a timer readies it. Deferred calls,
//! panics, and recover are managed per task.
use super::*;
use std::time::{Instant, Duration};
use std::sync::atomic::Ordering;

/// Unwinding payload of a harness case whose task blocked forever.
pub struct BlockedPayload;

/// xorshift32 choice source, identical on every target.
pub fn choose(n: usize) -> usize {
    sched(|s| {
        let mut x = s.rng;
        x ^= (x << 13) & 0xFFFF_FFFF;
        x ^= ((x as u64) >> 17) as i64;
        x ^= (x << 5) & 0xFFFF_FFFF;
        s.rng = x;
        (x % n as i64) as usize
    })
}

pub fn ready(t: &Rc<Task>) {
    if task_live(t) { sched(|s| s.runq.push_back(t.clone())); }
}

pub fn block(t: &Rc<Task>) {
    check_task(t);
    t.blocked.set(true)
}

pub fn add_timer(d: i64, task: Option<Rc<Task>>, f: Option<(fn(V), V)>) {
    add_timer_at(deadline_after(clock_now(), d), task, f);
}
pub fn add_timer_at(at: i64, task: Option<Rc<Task>>, f: Option<(fn(V), V)>) -> i64 {
    sched(|s| {
        s.seq = s.seq.checked_add(1).unwrap_or_else(|| host_fault("timer sequence exhausted"));
        let seq = s.seq; s.timers.push(Timer { at, seq, task, f }); seq
    })
}
pub fn fatal(msg: &str) -> ! {
    let active = sched(|s| s.active && (s.epoch.is_some() || s.cur.frame.borrow().is_some()));
    if active { std::panic::resume_unwind(Box::new(HostFatal(msg.into()))); }
    out::stderr(format!("fatal error: {}\n", msg).as_bytes()); std::process::exit(2)
}
pub fn fire_due_host_timers() { fire_timers(true) }
fn fire_timers(real: bool) {
    let now = if real { clock_now() } else { sched(|s| s.timers.iter().map(|t| t.at).min().unwrap_or(s.clock)) };
    let due = sched(|s| {
        s.clock = s.clock.max(now);
        let (mut due, keep): (Vec<Timer>, Vec<Timer>) = s.timers.drain(..).partition(|t| t.at <= now);
        s.timers = keep; due.sort_by_key(|t| (t.at, t.seq)); due
    });
    // Detaching a batch removes its scheduler roots. Root the entire batch
    // before any callback can collect or cancel/remove another context/task.
    let mut values = Vec::new(); let mut frames = Vec::new();
    for timer in &due {
        if let Some((_, v)) = &timer.f { values.push(v.clone()); }
        if let Some(task) = &timer.task { trace_task(task, &mut values, &mut frames); }
    }
    values.extend(frames.into_iter().map(V::Frame));
    let _roots = temp_root(&values);
    for t in due { if let Some((f, v)) = t.f { f(v); } if let Some(task) = t.task { ready(&task); } }
}
fn next() -> Rc<Task> {
    loop {
        let real = sched(|s| s.epoch.is_some());
        if real { dispatch_host(); }
        let (t, timers, harness, pending, mailbox) = sched(|s| (s.runq.pop_front(), !s.timers.is_empty(), s.harness, !s.pending.is_empty(), s.mailbox.clone()));
        if let Some(t) = t { if task_live(&t) { return t } else { continue } }
        if real && (timers || pending) {
            let version = mailbox.lock().version;
            // Sampling the version before a second drain closes wake-before-wait.
            dispatch_host();
            if sched(|s| !s.runq.is_empty()) { continue }
            let now = clock_now();
            let deadline = sched(|s| s.timers.iter().map(|t| t.at).chain(s.pending.values().filter_map(|p| p.boundary.deadline.filter(|_| !p.canceled))).min());
            let wait = deadline.map(|d| Duration::from_nanos(d.saturating_sub(now).max(0) as u64));
            mailbox.wait(version, if host_poll_active() {Some(wait.unwrap_or(Duration::from_millis(5)).min(Duration::from_millis(5)))} else {wait});
            continue;
        }
        if !timers {
            if harness { std::panic::resume_unwind(Box::new(BlockedPayload)); }
            fatal("all goroutines are asleep - deadlock!");
        }
        fire_timers(false);
    }
}

fn run(t: &Rc<Task>) {
    sched(|s| s.cur = t.clone());
    check_task(t);
    t.blocked.set(false);
    let c = t.cleanup.borrow_mut().take();
    if let Some(c) = c { c(); }
    t.cleanup_roots.borrow_mut().clear();
    while !t.blocked.get() {
        if sched(|s| s.epoch.is_some()) { dispatch_host(); }
        let Some(f) = t.frame.borrow().clone() else { break };
        let native = native_active(&f);
        let rp = if native { V::Nil } else { t.resume_panic.replace(V::Nil) };
        if !rp.is_nil() {
            exit(t, &f, rp);
            continue;
        }
        if let Err(e) = catch(|| (f.step)(t, &f)) {
            exit(t, &f, e);
        }
    }
}

fn set_frame(t: &Task, f: Option<Rc<Frame>>) {
    let old = t.frame.replace(f);
    drop(old);
}

fn exit(t: &Rc<Task>, f: &Rc<Frame>, p: V) {
    // A native parent's await must observe a child/primitive panic so its
    // explicit async defer runner can handle it. Completed native bodies
    // clear their future before propagating an unhandled source panic.
    if !p.is_nil() && native_active(f) {
        t.resume_panic.replace(p);
        set_frame(t, Some(f.clone()));
        return;
    }
    if !p.is_nil() {
        let earlier = f.panicking.borrow().clone();
        chain_panic(&p, &earlier);
        f.panicking.replace(p);
    }
    set_frame(t, Some(f.clone()));
    if f.l.has_defers() {
        let r = Frame::new(2, defer_runner_step, None);
        r.a.replace(Some(f.clone()));
        r.parent.replace(Some(f.clone()));
        set_frame(t, Some(r));
        return;
    }
    finish(t, f)
}

fn finish(t: &Rc<Task>, f: &Rc<Frame>) {
    let p = f.panicking.borrow().clone();
    let parent = f.parent.borrow().clone();
    set_frame(t, parent.clone());
    let Some(par) = parent else {
        t.done.set(true);
        sched(|s| s.tasks.retain(|x| !Rc::ptr_eq(x, t)));
        if !p.is_nil() {
            raise(p);
        }
        return;
    };
    let is_child = par.step as usize == defer_runner_step as Step as usize
        && par.b.borrow().as_ref().map_or(false, |c| Rc::ptr_eq(c, f));
    if is_child {
        child_done(t, &par, p);
        return;
    }
    if !p.is_nil() {
        exit(t, &par, p);
        return;
    }
    t.set_rv((f.results)(f));
}

fn defer_runner_step(t: &Rc<Task>, r: &Rc<Frame>) {
    let tf = r.a.borrow().clone().unwrap();
    while let Some(d) = tf.l.pop_defer() {
        r.l.s(0, t.cur_panic.replace(tf.panicking.borrow().clone()));
        r.l.s(1, V::Int(t.defer_target.replace(d.fid)));
        if d.start {
            let fv = d.f.clone();
            match catch(move || callv(&fv, d.args)) {
                Err(e) => {
                    restore(t, r);
                    after(&tf, e);
                }
                Ok(c) => {
                    let c = c.frame();
                    r.b.replace(Some(c.clone()));
                    c.parent.replace(Some(r.clone()));
                    set_frame(t, Some(c));
                    return;
                }
            }
            continue;
        }
        let fv = d.f.clone();
        let res = catch(move || {
            callv(&fv, d.args);
        });
        restore(t, r);
        after(&tf, res.err().unwrap_or(V::Nil));
    }
    set_frame(t, Some(tf.clone()));
    finish(t, &tf);
}

fn restore(t: &Task, r: &Frame) {
    t.cur_panic.replace(r.l.g(0));
    t.defer_target.set(r.l.g(1).i());
}

fn child_done(t: &Rc<Task>, r: &Rc<Frame>, p: V) {
    restore(t, r);
    r.b.replace(None);
    set_frame(t, Some(r.clone()));
    let tf = r.a.borrow().clone().unwrap();
    after(&tf, p);
}

fn after(tf: &Frame, p: V) {
    if !p.is_nil() {
        let earlier = tf.panicking.borrow().clone();
        chain_panic(&p, &earlier);
        tf.panicking.replace(p);
        return;
    }
    let cur = tf.panicking.borrow().clone();
    if !cur.is_nil() && panic_recovered(&cur) {
        tf.panicking.replace(V::Nil);
    }
}

/// Pushes a callee frame: a pause point.
pub fn call(t: &Rc<Task>, child: V) {
    check_task(t);
    let c = child.frame();
    if sched(|s| s.epoch.is_some()) {
        if native_stack_depth(t) >= 512 { std::panic::resume_unwind(Box::new(SourceStackFatal)); }
    }
    c.parent.replace(t.frame.borrow().clone());
    set_frame(t, Some(c));
}

pub fn ret(t: &Rc<Task>, f: &Rc<Frame>) {
    exit(t, f, V::Nil)
}

fn sync_step(t: &Rc<Task>, f: &Rc<Frame>) {
    let args = match f.l.g(1) {
        V::Tuple(a) => a.to_vec(),
        _ => Vec::new(),
    };
    let r = callv(&f.l.g(0), args);
    f.l.s(3, r);
    ret(t, f)
}

fn sync_results(f: &Frame) -> Vec<V> {
    let r = f.l.g(3);
    match f.l.g(2).i() {
        0 => Vec::new(),
        1 => vec![r],
        _ => match r {
            V::Tuple(t) => t.to_vec(),
            _ => fault("results expected"),
        },
    }
}

/// Runs an ordinary call of f with n results as a frame.
pub fn sync_frame(f: V, args: Vec<V>, n: usize) -> V {
    let fr = Frame::new(4, sync_step, Some(sync_results));
    fr.l.s(0, f);
    fr.l.s(1, tuple(args));
    fr.l.s(2, V::Int(n as i64));
    V::Frame(fr)
}

fn adapt_code(env: &[V], args: Vec<V>) -> V {
    sync_frame(env[0].clone(), args, env[1].i() as usize)
}

/// Adapts an ordinary function value with n results to the resumable form.
pub fn adapt(f: V, n: usize) -> V {
    if f.is_nil() {
        return V::Nil;
    }
    func(fid_of(&f), adapt_code, vec![f, V::Int(n as i64)])
}

pub fn adapt_slice(x: V, n: usize) -> V {
    let V::Slice(h, o, l, _) = x else { fault("slice expected") };
    if h == 0 {
        return x;
    }
    let fs: Vec<V> = (0..l as usize).map(|k| slot(h, o as usize + k)).collect();
    let _root = temp_root(&fs);
    let vs: Vec<V> = fs.into_iter().map(|f| adapt(f, n)).collect();
    V::Slice(alloc(Obj::Vals(vs)), 0, l, l)
}

/// go f(args): starts a task running frame f.
pub fn spawn(f: V) {
    let fr = f.frame();
    sched(|s| {
        let t = Task::new(s.next_id, Some(fr));
        t.owner.set(s.owner);
        s.next_id += 1;
        s.tasks.push(t.clone());
        s.runq.push_back(t);
    })
}

/// go f(args) for an ordinary call: runs it as a frame in a new task.
pub fn spawn_call(f: V, args: Vec<V>) {
    let f = fnchk(f);
    spawn(sync_frame(f, args, 0))
}

fn install(main: Rc<Task>, harness: bool) {
    retire_owner().unwrap_or_else(|e| host_fault(&e));
    let old = SCHED.with(|s| std::mem::replace(&mut *s.borrow_mut(), Sched::new(main, harness)));
    drop(old);
}
#[derive(Debug, PartialEq)]
pub enum HostError { Fault(String), Fatal(String), SourcePanic(Vec<u8>), Blocked }
pub fn classify(e: Box<dyn std::any::Any + Send>) -> HostError {
    if let Some(p) = e.downcast_ref::<GoPanicPayload>() {
        let p = V::Obj(p.0); let _root = temp_root(&[p.clone()]);
        return match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| format_chain(&p))) {
            Ok(b) => HostError::SourcePanic(b),
            Err(e) if e.is::<SourceStackFatal>() || e.is::<HostFatal>() => classify(e),
            Err(e) if e.is::<GoPanicPayload>() => HostError::Fault("source panic while formatting unrecovered panic".into()),
            Err(e) => HostError::Fault(native_fault_message(&e)),
        }
    }
    if e.is::<SourceStackFatal>() { return HostError::Fatal("runtime: goroutine stack exceeds limit\nfatal error: stack overflow\n".into()) }
    if let Some(f) = e.downcast_ref::<HostFatal>() { return HostError::Fatal(format!("fatal error: {}\n", f.0)) }
    if e.is::<BlockedPayload>() { return HostError::Blocked }
    HostError::Fault(native_fault_message(&e))
}
pub fn native_fault_message(e: &Box<dyn std::any::Any + Send>) -> String {
    if let Some(f) = e.downcast_ref::<HostFault>() { f.0.clone() }
    else if let Some(s) = e.downcast_ref::<String>() { s.clone() }
    else if let Some(s) = e.downcast_ref::<&str>() { s.to_string() }
    else { "unexpected native unwind".into() }
}
pub fn native_catch<R>(f: impl FnOnce() -> R) -> Result<R, String> {
    std::panic::catch_unwind(std::panic::AssertUnwindSafe(f)).map_err(|e| native_fault_message(&e))
}
/// Reserved before constructors; all fallible initialization is protected. Only
/// this calling thread owns source. No application panic hook is replaced.
fn drive(main_factory: impl FnOnce() -> Rc<Task>, real: bool, harness: bool, initialize: impl FnOnce(&Rc<Task>)) -> Result<(), HostError> {
    let reservation = reserve_entry().map_err(|e| HostError::Fault(e.0))?;
    drive_reserved(Some(reservation), main_factory, real, harness, initialize)
}
fn drive_reserved(_reservation: Option<EntryReservation>, main_factory: impl FnOnce() -> Rc<Task>, real: bool, harness: bool, initialize: impl FnOnce(&Rc<Task>)) -> Result<(), HostError> {
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let main = main_factory();
        install(main.clone(), harness);
        if real { sched(|s| s.epoch = Some(Instant::now())); }
        initialize(&main);
        ready(&main);
        while !main.done.get() { let t = next(); run(&t); safepoint(); }
    })).map_err(classify);
    let retirement = retire_owner();
    match (result, retirement) { (Ok(()), Err(e)) => Err(HostError::Fault(e)), (result, _) => result }
}
pub fn run_main_host(nglobals: usize, init: fn(), entry: fn() -> V) -> Result<(), HostError> {
    let _reservation = reserve_entry().map_err(|e| HostError::Fault(e.0))?;
    // Parent holds reservation through join and owner TLS teardown.
    // Function pointers, reservation and wire error bytes are Send. All source
    // heaps, globals, contexts and panic H values stay on this 16 MiB owner.
    std::thread::Builder::new().name("goalchemy-owner".into()).stack_size(16 * 1024 * 1024).spawn(move || {
        drive_reserved(None, || Task::new(0, None), true, false, |main| {
            init_globals(nglobals); init(); set_frame(main, Some(entry().frame()));
        })
    }).map_err(|e| HostError::Fault(format!("owner thread submission failed: {}", e)))?
      .join().unwrap_or_else(|e| Err(HostError::Fault(native_fault_message(&e))))
}
/// Test-only calling-owner frame factory: Rc values cannot cross threads. Caller
/// must supply sufficient native stack; emitted programs use run_main_host's
/// known 16 MiB owner stack. This is not an exported SDK library API.
pub fn run_host_frame(factory: impl FnOnce() -> Rc<Frame>) -> Result<(), HostError> {
    drive(|| Task::new(0, Some(factory())), true, false, |_| {})
}
pub fn report_host_result(result: Result<(), HostError>) {
    match result {
        Ok(()) => {},
        Err(HostError::Fatal(text)) => { out::stderr(text.as_bytes()); std::process::exit(2); }
        Err(HostError::SourcePanic(text)) => { out::stderr(&text); std::process::exit(2); }
        Err(HostError::Fault(text)) => { out::stderr(format!("goalchemy runtime fault: {}\n", text).as_bytes()); std::process::exit(101); }
        Err(HostError::Blocked) => host_fault("blocked executable"),
    }
}
/// Default executable keeps deterministic virtual time and the legacy stack.
pub fn run_main(nglobals: usize, init: fn(), entry: fn() -> V) -> ! {
    let reservation = reserve_entry().unwrap_or_else(|e| host_fault(&e.0));
    run_large(move || report_host_result(drive_reserved(Some(reservation), || Task::new(0, None), false, false, |main| {
        init_globals(nglobals); init(); set_frame(main, Some(entry().frame()));
    })))
}

/// Requeues the running task: a pause primitive.
pub fn yield_task(t: &Rc<Task>) {
    ready(t);
    block(t);
}

fn await_step(t: &Rc<Task>, f: &Rc<Frame>) {
    if f.pc.get() == 0 {
        f.pc.set(1);
        let p = f.prim.borrow_mut().take().unwrap();
        p(t);
        return;
    }
    let rv = t.rv.borrow().clone();
    f.l.s(0, tuple(rv));
    ret(t, f)
}

fn await_results(f: &Frame) -> Vec<V> {
    match f.l.g(0) {
        V::Tuple(t) => t.to_vec(),
        _ => Vec::new(),
    }
}

/// Runs one pause primitive in an isolated scheduler for a harness case;
/// unwinds with BlockedPayload when no task can run, and with the source
/// panic on panic.
pub fn run_isolated(prim: impl FnOnce(&Rc<Task>) + 'static) -> Vec<V> {
    let mut output = Vec::new();
    let mut source_panic = V::Nil;
    // Harness retains its source panic value rather than formatting it for an
    // executable. The protected region includes Frame constructor failure.
    let _reservation = reserve_entry().unwrap_or_else(|e| host_fault(&e.0));
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let h = Frame::new(1, await_step, Some(await_results));
        h.prim.replace(Some(Box::new(prim)));
        let main = Task::new(0, Some(h.clone())); install(main.clone(), true); ready(&main);
        while !main.done.get() { let t = next(); run(&t); }
        output = await_results(&h);
    }));
    if let Err(e) = &result { if let Some(p) = e.downcast_ref::<GoPanicPayload>() { source_panic = V::Obj(p.0); } }
    let _output_roots = temp_root(&output);
    let _root = temp_root(&[source_panic]);
    let retirement = retire_owner();
    if let Err(e) = result { std::panic::resume_unwind(e); }
    retirement.unwrap_or_else(|e| host_fault(&e)); output
}
pub fn reset_scheduler() {
    let _reservation = reserve_entry().unwrap_or_else(|e| host_fault(&e.0));
    install(Task::new(0, None), true)
}

/// Capture request timeout before any input copy/submission; context-only expiry
/// and request-only expiry are distinct. This is generic, not lib.http.do.
pub fn host_boundary(context: V, timeout: Option<i64>) -> HostBoundary {
    let now = clock_now(); observe_context(&context);
    let (error, parent) = with_ctx(&context, |c| (c.err.clone(), c.deadline));
    let request = timeout.map(|d| deadline_after(now, d));
    let deadline = match (parent, request) { (Some(a), Some(b)) => Some(a.min(b)), (a, b) => a.or(b) };
    HostBoundary { context, deadline, error }
}
pub fn register_host(t: &Rc<Task>, boundary: HostBoundary, roots: Vec<V>, cancel_result: fn(V) -> Vec<V>, decode: impl FnOnce(Vec<HostWire>) -> Vec<V> + 'static, cancel: impl FnOnce() + 'static, cleanup: impl FnOnce() + 'static) -> HostToken {
    check_task(t); check_context(&boundary.context);
    if !sched(|s| s.epoch.is_some()) { host_fault("host operation requires explicit monotonic entry") }
    block(t);
    sched(|s| {
        s.operation = s.operation.checked_add(1).unwrap(); let operation = s.operation;
        let mut traced = roots.clone(); traced.push(boundary.context.clone()); traced.push(boundary.error.clone());
        s.host_roots.insert(operation, traced);
        s.pending.insert(operation, HostPending {
            task: t.clone(), boundary, roots, canceled: false, cancel: Some(Box::new(cancel)), cleanup: Some(Box::new(cleanup)),
            decode: Some(Box::new(decode)), cancel_result,
        });
        s.mailbox.lock().live.insert(operation, MailEntry { task: t.id, record: None, ack: false });
        HostToken { mailbox: s.mailbox.clone(), owner: s.owner, operation, task: t.id }
    })
}
/// The closure owns native acquisitions and must release them on return or
/// unwind before its worker wrapper publishes ACK. No source V is Send.
pub fn launch_host(token: HostToken, work: impl FnOnce() -> Vec<HostWire> + Send + 'static) {
    let failure_token = token.clone();
    if let Err(e) = std::thread::Builder::new().spawn(move || {
        let result = native_catch(work);
        match result { Ok(v) => token.complete(v), Err(e) => token.fault(e) }
        token.acknowledge_cleanup();
    }) {
        // spawn has dropped the native work closure before returning failure.
        failure_token.fault(format!("native worker submission failed: {}", e)); failure_token.acknowledge_cleanup();
    }
}
fn pending_error(id: u64) -> V {
    let (context, deadline, error) = sched(|s| { let p = &s.pending[&id]; (p.boundary.context.clone(), p.boundary.deadline, p.boundary.error.clone()) });
    if !error.is_nil() { return error }
    let context_error = std_context_context_err(context);
    if !context_error.is_nil() { return context_error }
    if deadline.map_or(false, |d| d <= clock_now()) { context_deadline_exceeded() } else { V::Nil }
}
fn cancel_pending() -> Result<(), String> {
    let mut failure = None;
    let ids = sched(|s| s.pending.keys().copied().collect::<Vec<_>>());
    for id in ids {
        if sched(|s| s.pending[&id].canceled) { continue }
        let error = pending_error(id);
        if error.is_nil() { continue }
        let cancel = sched(|s| { let p = s.pending.get_mut(&id).unwrap(); p.canceled = true; p.boundary.error = error.clone(); s.host_roots.get_mut(&id).unwrap().push(error); p.cancel.take() });
        if let Some(cancel) = cancel { if let Err(e) = native_catch(cancel) { failure.get_or_insert(e); } }
    }
    failure.map_or(Ok(()), Err)
}
fn dispatch_host() {
    poll_host();
    fire_timers(true);
    cancel_pending().unwrap_or_else(|e| host_fault(&e));
    let mailbox = sched(|s| s.mailbox.clone());
    let ready = {
        let mut state = mailbox.lock();
        if let Some(e) = state.fault.take() { host_fault(&e) }
        let mut ready = state.live.iter().filter_map(|(id, x)| if x.ack { x.record.as_ref().map(|(seq, _)| (*seq, *id)) } else { None }).collect::<Vec<_>>();
        ready.sort_unstable();
        ready.into_iter().map(|(_, id)| (id, state.live.remove(&id).unwrap().record.unwrap().1)).collect::<Vec<_>>()
    };
    for (id, record) in ready {
        let mut p = sched(|s| s.pending.remove(&id).unwrap());
        let result = match record {
            HostRecord::Fault(e) => Err(e),
            HostRecord::Result(wire) => if !p.boundary.error.is_nil() { native_catch(|| (p.cancel_result)(p.boundary.error.clone())) }
                else { native_catch(|| p.decode.take().unwrap()(wire)) },
        };
        let result_roots = temp_root(result.as_ref().map(Vec::as_slice).unwrap_or(&[]));
        let cleanup = p.cleanup.take().map(native_catch).unwrap_or(Ok(()));
        sched(|s| { s.host_roots.remove(&id); });
        let values = result.unwrap_or_else(|e| host_fault(&e));
        cleanup.unwrap_or_else(|e| host_fault(&e));
        p.task.set_rv(values); ready_task_after_host(&p.task); drop(result_roots);
    }
}
fn ready_task_after_host(t: &Rc<Task>) { ready(t) }
/// Cancel, wait true resource ACKs, then detach all owner roots. Source is never
/// driven during retirement; hook faults do not short-circuit other cleanup.
pub fn retire_owner() -> Result<(), String> {
    if !sched(|s| s.active) { return Ok(()) }
    sched(|s| s.retiring = true);
    let mut failure = None;
    let cancels = sched(|s| s.pending.values_mut().filter_map(|p| { p.canceled = true; p.cancel.take() }).collect::<Vec<_>>());
    for cancel in cancels { if let Err(e) = native_catch(cancel) { failure.get_or_insert(e); } }
    let mailbox = sched(|s| s.mailbox.clone());
    loop {
        let (all_ack, version) = { let state = mailbox.lock(); (state.live.values().all(|x| x.ack), state.version) };
        if all_ack { break }
        mailbox.waiting_cleanup.store(true, Ordering::Release);
        safepoint(); mailbox.wait(version, None);
    }
    mailbox.waiting_cleanup.store(false, Ordering::Release);
    let pending = sched(|s| std::mem::take(&mut s.pending));
    for (id, mut p) in pending {
        if let Some(cleanup) = p.cleanup.take() { if let Err(e) = native_catch(cleanup) { failure.get_or_insert(e); } }
        sched(|s| { s.host_roots.remove(&id); });
    }
    let contexts = sched(|s| s.contexts.clone());
    for context in contexts {
        let hooks = raw_ctx(&context, |c| { c.parent = V::Nil; c.children.clear(); c.timer = None; std::mem::take(&mut c.hooks) });
        let all_roots = hooks.values().flat_map(|h| h.roots.iter().cloned()).collect::<Vec<_>>();
        let _all_roots = temp_root(&all_roots);
        for (_, hook) in hooks {
            let _roots = temp_root(&hook.roots);
            if let Err(e) = native_catch(hook.call) { failure.get_or_insert(e); }
        }
    }
    let tasks = sched(|s| { let mut tasks=s.tasks.clone(); tasks.push(s.cur.clone()); tasks.extend(s.runq.iter().cloned()); tasks });
    detach_owner_frames(&tasks);
    for task in tasks {
        if let Some(cleanup) = task.cleanup.borrow_mut().take() { if let Err(e) = native_catch(cleanup) { failure.get_or_insert(e); } }
        task.cleanup_roots.borrow_mut().clear(); task.frame.replace(None); task.rv.borrow_mut().clear();
        task.resume_panic.replace(V::Nil); task.cur_panic.replace(V::Nil); task.defer_target.set(-1);
        task.blocked.set(false); task.done.set(true); task.retired.set(true);
    }
    { let mut state = mailbox.lock(); state.closed = true; state.live.clear(); state.version = state.version.wrapping_add(1); }
    mailbox.notify();
    sched(|s| {
        s.active = false; s.retiring = false; s.runq.clear(); s.tasks.clear(); s.timers.clear(); s.contexts.clear(); s.host_roots.clear();
        s.cur.frame.replace(None); s.cur.rv.borrow_mut().clear(); s.cur.cur_panic.replace(V::Nil); s.cur.resume_panic.replace(V::Nil);
    });
    failure.map_or(Ok(()), Err)
}

/// Defer runners have a real r.b -> child.parent -> r Rc cycle. Trace the whole
/// supported frame/value graph before detaching it; iterative visited traversal
/// prevents recursion and preserves roots until every link has been discovered.
fn detach_owner_frames(tasks: &[Rc<Task>]) {
    let mut values=Vec::new(); let mut frames=Vec::new();
    for task in tasks { trace_task(task,&mut values,&mut frames); }
    let mut seen=std::collections::HashSet::new(); let mut handles=std::collections::HashSet::new(); let mut owned=Vec::new();
    loop {
        while let Some(v)=values.pop() {
            match v {
                V::Frame(f)=>frames.push(f),
                V::Tuple(v)=>values.extend(v.iter().cloned()),
                V::Obj(h)|V::Ptr(h,_)|V::Slice(h,_,_,_)|V::ByteSlice(h,_,_,_) if h!=0 && handles.insert(h)=>with(h,|o|trace_obj(o,&mut values,&mut frames)),
                _=>{},
            }
        }
        let Some(f)=frames.pop() else{break};
        if !seen.insert(Rc::as_ptr(&f)){continue}
        f.l.trace(&mut values); values.push(f.panicking.borrow().clone());
        for link in [&f.parent,&f.a,&f.b] {if let Some(f)=link.borrow().clone(){frames.push(f)}}
        owned.push(f);
    }
    for f in owned {f.native.borrow_mut().take();f.parent.replace(None);f.a.replace(None);f.b.replace(None);f.panicking.replace(V::Nil);f.prim.borrow_mut().take();f.l.clear();}
}

thread_local! { static HOST_POLL: RefCell<Option<Rc<dyn Fn()>>> = RefCell::new(None); }
pub fn set_host_poll(p:Option<Rc<dyn Fn()>>){HOST_POLL.with(|x|*x.borrow_mut()=p);}
fn host_poll_active()->bool{HOST_POLL.with(|x|x.borrow().is_some())}
fn poll_host(){let p=HOST_POLL.with(|x|x.borrow().clone());if let Some(p)=p{p()}}
pub fn library_install(){install(Task::new(0,None),false);sched(|s|s.epoch=Some(Instant::now()));}
pub fn library_run(main:&Rc<Task>){while !main.done.get(){let t=next();run(&t);safepoint();}}

/// Harness-only owner-local values; production library returns detached native types.
pub fn run_host_isolated(prim:impl FnOnce(&Rc<Task>)+'static)->Vec<V>{
 let _reservation=reserve_entry().unwrap_or_else(|e|host_fault(&e.0));let mut output=Vec::new();let result=std::panic::catch_unwind(std::panic::AssertUnwindSafe(||{let h=Frame::new(1,await_step,Some(await_results));h.prim.replace(Some(Box::new(prim)));let main=Task::new(0,Some(h.clone()));install(main.clone(),true);sched(|s|s.epoch=Some(Instant::now()));ready(&main);while !main.done.get(){let t=next();run(&t)};output=await_results(&h)}));let _roots=temp_root(&output);let retirement=retire_owner();if let Err(e)=result{std::panic::resume_unwind(e)};retirement.unwrap_or_else(|e|host_fault(&e));output
}

// Native futures are deliberately !Send. The owner polls them from run(),
// exactly where it steps frames; no waker or foreign executor schedules source.
fn native_active(f: &Frame) -> bool {
    f.native.borrow().as_ref().map_or(false, |n| n.future.is_some())
}
fn native_results(f: &Frame) -> Vec<V> {
    match f.l.g(0) { V::Tuple(v) => v.to_vec(), _ => Vec::new() }
}
/// roots must list every captured source value until the async activation
/// registers its locals. Rust closure captures are invisible to the collector.
pub fn native_task(roots: Vec<V>, results: usize, start: impl FnOnce(Rc<Task>) -> NativeFuture + 'static) -> V {
    let fr = Frame::new(roots.len() + 1, native_step, Some(native_results));
    for (i, v) in roots.into_iter().enumerate() { fr.l.s(i + 1, v); }
    fr.native.replace(Some(NativeBody { start: Some(Box::new(start)), future: None, results }));
    V::Frame(fr)
}
fn native_step(t: &Rc<Task>, f: &Rc<Frame>) {
    let mut body = f.native.borrow_mut().take().expect("native body already completed");
    if let Some(start) = body.start.take() { body.future = Some(start(t.clone())); }
    // Wakes are intentionally ignored: runtime primitives put tasks on the
    // scheduler's FIFO queue. Polling another task here would nest task bodies.
    struct NoWake;
    impl std::task::Wake for NoWake { fn wake(self: std::sync::Arc<Self>) {} }
    static WAKER: std::sync::OnceLock<std::task::Waker> = std::sync::OnceLock::new();
    let waker = WAKER.get_or_init(|| std::task::Waker::from(std::sync::Arc::new(NoWake)));
    let mut cx = std::task::Context::from_waker(waker);
    let polled = catch(|| body.future.as_mut().unwrap().as_mut().poll(&mut cx));
    match polled {
        Ok(std::task::Poll::Pending) => {
            if !t.blocked.get() && t.frame.borrow().as_ref().map_or(true, |current| Rc::ptr_eq(current, f)) {
                host_fault("native future yielded without a scheduler wait");
            }
            f.native.replace(Some(body));
        }
        Ok(std::task::Poll::Ready(value)) => {
            let values = match body.results {
                0 => Vec::new(), 1 => vec![value],
                _ => match value { V::Tuple(v) => v.to_vec(), _ => fault("native results expected") },
            };
            f.l.s(0, tuple(values));
            drop(body);
            ret(t, f);
        }
        Err(p) => { drop(body); raise(p); }
    }
}

/// Wait once for the scheduler to return the turn. No native event loop polls
/// this future; run() resumes it only after the task is explicitly readied.
struct NativeWait { task: Rc<Task>, first: bool }
impl std::future::Future for NativeWait {
    type Output = ();
    fn poll(mut self: std::pin::Pin<&mut Self>, _: &mut std::task::Context<'_>) -> std::task::Poll<()> {
        if self.first { self.first = false; return std::task::Poll::Pending; }
        let panic = self.task.resume_panic.replace(V::Nil);
        if !panic.is_nil() { raise(panic); }
        std::task::Poll::Ready(())
    }
}
pub async fn wait(t: &Rc<Task>) { NativeWait { task: t.clone(), first: true }.await }

/// Run a frame-convention function value, interface method or runtime adapter
/// on this same task, retaining recover identity and the existing host ABI.
pub async fn await_frame(t: &Rc<Task>, child: V) {
    call(t, child);
    wait(t).await;
}

/// Catch only source panics. Driver failures never enter a future's poll;
/// faults and fatals propagate without running source defers.
pub async fn catch_async<T>(future: impl std::future::Future<Output = T>) -> Result<T, V> {
    let mut future = Some(Box::pin(future));
    std::future::poll_fn(move |cx| {
        match catch(|| future.as_mut().unwrap().as_mut().poll(cx)) {
            Ok(std::task::Poll::Pending) => std::task::Poll::Pending,
            Ok(std::task::Poll::Ready(v)) => { future.take(); std::task::Poll::Ready(Ok(v)) }
            Err(p) => { future.take(); std::task::Poll::Ready(Err(p)) }
        }
    }).await
}

pub async fn run_defers_async(t: &Rc<Task>, fr: &Fr, p: V) {
    // TLS TempRoots cannot span an await: other tasks can interleave their
    // pushes and drops. Root panic state and deferred captures on this task.
    let state = Fr::task(t, 5);
    state.s(0, p);
    while let Some(d) = fr.pop_defer() {
        state.s(1, t.cur_panic.replace(state.g(0)));
        let saved_target = t.defer_target.replace(d.fid);
        state.s(2, d.f.clone());
        state.s(3, tuple(d.args.clone()));
        let result = catch_async(async {
            if d.start { await_frame(t, callv(&d.f, d.args)).await; }
            else { callv(&d.f, d.args); }
        }).await;
        t.cur_panic.replace(state.g(1));
        t.defer_target.set(saved_target);
        match result {
            Err(p) => { chain_panic(&p, &state.g(0)); state.s(0, p); }
            Ok(()) => {
                let p = state.g(0);
                if !p.is_nil() && panic_recovered(&p) { state.s(0, V::Nil); }
            }
        }
    }
    let p = state.g(0);
    if !p.is_nil() { raise(p); }
}

pub struct NativeDepth(std::rc::Weak<Task>);
impl Drop for NativeDepth {
    fn drop(&mut self) {
        if let Some(t) = self.0.upgrade() { t.native_depth.set(t.native_depth.get() - 1); }
    }
}
/// Bound poll/drop recursion before exhausting a native stack. Host mode keeps
/// the frame-chain limit; virtual mode has an explicit native-only ceiling.
pub fn native_guard(t: &Rc<Task>) -> NativeDepth {
    let host = sched(|s| s.epoch.is_some());
    let limit = if host { 512 } else { 4096 };
    let depth = if host { native_stack_depth(t) } else { t.native_depth.get() };
    if depth >= limit { std::panic::resume_unwind(Box::new(SourceStackFatal)); }
    t.native_depth.set(t.native_depth.get() + 1);
    NativeDepth(Rc::downgrade(t))
}

// Native activations replace source frames. Do not count their adapter frames
// a second time when a function-value call crosses the compatibility boundary.
fn native_stack_depth(t: &Task) -> usize {
    let mut depth = t.native_depth.get();
    let mut frame = t.frame.borrow().clone();
    while let Some(f) = frame {
        if f.step as usize != native_step as Step as usize { depth += 1; }
        frame = f.parent.borrow().clone();
    }
    depth
}
