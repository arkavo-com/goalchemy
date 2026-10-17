package rt;

import java.util.ArrayDeque;
import java.util.Deque;
import java.util.LinkedList;
import java.util.function.Supplier;

/** core.chan.make: make(chan T, size), and the channel state shared by the
 * channel and select runtime functions. */
public final class ChanMake {
    private ChanMake() {}

    public static final class SelectState {
        boolean done;
    }

    public static final class Waiter {
        TaskSpawn.Task task;
        Object val;
        boolean active=true;
        final SelectState sel;
        final int idx;

        Waiter(TaskSpawn.Task task, Object val, SelectState sel, int idx) {
            this.task = task;
            this.val = val;
            this.sel = sel;
            this.idx = idx;
        }

        boolean live() { return active && task!=null && task.owner.live(task) && (sel==null || !sel.done); }
        void clear() { active=false; val=null; task=null; }
        void recvDone(Object v, boolean ok) {
            if(!live()) return;
            active=false;
            if (sel != null) {
                sel.done = true;
                task.rv = new Object[] {(long) idx, v, ok};
            } else {
                task.rv = new Object[] {v, ok};
            }
            TaskSpawn.sched.ready(task);
        }

        void sendDone(boolean closed) {
            if(!live()) return;
            active=false;
            if (sel != null) {
                sel.done = true;
                task.rv = new Object[] {(long) idx, null, false};
            } else {
                task.rv = new Object[0];
            }
            if (closed) task.resumePanic = Panics.plainPanic("send on closed channel");
            TaskSpawn.sched.ready(task);
        }
    }

    public static final class Chan {
        final Deque<Object> buf = new LinkedList<>();
        public final int size;
        public boolean closed;
        ArrayDeque<Waiter> recvq = new ArrayDeque<>();
        ArrayDeque<Waiter> sendq = new ArrayDeque<>();
        final Supplier<Object> zero;

        Chan(int size, Supplier<Object> zero) {
            this.size = size;
            this.zero = zero;
        }

        public Deque<Object> buffer() {
            return buf;
        }
    }

    static Waiter dequeue(ArrayDeque<Waiter> q) {
        while (!q.isEmpty()) {
            Waiter w = q.poll();
            if (w.live()) return w;
            w.clear();
        }
        return null;
    }

    static boolean hasLive(ArrayDeque<Waiter> q) {
        for (Waiter w : q) if (w.live()) return true;
        return false;
    }

    /** Receives without blocking: {value, ok, done}. */
    static Object[] tryRecv(Chan ch) {
        if (!ch.buf.isEmpty()) {
            Object v = ch.buf.poll();
            Waiter w = dequeue(ch.sendq);
            if (w != null) {
                ch.buf.add(w.val);
                w.sendDone(false);
            }
            return new Object[] {v, true, true};
        }
        Waiter w = dequeue(ch.sendq);
        if (w != null) {
            w.sendDone(false);
            return new Object[] {w.val, true, true};
        }
        if (ch.closed) return new Object[] {ch.zero.get(), false, true};
        return new Object[] {null, false, false};
    }

    public static Chan makeChan(long size, Supplier<Object> zero) {
        if (size < 0 || size > (1L << 53)) throw Panics.plainPanic("makechan: size out of range");
        return new Chan((int) size, zero);
    }
}
