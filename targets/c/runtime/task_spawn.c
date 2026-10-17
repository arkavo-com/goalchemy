/* core.task.spawn and the cooperative scheduler. A task is a stack of
 * frames driven by a trampoline; exactly one task runs at a time and
 * runnable tasks are dispatched in FIFO order. Pause primitives either
 * complete immediately, leaving their results in the task's rv, or block
 * the task until another task or a timer readies it. */
#define GC_NO_THREAD_REDIRECTS
#include "gx.h"

#include <stdio.h>
#include <stdlib.h>
#include <errno.h>
#include <limits.h>

static void retire_owner(gx_Sched *s);
void (*gx_native_domain_release)(uint64_t domain);
static void host_cleanup_action(gx_Sched *s, gx_HostAction action, void *arg);

/* xorshift32 choice source, identical on every target. */
size_t gx_choose(size_t n) {
    gx_owner_check(gx_sched);
    int64_t x = gx_sched->rng;
    x ^= (x << 13) & 0xFFFFFFFFLL;
    x ^= (int64_t)((uint64_t)x >> 17);
    x ^= (x << 5) & 0xFFFFFFFFLL;
    gx_sched->rng = x;
    return (size_t)(x % (int64_t)n);
}

void gx_ready(gx_Task *t) {
    gx_Sched *s = gx_sched;
    if (!t || !gx_owner_current(t->owner) || t->done || s->retiring || t->queued)
        return;
    t->queued = true;
    if (s->qlen == s->qcap) {
        size_t c = s->qcap ? s->qcap * 2 : 16;
        gx_Task **q = GC_MALLOC(c * sizeof(gx_Task *));
        for (size_t i = 0; i < s->qlen; i++)
            q[i] = s->runq[(s->qhead + i) % s->qcap];
        s->runq = q;
        s->qcap = c;
        s->qhead = 0;
    }
    s->runq[(s->qhead + s->qlen) % s->qcap] = t;
    s->qlen++;
}

void gx_block(gx_Task *t) {
    gx_owner_check(t->owner);
    t->blocked = true;
}

int64_t gx_deadline(int64_t now, int64_t duration) {
    if (duration <= 0)
        return now;
    return now > INT64_MAX - duration ? INT64_MAX : now + duration;
}
int64_t gx_now(void) {
    if (!gx_sched)
        gx_cur_task();
    gx_Sched *s = gx_sched;
    gx_owner_check(s);
    if (!s->host)
        return s->clock;
    struct timespec now;
    if (clock_gettime(CLOCK_MONOTONIC, &now))
        gx_host_fault("monotonic clock failed");
    if (now.tv_sec < s->epoch.tv_sec || (now.tv_sec == s->epoch.tv_sec && now.tv_nsec < s->epoch.tv_nsec))
        gx_host_fault("monotonic clock regressed");
    uint64_t sec = (uint64_t)now.tv_sec - (uint64_t)s->epoch.tv_sec;
    int64_t ns = now.tv_nsec - s->epoch.tv_nsec;
    if (ns < 0) {
        sec--;
        ns += 1000000000LL;
    }
    if (sec > (uint64_t)INT64_MAX / 1000000000ULL)
        return s->clock = INT64_MAX;
    uint64_t elapsed = sec * 1000000000ULL + (uint64_t)ns;
    s->clock = elapsed > INT64_MAX ? INT64_MAX : (int64_t)elapsed;
    return s->clock;
}
void gx_add_timer_at(int64_t at, gx_Task *task, void (*f)(gx_V), gx_V arg) {
    gx_Sched *s = gx_sched;
    gx_owner_check(s);
    if (s->ntimers == s->tcap) {
        size_t c = s->tcap ? s->tcap * 2 : 8;
        gx_Timer *ts = GC_MALLOC(c * sizeof(gx_Timer));
        if (s->ntimers)
            memcpy(ts, s->timers, s->ntimers * sizeof(gx_Timer));
        s->timers = ts;
        s->tcap = c;
    }
    if (s->seq == INT64_MAX)
        gx_host_fault("timer sequence exhausted");
    gx_Timer t = {at, ++s->seq, task, f, arg};
    s->timers[s->ntimers++] = t;
}
void gx_add_timer(int64_t d, gx_Task *task, void (*f)(gx_V), gx_V arg) {
    gx_add_timer_at(gx_deadline(gx_now(), d), task, f, arg);
}
void gx_remove_context_timer(gx_Context *c) {
    gx_Sched *s = gx_sched;
    size_t n = 0;
    for (size_t i = 0; i < s->ntimers; i++)
        if (s->timers[i].arg.u.p != c)
            s->timers[n++] = s->timers[i];
    if (n < s->ntimers)
        memset(s->timers + n, 0, (s->ntimers - n) * sizeof(gx_Timer));
    s->ntimers = n;
}

_Noreturn void gx_fatal(const char *msg) {
    if (gx_sched && gx_sched->host && gx_sched->escape) {
        gx_sched->fatal = GC_strdup(msg);
        gx_handler = NULL;
        gx_source_depth = 0;
        longjmp(*gx_sched->escape, 1);
    }
    char buf[256];
    int n = snprintf(buf, sizeof buf, "fatal error: %s\n", msg);
    gx_stderr(buf, (size_t)n);
    exit(2);
}

static void check_future_panics(void) {
    for (gx_Unawaited *p = gx_sched->unawaited; p; p = p->next) {
        if (!p->panic) continue;
        if (gx_sched->harness) gx_raise(p->panic);
        gx_report_panic(p->panic);
    }
}

