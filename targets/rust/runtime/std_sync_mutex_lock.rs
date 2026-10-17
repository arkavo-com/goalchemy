//! std.sync.mutex.lock: FIFO handoff to waiting tasks.
use super::*;

pub fn with_mutex<R>(m: &V, f: impl FnOnce(&mut Mutex) -> R) -> R {
    with(m.h(), |o| match o {
        Obj::Mutex(x) => f(x),
        _ => fault("mutex expected"),
    })
}

pub fn std_sync_mutex_lock(t: &Rc<Task>, m: V) {
    check_task(t);
    t.set_rv(Vec::new());
    let wait = with_mutex(&m, |x| {
        if !x.locked {
            x.locked = true;
            return false;
        }
        x.waiters.push_back(t.clone());
        true
    });
    if wait {
        let id = t.id; let owner = t.owner.get();
        let object = m;
        t.cleanup_roots.replace(vec![object.clone()]);
        t.cleanup.replace(Some(Box::new(move || {
            with_mutex(&object, |x| x.waiters.retain(|w| w.id != id || w.owner.get() != owner));
        })));
        block(t);
    }
}
