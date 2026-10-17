use super::*;

pub fn copy_future_panic(p: &V) -> V {
    if p.is_nil() { return V::Nil; }
    let previous = copy_future_panic(&panic_prev(p));
    V::Obj(alloc(Obj::Panic(PanicObj { value: panic_value(p), recovered: false, prev: previous })))
}

fn future_child_results(f: &Frame) -> Vec<V> { vec![f.l.g(2)] }
fn future_child_step(t: &Rc<Task>, f: &Rc<Frame>) {
    if f.pc.get() == 0 {
        f.pc.set(1);
        let result = callv(&unboxed(&f.l.g(0)), Vec::new());
        if matches!(result, V::Frame(_)) { call(t, result); return; }
        f.l.s(2, result);
    } else { f.l.s(2, t.rv(0)); }
    ret(t, f);
}
fn future_complete(_: &Rc<Task>, f: &Rc<Frame>, panic: V) {
    let future = f.l.g(1);
    let (results, observed, waiters) = with(future.h(), |o| {
        let Obj::Future(state) = o else { fault("future expected") };
        state.done = true;
        state.panic = panic.clone();
        if panic.is_nil() { state.results = f.l.g(2); }
        (state.results.clone(), state.observed, std::mem::take(&mut state.waiters))
    });
    if !panic.is_nil() && !observed { sched(|s| s.unawaited.push((future, panic.clone()))); }
    for waiter in waiters {
        waiter.set_rv(vec![results.clone()]);
        waiter.resume_panic.replace(copy_future_panic(&panic));
        ready(&waiter);
    }
}
pub fn lib_task_start(function: V) -> V {
    let future = V::Obj(alloc(Obj::Future(FutureState {
        done: false, observed: false, results: V::Nil, panic: V::Nil, waiters: Vec::new(),
    })));
    let child = Frame::new(3, future_child_step, Some(future_child_results));
    child.l.s(0, function);
    child.l.s(1, future.clone());
    child.completion.set(Some(future_complete));
    spawn(V::Frame(child));
    future
}