static int64_t earliest_timer(void) {
    int64_t at = INT64_MAX;
    for (size_t i = 0; i < gx_sched->ntimers; i++)
        if (gx_sched->timers[i].at < at)
            at = gx_sched->timers[i].at;
    return at;
}
static void fire_timers(void) {
    gx_Sched *s = gx_sched;
    int64_t at = s->host ? gx_now() : earliest_timer();
    if (!s->host)
        s->clock = at;
    size_t nd = 0, nk = 0, old = s->ntimers;
    /* Scanned storage roots the entire detached batch across nested GC. */
    gx_Timer *due = GC_MALLOC((old ? old : 1) * sizeof(gx_Timer));
    for (size_t i = 0; i < old; i++) {
        if (s->timers[i].at <= at)
            due[nd++] = s->timers[i];
        else
            s->timers[nk++] = s->timers[i];
    }
    if (nk < old)
        memset(s->timers + nk, 0, (old - nk) * sizeof(gx_Timer));
    s->ntimers = nk;
    for (size_t i = 1; i < nd; i++)
        for (size_t j = i;
             j > 0 && (due[j - 1].at > due[j].at || (due[j - 1].at == due[j].at && due[j - 1].seq > due[j].seq)); j--) {
            gx_Timer t = due[j];
            due[j] = due[j - 1];
            due[j - 1] = t;
        }
    for (size_t i = 0; i < nd; i++) {
        if (due[i].f)
            due[i].f(due[i].arg);
        if (due[i].task)
            gx_ready(due[i].task);
    }
    GC_reachable_here(due);
}
static gx_Task *next(void) {
    gx_Sched *s = gx_sched;
    for (;;) {
        if (s->host) {
            fire_timers();
            gx_host_poll();
        }
        if (s->qlen)
            break;
        if (s->host && (s->pending || s->ntimers)) {
            gx_host_wait(earliest_timer(), s->ntimers != 0);
            continue;
        }
        if (s->ntimers) {
            fire_timers();
            continue;
        }
        check_future_panics();
        if (s->harness) {
            gx_blocked_signal = 1;
            gx_raise(gx_new_panic(gx_nil()));
        }
        gx_fatal("all goroutines are asleep - deadlock!");
    }
    gx_Task *t = s->runq[s->qhead];
    s->runq[s->qhead] = NULL;
    s->qhead = (s->qhead + 1) % s->qcap;
    s->qlen--;
    t->queued = false;
    return t;
}

static void exit_frame(gx_Task *t, gx_Frame *f, gx_Panic *p);
static void defer_runner_step(gx_Task *t, gx_Frame *r);

static void run(gx_Task *t) {
    gx_sched->cur = t;
    t->blocked = false;
    if (t->cleanup) {
        void (*c)(void *) = t->cleanup;
        t->cleanup = NULL;
        void *arg = t->cleanup_arg;
        t->cleanup_arg = NULL;
        c(arg);
    }
    while (!t->blocked && t->frame) {
        gx_Panic *rp = t->resume_panic;
        if (rp) {
            t->resume_panic = NULL;
            exit_frame(t, t->frame, rp);
            continue;
        }
        gx_Frame *volatile f = t->frame;
        gx_Handler h;
        if (GX_TRY(h)) {
            exit_frame(t, f, gx_thrown);
            continue;
        }
        f->step(t, f);
        GX_END(h);
    }
}

static void finish(gx_Task *t, gx_Frame *f);

static void exit_frame(gx_Task *t, gx_Frame *f, gx_Panic *p) {
    if (p) {
        gx_chain_panic(p, f->panicking);
        f->panicking = p;
    }
    t->frame = f;
    if (f->defers) {
        gx_Frame *r = gx_new_frame(2, defer_runner_step, NULL);
        r->a = f;
        r->parent = f;
        t->frame = r;
        return;
    }
    finish(t, f);
}

static void after(gx_Frame *tf, gx_Panic *p) {
    if (p) {
        gx_chain_panic(p, tf->panicking);
        tf->panicking = p;
        return;
    }
    if (tf->panicking && tf->panicking->recovered)
        tf->panicking = NULL;
}

static void restore(gx_Task *t, gx_Frame *r) {
    t->cur_panic = r->l[0].u.p;
    t->defer_target = r->l[1].u.i;
}

static void child_done(gx_Task *t, gx_Frame *r, gx_Panic *p) {
    restore(t, r);
    r->b = NULL;
    t->frame = r;
    after(r->a, p);
}

static void finish(gx_Task *t, gx_Frame *f) {
    gx_Panic *p = f->panicking;
    gx_Frame *parent = f->parent;
    t->frame = parent;
    if (!parent) {
        t->done = true;
        gx_Task **link = &gx_sched->all;
        while (*link && *link != t)
            link = &(*link)->next_all;
        if (*link)
            *link = t->next_all;
        t->next_all = NULL;
        if (f->completion) { f->completion(t, f, p); return; }
        if (p) {
            if (gx_sched->harness)
                gx_raise(p);
            gx_report_panic(p);
        }
        return;
    }
    if (parent->step == defer_runner_step && parent->b == f) {
        child_done(t, parent, p);
        return;
    }
    if (p) {
        exit_frame(t, parent, p);
        return;
    }
    gx_V out[16];
    int n = f->results ? f->results(f, out) : 0;
    gx_set_rv(t, n, out);
}

static void defer_runner_step(gx_Task *t, gx_Frame *r) {
    gx_Frame *tf = r->a;
    while (tf->defers) {
        gx_Deferred *d = tf->defers;
        tf->defers = d->next;
        r->l[0] = gx_obj(t->cur_panic);
        r->l[1] = gx_int(t->defer_target);
        t->cur_panic = tf->panicking;
        t->defer_target = d->fid;
        gx_Handler h;
        if (GX_TRY(h)) {
            restore(t, r);
            after(tf, gx_thrown);
            continue;
        }
        gx_V c = gx_callv(d->f, d->n, d->args);
        GX_END(h);
        if (d->start) {
            gx_Frame *cf = gx_frameof(c);
            r->b = cf;
            cf->parent = r;
            t->frame = cf;
            return;
        }
        restore(t, r);
        after(tf, NULL);
    }
    t->frame = tf;
    finish(t, tf);
}

