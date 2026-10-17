use super::*;

pub fn lib_task_await(t: &Rc<Task>, future: V) {
    check_task(t);
    if future.is_nil() { block(t); return; }
    sched(|s| s.unawaited.retain(|(key, _)| key.h() != future.h()));
    let (done, results, panic) = with(future.h(), |o| {
        let Obj::Future(state) = o else { fault("future expected") };
        state.observed = true;
        if !state.done { state.waiters.push(t.clone()); }
        (state.done, state.results.clone(), state.panic.clone())
    });
    if !done { block(t); return; }
    if !panic.is_nil() { raise(copy_future_panic(&panic)); }
    t.set_rv(vec![results]);
}
