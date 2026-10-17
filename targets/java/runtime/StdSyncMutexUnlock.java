package rt;

/** std.sync.mutex.unlock: hand the lock to the first waiter or release it. */
public final class StdSyncMutexUnlock {
    private StdSyncMutexUnlock() {}

    public static void stdSyncMutexUnlock(StdSyncMutexLock.Mutex m) {
        if (!m.locked) TaskSpawn.fatal("sync: unlock of unlocked mutex");
        while (!m.waiters.isEmpty()) {
            TaskSpawn.Task t=m.waiters.poll();
            if(t.owner==TaskSpawn.sched && TaskSpawn.sched.live(t)) { TaskSpawn.sched.ready(t); return; }
        }
        m.locked = false;
    }
}