/* Pushes a callee frame: a pause point. */
void gx_call(gx_Task *t, gx_V child) {
    gx_owner_check(t->owner);
    gx_Frame *c = gx_frameof(child);
    if (gx_sched->host) {
        size_t depth = 0;
        for (gx_Frame *f = t->frame; f; f = f->parent)
            if (++depth >= 256)
                gx_fatal("stack overflow");
    }
    c->parent = t->frame;
    t->frame = c;
}

void gx_ret(gx_Task *t, gx_Frame *f) { gx_owner_check(t->owner); exit_frame(t, f, NULL); }

static void sync_step(gx_Task *t, gx_Frame *f) {
    gx_V args = f->l[1];
    f->l[3] = gx_callv(f->l[0], (int)args.l, gx_vals(args));
    gx_ret(t, f);
}

static int sync_results(gx_Frame *f, gx_V *out) {
    int n = (int)f->l[2].u.i;
    if (n == 1)
        out[0] = f->l[3];
    for (int i = 0; n > 1 && i < n; i++)
        out[i] = gx_at(f->l[3], i);
    return n;
}

/* Runs an ordinary call of f with nres results as a frame. */
gx_V gx_sync_frame(gx_V f, int n, const gx_V *args, int nres) {
    gx_Frame *fr = gx_new_frame(4, sync_step, sync_results);
    fr->l[0] = f;
    fr->l[1] = gx_tuple(n, args);
    fr->l[2] = gx_int(nres);
    return gx_vframe(fr);
}

static gx_V adapt_code(gx_V *env, gx_V *args, int n) { return gx_sync_frame(env[0], n, args, (int)env[1].u.i); }

/* Adapts an ordinary function value with nres results to the resumable form. */
gx_V gx_adapt(gx_V f, int nres) {
    if (f.t == GX_NIL)
        return f;
    gx_V env[2] = {f, gx_int(nres)};
    return gx_func(gx_fid_of(f), adapt_code, 2, env);
}

gx_V gx_adapt_slice(gx_V s, int nres) {
    if (!s.u.p)
        return s;
    gx_V *a = gx_alloc_vals(s.l);
    for (uint32_t i = 0; i < s.l; i++)
        a[i] = gx_adapt(gx_vals(s)[i], nres);
    return gx_slice(a, s.l, s.l);
}

/* go f(args): starts a task running frame f. */
void gx_spawn(gx_V frame) {
    gx_owner_check(gx_sched);
    if (gx_sched->next_id == INT_MAX) gx_host_fault("task identity exhausted");
    gx_Task *t = gx_new_task(gx_sched->next_id++, gx_frameof(frame));
    gx_own_task(gx_sched, t);
    gx_ready(t);
}

void gx_spawn_call(gx_V f, int n, const gx_V *args) { gx_spawn(gx_sync_frame(gx_fnchk(f), n, args, 0)); }

static void (*main_init)(void);
static gx_V (*main_entry)(void);

static void main_body(void) {
    gx_Task *main = gx_new_task(0, NULL);
    gx_sched = gx_new_sched(main, false);
    gx_sched->retire = retire_owner;
    gx_Handler h;
    if (GX_TRY(h))
        gx_report_panic(gx_thrown);
    main_init();
    main->frame = gx_frameof(main_entry());
    GX_END(h);
    gx_ready(main);
    while (!main->done)
        run(next());
    check_future_panics();
    gx_retire(gx_sched);
}

/* Runs the program entry as the first task until it returns. */
_Noreturn void gx_run_main(void (*init)(void), gx_V (*entry)(void)) {
    if (!gx_entry_reserve())
        gx_host_fault("overlapping executable entry");
    GC_INIT();
    gx_retire(gx_sched);
    gx_sched = NULL;
    main_init = init;
    main_entry = entry;
    gx_run_large(main_body);
    gx_entry_release();
    exit(0);
}

/* Requeues the running task: a pause primitive. */
void gx_yield_task(gx_Task *t) {
    gx_ready(t);
    gx_block(t);
}

static void await_step(gx_Task *t, gx_Frame *f) {
    if (f->pc == 0) {
        f->pc = 1;
        f->prim(t, f->prim_arg);
        return;
    }
    f->l[0] = gx_tuple(t->nrv, t->rv);
    gx_ret(t, f);
}

/* Runs one pause primitive in an isolated scheduler for a harness case and
 * stores its results in out. A blocked case raises with gx_blocked_signal
 * set; a panic raises the source panic. */
int gx_run_isolated(void (*prim)(gx_Task *, void *), void *arg, gx_V *out) {
    if (!gx_entry_reserve())
        gx_host_fault("overlapping harness entry");
    gx_Handler guard;
    if (GX_TRY(guard)) {
        gx_Panic *panic = gx_thrown;
        gx_retire(gx_sched);
        gx_entry_release();
        gx_raise(panic);
    }
    gx_retire(gx_sched);
    gx_Frame *h = gx_new_frame(1, await_step, NULL);
    h->prim = prim;
    h->prim_arg = arg;
    h->l[0] = gx_tuple(0, NULL);
    gx_Task *main = gx_new_task(0, h);
    gx_sched = gx_new_sched(main, true);
    gx_sched->retire = retire_owner;
    gx_ready(main);
    while (!main->done)
        run(next());
    gx_V r = h->l[0];
    for (uint32_t i = 0; i < r.l; i++)
        out[i] = gx_at(r, (int)i);
    gx_retire(gx_sched);
    GX_END(guard);
    gx_entry_release();
    return (int)r.l;
}
void gx_reset_scheduler(void) {
    if (!gx_entry_reserve())
        gx_host_fault("overlapping scheduler reset");
    gx_retire(gx_sched);
    gx_sched = gx_new_sched(gx_new_task(0, NULL), true);
    gx_sched->retire = retire_owner;
    gx_blocked_signal = 0;
    gx_entry_release();
}

