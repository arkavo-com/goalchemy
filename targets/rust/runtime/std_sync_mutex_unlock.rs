//! std.sync.mutex.unlock.
use super::*;

pub fn std_sync_mutex_unlock(m: V) {
    let r = with_mutex(&m, |x| {
        if !x.locked {
            return Err(());
        }
        if x.waiters.is_empty() {
            x.locked = false;
            return Ok(None);
        }
        Ok(x.waiters.pop_front())
    });
    match r {
        Err(()) => fatal("sync: unlock of unlocked mutex"),
        Ok(Some(w)) => ready(&w),
        Ok(None) => {}
    }
}
