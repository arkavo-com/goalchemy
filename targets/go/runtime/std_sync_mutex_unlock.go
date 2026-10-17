package rt

func StdSyncMutexUnlock(m *Mutex) {
	if !m.locked {
		fatal("sync: unlock of unlocked mutex")
	}
	if len(m.waiters) > 0 {
		sched.ready(popFront(&m.waiters))
		return
	}
	m.locked = false
}