/* Native wire mailbox. None of these malloc objects contains source pointers.
 * The live identity table bounds records and ACKs to registered operations. */
typedef struct gx_HostLive {
    uint64_t operation;
    int task;
    bool ack, published, inline_fault;
    char emergency_fault[128];
    uint8_t *bytes;
    size_t length;
    char *fault;
    struct gx_HostLive *next, *published_next;
} gx_HostLive;
struct gx_Mailbox {
    pthread_mutex_t mutex;
    pthread_cond_t wake;
    size_t refs;
    uint64_t generation, version;
    bool closed;
    gx_HostLive *live, *head, *tail;
};
struct gx_HostPending {
    gx_HostToken token;
    gx_Task *task;
    gx_HostBoundary boundary;
    gx_V roots;
    gx_HostDecode decode;
    gx_HostAction cancel, cleanup;
    void *native;
    bool cancel_requested;
    gx_HostPending *next;
};
static uint64_t host_generation;
static pthread_mutex_t library_wake_mutex=PTHREAD_MUTEX_INITIALIZER;
static gx_HostToken library_wake_token;

static void host_free_live(gx_HostLive *live) {
    free(live->bytes);
    if(!live->inline_fault) free(live->fault);
    free(live);
}
static gx_HostLive *host_find(gx_HostToken t) {
    gx_Mailbox *b = t.mailbox;
    if (b->closed || b->generation != t.generation)
        return NULL;
    for (gx_HostLive *l = b->live; l; l = l->next)
        if (l->operation == t.operation && l->task == t.task)
            return l;
    return NULL;
}
void gx_host_token_retain(gx_HostToken t) {
    if (!t.mailbox)
        return;
    pthread_mutex_lock(&t.mailbox->mutex);
    t.mailbox->refs++;
    pthread_mutex_unlock(&t.mailbox->mutex);
}
void gx_host_token_release(gx_HostToken t) {
    gx_Mailbox *b = t.mailbox;
    if (!b)
        return;
    pthread_mutex_lock(&b->mutex);
    bool release = --b->refs == 0;
    pthread_mutex_unlock(&b->mutex);
    if (release) {
        pthread_cond_destroy(&b->wake);
        pthread_mutex_destroy(&b->mutex);
        free(b);
    }
}
bool gx_host_publish(gx_HostToken t, const void *bytes, size_t length, const char *fault) {
    gx_Mailbox *b = t.mailbox;
    if (!b || (length && !bytes))
        return false;
    /* Allocate before locking; rejected duplicates release their snapshot. */
    uint8_t *copy = length ? malloc(length) : NULL;
    char *error = fault ? strdup(fault) : NULL;
    if ((length && !copy) || (fault && !error)) {
        free(copy);
        free(error);
        return false;
    }
    if (length)
        memcpy(copy, bytes, length);
    pthread_mutex_lock(&b->mutex);
    gx_HostLive *l = host_find(t);
    bool accepted = l && !l->published;
    if (accepted) {
        l->published = true;
        l->bytes = copy;
        l->length = length;
        l->fault = error;
        if (b->tail)
            b->tail->published_next = l;
        else
            b->head = l;
        b->tail = l;
        b->version++;
        pthread_cond_broadcast(&b->wake);
    }
    pthread_mutex_unlock(&b->mutex);
    if (!accepted) {
        free(copy);
        free(error);
    }
    return accepted;
}
bool gx_host_publish_fault(gx_HostToken t,const char *fault) {
 gx_Mailbox *b=t.mailbox;if(!b||!fault)return false;
 pthread_mutex_lock(&b->mutex);gx_HostLive *l=host_find(t);bool accepted=l&&!l->published;
 if(accepted){size_t n=strnlen(fault,sizeof(l->emergency_fault)-1);memcpy(l->emergency_fault,fault,n);l->emergency_fault[n]=0;l->fault=l->emergency_fault;l->inline_fault=true;l->published=true;
  if(b->tail)b->tail->published_next=l;else b->head=l;b->tail=l;b->version++;pthread_cond_broadcast(&b->wake);}
 pthread_mutex_unlock(&b->mutex);return accepted;
}
bool gx_host_ack(gx_HostToken t) {
    gx_Mailbox *b = t.mailbox;
    if (!b)
        return false;
    pthread_mutex_lock(&b->mutex);
    gx_HostLive *l = host_find(t);
    bool accepted = l && !l->ack;
    if (accepted) {
        l->ack = true;
        b->version++;
        pthread_cond_broadcast(&b->wake);
    }
    pthread_mutex_unlock(&b->mutex);
    return accepted;
}
void gx_library_wake(void) {
 pthread_mutex_lock(&library_wake_mutex);gx_Mailbox *b=library_wake_token.mailbox;
 if(b){pthread_mutex_lock(&b->mutex);if(!b->closed&&b->generation==library_wake_token.generation){b->version++;pthread_cond_broadcast(&b->wake);}pthread_mutex_unlock(&b->mutex);}
 pthread_mutex_unlock(&library_wake_mutex);
}
size_t gx_host_live_count(gx_HostToken t) {
    if (!t.mailbox)
        return 0;
    pthread_mutex_lock(&t.mailbox->mutex);
    size_t n = 0;
    for (gx_HostLive *l = t.mailbox->live; l; l = l->next)
        n++;
    pthread_mutex_unlock(&t.mailbox->mutex);
    return n;
}
static gx_HostPending *host_pending(gx_HostToken t) {
    for (gx_HostPending *p = gx_sched->pending; p; p = p->next)
        if (p->token.operation == t.operation && p->token.task == t.task && p->token.generation == t.generation &&
            p->token.mailbox == t.mailbox)
            return p;
    return NULL;
}
static gx_V host_cancellation(gx_HostPending *p) {
    if (p->boundary.context && p->boundary.context->err.t != GX_NIL)
        return p->boundary.context->err;
    if (p->boundary.has_deadline && gx_now() >= p->boundary.deadline)
        return p->boundary.deadline_error;
    return gx_nil();
}
bool gx_host_should_submit(gx_HostToken token) {
    gx_owner_check(gx_sched);
    gx_HostPending *p = host_pending(token);
    if (!p)
        gx_host_fault("foreign host submission token");
    if (host_cancellation(p).t == GX_NIL)
        return true;
    /* No native work was submitted, so no native resource can require cleanup. */
    gx_host_publish(token, NULL, 0, NULL);
    gx_host_ack(token);
    return false;
}
static void host_boundary_due(gx_V ignored) {
    (void)ignored;
    gx_host_context_changed();
}
gx_HostToken gx_host_register(gx_Task *task, gx_HostBoundary boundary, gx_V roots, gx_HostDecode decode,
                              gx_HostAction cancel, gx_HostAction cleanup, void *native) {
    gx_Sched *s = gx_sched;
    gx_owner_check(s);
    if (!s->host || s->retiring || task->owner != s || task->done || !decode)
        gx_host_fault("invalid host registration");
    if (boundary.context && boundary.context->owner && boundary.context->owner != s)
        gx_host_fault("foreign host context");
    if (s->operation == UINT64_MAX) gx_host_fault("operation identity exhausted");
    gx_HostPending *p = GC_MALLOC(sizeof(*p));
    if(!p){host_cleanup_action(s,cleanup,native);gx_host_fault("collector registration allocation");}
    p->task = task;
    p->boundary = boundary;
    p->roots = roots;
    p->decode = decode;
    p->cancel = cancel;
    p->cleanup = cleanup;
    p->native = native;
    p->token = (gx_HostToken){s->mailbox, s->generation, ++s->operation, task->id};
    gx_HostLive *l = calloc(1, sizeof(*l));
    if (!l) {
        host_cleanup_action(s,cleanup,native);
        gx_host_fault("cannot allocate native registration");
    }
    l->operation = p->token.operation;
    l->task = task->id;
    p->next = s->pending;
    s->pending = p;
    gx_block(task); /* Install all collector roots and park before native submit. */
    pthread_mutex_lock(&s->mailbox->mutex);
    l->next = s->mailbox->live;
    s->mailbox->live = l;
    s->mailbox->refs++; /* returned token ownership */
    pthread_mutex_unlock(&s->mailbox->mutex);
    if (boundary.has_deadline)
        gx_add_timer_at(boundary.deadline, NULL, host_boundary_due, gx_obj(p));
    return p->token;
}
static void host_note_fault(gx_Sched *s, const char *fault) {
    if (fault && !s->fault)
        s->fault = GC_strdup(fault);
}
static void host_cancel_pending(gx_Sched *s, gx_HostPending *p) {
    if (p->cancel_requested)
        return;
    p->cancel_requested = true;
    host_cleanup_action(s, p->cancel, p->native);
}
void gx_host_context_changed(void) {
    gx_Sched *s = gx_sched;
    if (!s || !s->host || s->retiring)
        return;
    gx_owner_check(s);
    for (gx_HostPending *p = s->pending; p; p = p->next)
        if (host_cancellation(p).t != GX_NIL)
            host_cancel_pending(s, p);
    if (s->fault)
        gx_host_fault(s->fault);
}
/* Detach a ready result from both lists. ACK holes do not alter publication FIFO. */
static gx_HostLive *host_take(bool retiring) {
    gx_Mailbox *b = gx_sched->mailbox;
    pthread_mutex_lock(&b->mutex);
    gx_HostLive *chosen = NULL, *prev = NULL;
    for (gx_HostLive *l = b->head; l; prev = l, l = l->published_next) {
        if (!l->ack)
            continue;
        chosen = l;
        if (prev)
            prev->published_next = l->published_next;
        else
            b->head = l->published_next;
        if (b->tail == l)
            b->tail = prev;
        break;
    }
    if (!chosen && retiring)
        for (gx_HostLive *l = b->live; l; l = l->next)
            if (l->ack && !l->published) {
                chosen = l;
                break;
            }
    if (chosen) {
        gx_HostLive **link = &b->live;
        while (*link != chosen)
            link = &(*link)->next;
        *link = chosen->next;
        chosen->next = chosen->published_next = NULL;
    }
    pthread_mutex_unlock(&b->mutex);
    return chosen;
}
static void host_remove_pending(gx_HostPending *p) {
    size_t n = 0, old = gx_sched->ntimers;
    for (size_t i = 0; i < old; i++)
        if (gx_sched->timers[i].arg.u.p != p)
            gx_sched->timers[n++] = gx_sched->timers[i];
    if (n < old)
        memset(gx_sched->timers + n, 0, (old - n) * sizeof(gx_Timer));
    gx_sched->ntimers = n;
    gx_HostPending **link = &gx_sched->pending;
    while (*link && *link != p)
        link = &(*link)->next;
    if (*link)
        *link = p->next;
    p->next = NULL;
    p->task = NULL;
    p->roots = gx_nil();
    p->boundary.context = NULL;
    p->decode = NULL;
    p->cancel = p->cleanup = NULL;
    p->native = NULL;
}
void gx_host_poll(void) {
    gx_Sched *s = gx_sched;
    gx_owner_check(s);
    if (s->library_poll && !s->retiring) s->library_poll();
    gx_host_context_changed();
    gx_HostLive *l;
    while ((l = host_take(false))) {
        gx_HostToken identity = {s->mailbox, s->generation, l->operation, l->task};
        gx_HostPending *p = host_pending(identity);
        if (!p) {
            host_free_live(l);
            continue;
        }
        gx_Task *task = p->task;
        const char *failure = l->fault;
        if (!failure) {
            jmp_buf decoder_escape;
            jmp_buf *saved_escape = s->escape;
            gx_Handler *saved_handler = gx_handler;
            s->escape = &decoder_escape;
            if (!setjmp(decoder_escape)) {
                gx_Handler h;
                if (!GX_TRY(h)) {
                    failure = p->decode(task, l->bytes, l->length, host_cancellation(p), p->roots);
                    GX_END(h);
                } else
                    failure = "source panic in native decoder";
            } else
                failure = s->fault ? s->fault : "native decoder fatal";
            s->escape = saved_escape;
            gx_handler = saved_handler;
            gx_source_depth = 0;
        }
        host_note_fault(s, failure);
        /* Decode status is explicit: registration cleanup still runs after failure. */
        host_cleanup_action(s, p->cleanup, p->native);
        host_remove_pending(p);
        host_free_live(l);
        if (s->fault)
            gx_host_fault(s->fault);
        gx_ready(task);
    }
}
void gx_host_wait(int64_t deadline, bool timed) {
    gx_Sched *s = gx_sched;
    gx_Mailbox *b = s->mailbox;
    if (!gx_owner_current(s)) gx_host_fault("foreign mailbox driver wait");
    if (s->library_poll && !s->retiring) {
        int64_t cap = gx_deadline(gx_now(), 10000000);
        if (!timed || deadline > cap) { deadline = cap; timed = true; }
    }
    pthread_mutex_lock(&b->mutex);
    uint64_t version = b->version;
    /* Check the predicate under the same mutex as publication/ACK. */
    bool ready = false;
    for (gx_HostLive *l = b->live; l; l = l->next)
        if (l->ack && (s->retiring || l->published)) {
            ready = true;
            break;
        }
    while (!ready && version == b->version && !b->closed) {
        int rc;
        if (timed) {
            struct timespec now;
            if (clock_gettime(CLOCK_MONOTONIC, &now)) {
                pthread_mutex_unlock(&b->mutex);
                gx_host_fault("monotonic wait clock failed");
            }
            if (now.tv_sec < s->epoch.tv_sec || (now.tv_sec == s->epoch.tv_sec && now.tv_nsec < s->epoch.tv_nsec)) {
                pthread_mutex_unlock(&b->mutex);
                gx_host_fault("monotonic wait clock regressed");
            }
            uint64_t sec = (uint64_t)now.tv_sec - (uint64_t)s->epoch.tv_sec;
            int64_t nanoseconds = now.tv_nsec - s->epoch.tv_nsec;
            if (nanoseconds < 0) {
                sec--;
                nanoseconds += 1000000000LL;
            }
            int64_t elapsed = INT64_MAX;
            if (sec <= (uint64_t)INT64_MAX / 1000000000ULL) {
                uint64_t value = sec * 1000000000ULL + (uint64_t)nanoseconds;
                if (value <= INT64_MAX)
                    elapsed = (int64_t)value;
            }
            s->clock = elapsed;
            int64_t remain = deadline - elapsed;
            if (remain <= 0)
                break;
            /* A one-day cap avoids time_t/timespec overflow at i64::MAX. */
            if (remain > 86400000000000LL)
                remain = 86400000000000LL;
            _Static_assert(sizeof(time_t) <= sizeof(uint64_t), "host time_t exceeds supported wire range");
            uint64_t time_max = UINT64_MAX >> (64 - sizeof(time_t) * CHAR_BIT + ((time_t)-1 < 0));
            uint64_t sec_add = (uint64_t)(remain / 1000000000LL);
            if ((uint64_t)now.tv_sec > time_max - sec_add - 1) {
                now.tv_sec = (time_t)time_max;
                now.tv_nsec = 999999999L;
            } else {
                now.tv_sec += (time_t)sec_add;
                now.tv_nsec += remain % 1000000000LL;
                if (now.tv_nsec >= 1000000000L) {
                    now.tv_sec++;
                    now.tv_nsec -= 1000000000L;
                }
            }
            rc = pthread_cond_timedwait(&b->wake, &b->mutex, &now);
        } else
            rc = pthread_cond_wait(&b->wake, &b->mutex);
        if (rc == ETIMEDOUT)
            break;
        if (rc) {
            pthread_mutex_unlock(&b->mutex);
            gx_host_fault("native mailbox wait failed");
        }
    }
    pthread_mutex_unlock(&b->mutex);
}

static void host_cleanup_action(gx_Sched *s, gx_HostAction action, void *arg) {
    if (!action)
        return;
    jmp_buf escape;
    jmp_buf *saved = s->escape;
    gx_Handler *handler = gx_handler;
    s->escape = &escape;
    if (!setjmp(escape)) {
        gx_Handler h;
        if (!GX_TRY(h)) {
            host_note_fault(s, action(arg));
            GX_END(h);
        } else
            host_note_fault(s, "source panic in native cleanup");
    }
    s->escape = saved;
    gx_handler = handler;
    gx_source_depth = 0;
}
static void host_task_cleanup(gx_Sched *s, gx_Task *t) {
    if (!t->cleanup)
        return;
    void (*action)(void *) = t->cleanup;
    void *arg = t->cleanup_arg;
    t->cleanup = NULL;
    t->cleanup_arg = NULL;
    jmp_buf escape;
    jmp_buf *saved = s->escape;
    gx_Handler *handler = gx_handler;
    s->escape = &escape;
    if (!setjmp(escape)) {
        gx_Handler h;
        if (!GX_TRY(h)) {
            action(arg);
            GX_END(h);
        } else
            host_note_fault(s, "source panic in registration cleanup");
    }
    s->escape = saved;
    gx_handler = handler;
    gx_source_depth = 0;
}
static void clear_frame_graph(gx_Frame *first) {
    size_t n = 0, cap = 16;
    gx_Frame **roots = GC_MALLOC(cap * sizeof(*roots));
    if (first)
        roots[n++] = first;
    for (size_t i = 0; i < n; i++) {
        gx_Frame *f = roots[i];
        gx_Frame *children[3] = {f->parent, f->a, f->b};
        for (int j = 0; j < 3; j++)
            if (children[j]) {
                bool seen = false;
                for (size_t k = 0; k < n; k++)
                    if (roots[k] == children[j])
                        seen = true;
                if (seen)
                    continue;
                if (n == cap) {
                    cap *= 2;
                    gx_Frame **next_roots = GC_MALLOC(cap * sizeof(*roots));
                    memcpy(next_roots, roots, n * sizeof(*roots));
                    roots = next_roots;
                }
                roots[n++] = children[j];
            }
        f->parent = f->a = f->b = NULL;
        f->l = NULL;
        f->nl = 0;
        f->defers = NULL;
        f->panicking = NULL;
        f->prim = NULL;
        f->prim_arg = NULL;
    }
    GC_reachable_here(roots);
}
static void retire_owner(gx_Sched *s) {
    if (s->retiring || s->retired)
        return;
    gx_owner_check(s);
    s->retiring = true;
    if (s->host) {
        for (gx_HostPending *p = s->pending; p; p = p->next) {
            if (!p->cancel_requested) {
                p->cancel_requested = true;
                host_cleanup_action(s, p->cancel, p->native);
            }
        }
        while (s->pending) {
            gx_HostLive *l = host_take(true);
            if (!l) {
                gx_host_wait(0, false);
                continue;
            }
            gx_HostToken identity = {s->mailbox, s->generation, l->operation, l->task};
            gx_HostPending *p = host_pending(identity);
            if (p) {
                host_cleanup_action(s, p->cleanup, p->native);
                host_remove_pending(p);
            }
            host_free_live(l);
        }
        gx_Mailbox *b = s->mailbox;
        pthread_mutex_lock(&b->mutex);
        b->closed = true;
        b->version++;
        pthread_cond_broadcast(&b->wake);
        gx_HostLive *l = b->live;
        b->live = b->head = b->tail = NULL;
        pthread_mutex_unlock(&b->mutex);
        while (l) {
            gx_HostLive *next_live = l->next;
            host_free_live(l);
            l = next_live;
        }
        s->mailbox = NULL;
        gx_host_token_release((gx_HostToken){.mailbox = b}); /* owner reference */
    }
    /* Lists stay scanned and owner-visible for the whole cleanup batch. */
    for (gx_Task *t = s->all; t; t = t->next_all) {
        host_task_cleanup(s, t);
        clear_frame_graph(t->frame);
        t->frame = NULL;
        t->rv = NULL;
        t->nrv = 0;
        t->resume_panic = t->cur_panic = NULL;
        t->blocked = t->queued = false;
        t->done = true;
    }
    gx_Task *task = s->all;
    s->all = NULL;
    while (task) {
        gx_Task *next_task = task->next_all;
        task->next_all = NULL;
        task = next_task;
    }
    for (gx_Context *c = s->contexts; c; c = c->next_owner) {
        c->parent = NULL;
        c->children = NULL;
        c->n = c->cap = 0;
        c->done = c->err = gx_nil();
    }
    gx_Context *ctx = s->contexts;
    s->contexts = NULL;
    while (ctx) {
        gx_Context *next_context = ctx->next_owner;
        ctx->next_owner = NULL;
        ctx = next_context;
    }
    s->runq = NULL;
    s->qlen = s->qcap = s->qhead = 0;
    s->timers = NULL;
    s->ntimers = s->tcap = 0;
    s->cur = NULL;
    s->retired = true;
    s->retiring = false;
}

typedef struct gx_HostEntry {
    void (*init)(void);
    gx_V (*entry)(void);
    int status;
    bool library;
    void (*finish)(gx_Task *);
    void (*poll)(void);
    uint8_t **report;
    size_t *length;
} gx_HostEntry;
static void *host_entry_thread(void *arg) {
    gx_HostEntry *entry = arg;
    if(entry->library){gx_retire(gx_sched);gx_sched=NULL;gx_handler=NULL;gx_thrown=NULL;gx_source_depth=0;}
    gx_Sched *s = gx_new_sched(gx_new_task(0, NULL), false);
    gx_sched = s;
    s->host = true;
    s->library_poll = entry->poll;
    s->retire = retire_owner;
    jmp_buf escape;
    s->escape = &escape;
    if (!setjmp(escape)) {
        if (clock_gettime(CLOCK_MONOTONIC, &s->epoch))
            gx_host_fault("cannot initialize monotonic epoch");
        if (host_generation == UINT64_MAX) gx_host_fault("owner generation exhausted");
        gx_Mailbox *b = calloc(1, sizeof(*b));
        if (!b)
            gx_host_fault("cannot allocate native mailbox");
        if (pthread_mutex_init(&b->mutex, NULL)) {
            free(b);
            gx_host_fault("cannot initialize mailbox mutex");
        }
        pthread_condattr_t attr;
        int rc = pthread_condattr_init(&attr);
        bool attr_ready = !rc;
        if (!rc)
            rc = pthread_condattr_setclock(&attr, CLOCK_MONOTONIC);
        if (!rc)
            rc = pthread_cond_init(&b->wake, &attr);
        if (attr_ready)
            pthread_condattr_destroy(&attr);
        if (rc) {
            pthread_mutex_destroy(&b->mutex);
            free(b);
            gx_host_fault("cannot initialize monotonic mailbox condition");
        }
        b->refs = 1;
        b->generation = ++host_generation;
        s->generation = b->generation;
        s->mailbox = b;
        if(entry->library){pthread_mutex_lock(&library_wake_mutex);library_wake_token=(gx_HostToken){.mailbox=b,.generation=b->generation};gx_host_token_retain(library_wake_token);pthread_mutex_unlock(&library_wake_mutex);}
        gx_Handler h;
        if (GX_TRY(h))
            gx_report_panic(gx_thrown);
        entry->init();
        s->cur->frame = gx_frameof(entry->entry());
        GX_END(h);
        gx_Task *main = s->cur;
        gx_ready(main);
        while (!main->done)
            run(next());
        check_future_panics();
        if (entry->finish) entry->finish(main);
    }
    gx_handler = NULL;
    gx_source_depth = 0;
    /* Error/String methods are source calls: render while this owner is live. */
    volatile gx_Buf panic_report = {0};
    if (s->panic) {
        jmp_buf rendering;
        s->escape = &rendering;
        if (!setjmp(rendering)) {
            gx_Handler h;
            if (!GX_TRY(h)) {
                panic_report = gx_panic_report(s->panic);
                GX_END(h);
            } else {
                gx_Buf fallback = {0};
                gx_buf_put(&fallback, "panic: panic while formatting panic\n",
                           sizeof("panic: panic while formatting panic\n") - 1);
                panic_report = fallback;
            }
        } else {
            gx_Buf fallback = {0};
            gx_buf_put(&fallback, "panic: fault while formatting panic\n",
                       sizeof("panic: fault while formatting panic\n") - 1);
            panic_report = fallback;
        }
        s->escape = &escape;
    }
    gx_handler = NULL;
    gx_thrown = NULL;
    gx_source_depth = 0;
    s->panic = NULL;
    if (s->mailbox)
        retire_owner(s);
    else {
        s->host = false;
        retire_owner(s);
        s->host = true;
    }
    s->escape = NULL;
    gx_handler = NULL;
    gx_thrown = NULL;
    gx_source_depth = 0;
    if (s->native_cleanup) s->native_cleanup();
    if (entry->library) {
        const uint8_t *bytes = panic_report.b;
        size_t n = panic_report.n;
        if (s->fault) { entry->status = 3; bytes = (const uint8_t *)s->fault; n = strlen(s->fault); }
        else if (s->fatal) { entry->status = 4; bytes = (const uint8_t *)s->fatal; n = strlen(s->fatal); }
        else if (bytes) entry->status = 2;
        if (n) {
            *entry->report = malloc(n);
            if (!*entry->report) { entry->status = 3; *entry->length = 0; }
            else { memcpy(*entry->report, bytes, n); *entry->length = n; }
        }
        gx_sched=NULL;
        return NULL;
    }
    if (panic_report.b) {
        gx_stderr(panic_report.b, panic_report.n);
        s->panic = NULL;
        exit(2);
    }
    if (s->fatal) {
        entry->status = 2;
        gx_fatal(s->fatal);
    }
    if (s->fault) {
        entry->status = 3;
        gx_stderr("goalchemy host fault: ", 22);
        gx_stderr(s->fault, strlen(s->fault));
        gx_stderr("\n", 1);
    }
    return NULL;
}
int gx_run_main_host(void (*init)(void), gx_V (*entry)(void)) {
    if (!gx_entry_reserve())
        return 1;
    GC_INIT();
    /* Reservation protects the process-static executable ABI before any roots change. */
    gx_retire(gx_sched);
    gx_sched = NULL;
    gx_handler = NULL;
    gx_thrown = NULL;
    gx_source_depth = 0;
    gx_HostEntry args = {init, entry, 0};
    pthread_attr_t attr;
    int rc = pthread_attr_init(&attr);
    bool attr_ready = !rc;
    if (!rc)
        rc = pthread_attr_setstacksize(&attr, (size_t)8 << 20);
    pthread_t thread;
    if (!rc)
        rc = GC_pthread_create(&thread, &attr, host_entry_thread, &args);
    if (attr_ready)
        pthread_attr_destroy(&attr);
    if (!rc && GC_pthread_join(thread, NULL))
        gx_host_fault("cannot join executable owner thread");
    gx_entry_release();
    return rc ? 3 : args.status;
}
_Noreturn void gx_host_main(void (*init)(void), gx_V (*entry)(void)) { exit(gx_run_main_host(init, entry)); }

/* The primordial registration belongs to this SDK-owned owner, not a foreign
 * invoking thread. Boehm8.2.8 explicitly allows its one-time unregister. */
static void *library_initial_owner(void *arg) {
    gx_HostEntry *entry=arg;
    struct GC_stack_base base;
    if(GC_is_init_called() || GC_get_stack_base(&base)!=GC_SUCCESS){entry->status=3;return NULL;}
    GC_set_stackbottom(NULL,&base); /* documented pre-init primordial stack */
    GC_INIT();
    GC_allow_register_threads();
    void *result=host_entry_thread(arg);
    if(GC_unregister_my_thread()!=GC_SUCCESS)abort();
    return result;
}
int gx_run_library_host(void (*init)(void), gx_V (*entry)(void),
    void (*finish)(gx_Task *), void (*poll)(void), uint8_t **report, size_t *length) {
    if (!gx_entry_reserve()) return 1;
    bool initial=!GC_is_init_called();
    gx_HostEntry args = {.init=init, .entry=entry, .library=true, .finish=finish,
        .poll=poll, .report=report, .length=length};
    pthread_attr_t attr;
    int rc = pthread_attr_init(&attr);
    bool ready = !rc;
    if (!rc) rc = pthread_attr_setstacksize(&attr, (size_t)8 << 20);
    pthread_t thread;
    if (!rc) rc = initial?pthread_create(&thread,&attr,library_initial_owner,&args):GC_pthread_create(&thread,&attr,host_entry_thread,&args);
    if (ready) pthread_attr_destroy(&attr);
    if (!rc && (initial?pthread_join(thread,NULL):GC_pthread_join(thread,NULL))) abort();
    pthread_mutex_lock(&library_wake_mutex);gx_HostToken wake=library_wake_token;library_wake_token=(gx_HostToken){0};pthread_mutex_unlock(&library_wake_mutex);
    gx_host_token_release(wake);
    gx_entry_release();
    return rc ? 3 : args.status;
}
