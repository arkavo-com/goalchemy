/* Program entry, unrecovered-panic reports, and opaque values. */
#include "gx.h"

#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

int gx_blocked_signal;

#ifndef GX_MICRO
void gx_stderr(const void *b, size_t n) {
    const char *p = b;
    while (n > 0) {
        ssize_t w = write(2, p, n);
        if (w <= 0) return;
        p += w;
        n -= (size_t)w;
    }
}

#endif

static void indented(gx_V s, gx_Buf *out) {
    const uint8_t *b = gx_sbytes(s);
    for (size_t i = 0; i < s.l; i++) {
        gx_buf_put(out, &b[i], 1);
        if (b[i] == '\n') gx_buf_put(out, "\t", 1);
    }
}

void gx_format_panic_value(gx_V v, gx_Buf *out) {
    const gx_TypeDesc *t = gx_dyn_type(v);
    if (!t) {
        gx_buf_put(out, "nil", 3);
        return;
    }
    gx_V x = gx_unboxed(v);
    const char *names[] = {"Error", "String"};
    for (int i = 0; i < 2; i++) {
        const gx_Method *m = gx_method(t, names[i]);
        if (m) {
            gx_V a[1] = {x};
            indented(m->code(NULL, a, 1), out);
            return;
        }
    }
    bool builtin = strchr(t->name, '.') == NULL;
    char buf[32];
    gx_Buf inner = {0};
    bool quote = false;
    if (strcmp(t->basic, "string") == 0) {
        indented(x, &inner);
        quote = true;
    } else if (strcmp(t->basic, "bool") == 0) {
        gx_buf_put(&inner, x.u.i ? "true" : "false", x.u.i ? 4 : 5);
    } else if (strcmp(t->basic, "int") == 0) {
        int n = snprintf(buf, sizeof buf, "%lld", (long long)x.u.i);
        gx_buf_put(&inner, buf, (size_t)n);
    } else if (strcmp(t->basic, "float32") == 0 || strcmp(t->basic, "float64") == 0) {
        gx_V formatted = gx_float_print(gx_f(x), strcmp(t->basic, "float32") == 0 ? 32 : 64);
        gx_buf_put(&inner, formatted.u.p, formatted.l);
    } else if (strcmp(t->basic, "uint") == 0) {
        int n = snprintf(buf, sizeof buf, "%llu", (unsigned long long)(uint64_t)x.u.i);
        gx_buf_put(&inner, buf, (size_t)n);
    } else {
        gx_buf_put(out, "(", 1);
        gx_buf_put(out, t->name, strlen(t->name));
        gx_buf_put(out, ") 0xc000000000", 14);
        return;
    }
    if (builtin) {
        gx_buf_put(out, inner.b, inner.n);
        return;
    }
    gx_buf_put(out, t->name, strlen(t->name));
    gx_buf_put(out, quote ? "(\"" : "(", quote ? 2 : 1);
    gx_buf_put(out, inner.b, inner.n);
    gx_buf_put(out, quote ? "\")" : ")", quote ? 2 : 1);
}

static void format_chain(gx_Panic *p, gx_Buf *out) {
    if (p->prev) {
        format_chain(p->prev, out);
        gx_buf_put(out, "\t", 1);
    }
    gx_buf_put(out, "panic: ", 7);
    gx_format_panic_value(p->value, out);
    if (p->recovered) gx_buf_put(out, " [recovered]", 12);
    gx_buf_put(out, "\n", 1);
}

gx_Buf gx_panic_report(gx_Panic *p) { gx_Buf b = {0}; format_chain(p, &b); return b; }

/* The report of an unrecovered panic as a NUL-terminated string. */
const char *gx_panic_text(gx_Panic *p) {
    gx_Buf b = {0};
    format_chain(p, &b);
    if (b.n && b.b[b.n - 1] == '\n') b.n--;
    gx_buf_put(&b, "", 1);
    return (const char *)b.b;
}

#ifndef GX_MICRO
_Noreturn void gx_report_panic(gx_Panic *p) {
    if (gx_sched && gx_sched->host && gx_sched->escape) {
        gx_sched->panic = p;
        gx_handler = NULL;
        gx_source_depth = 0;
        longjmp(*gx_sched->escape, 1);
    }
    gx_Buf b = {0};
    format_chain(p, &b);
    gx_stderr(b.b, b.n);
    exit(2);
}

static void (*large_body)(void);

static void *large_main(void *arg) {
    (void)arg;
    large_body();
    if (getenv("GOALCHEMY_HEAP_STATS")) {
        char buf[128];
        int n = snprintf(buf, sizeof buf, "heap: size %zu collections %zu\n", GC_get_heap_size(), (size_t)GC_get_gc_no());
        gx_stderr(buf, (size_t)n);
    }
    return NULL;
}

/* Runs body on a thread with a large stack so deep recursion behaves. */
void gx_run_large(void (*body)(void)) {
    large_body = body;
    pthread_attr_t a;
    pthread_attr_init(&a);
    pthread_attr_setstacksize(&a, (size_t)1 << 30);
    pthread_t t;
    if (GC_pthread_create(&t, &a, large_main, NULL) != 0) gx_fault("cannot start the main thread");
    pthread_attr_destroy(&a);
    GC_pthread_join(t, NULL);
}

static void (*entry_fn)(void);
static void (*init_fn)(void);

static void program_body(void) {
    gx_Handler h;
    if (GX_TRY(h)) gx_report_panic(gx_thrown);
    init_fn();
    entry_fn();
    GX_END(h);
}

_Noreturn void gx_program_main(void (*init)(void), void (*entry)(void)) {
    if (!gx_entry_reserve()) gx_host_fault("overlapping executable entry");
    GC_INIT();
    gx_retire(gx_sched); gx_sched = NULL;
    init_fn = init;
    entry_fn = entry;
    gx_run_large(program_body);
    gx_entry_release();
    exit(0);
}

gx_V gx_new_mutex(void) {
    gx_Mutex *m = GC_MALLOC(sizeof(gx_Mutex));
    m->kind = GX_KIND_MUTEX;
    return gx_obj(m);
}

gx_V gx_new_waitgroup(void) {
    gx_WaitGroup *w = GC_MALLOC(sizeof(gx_WaitGroup));
    w->kind = GX_KIND_WAITGROUP;
    return gx_obj(w);
}

/* Copies an opaque value object (sync.Mutex, sync.WaitGroup). */
gx_V gx_opaque_clone(gx_V x) {
    gx_V c = *(int *)x.u.p == GX_KIND_MUTEX ? gx_new_mutex() : gx_new_waitgroup();
    gx_opaque_set(c, x);
    return c;
}

void gx_opaque_set(gx_V d, gx_V s) {
    if (*(int *)s.u.p == GX_KIND_MUTEX) {
        gx_Mutex *md = d.u.p, *ms = s.u.p;
        md->locked = ms->locked;
        md->n = md->cap = ms->n;
        md->waiters = GC_MALLOC((ms->n ? ms->n : 1) * sizeof(gx_Task *));
        if (ms->n) memcpy(md->waiters, ms->waiters, ms->n * sizeof(gx_Task *));
    } else {
        gx_WaitGroup *wd = d.u.p, *ws = s.u.p;
        wd->n = ws->n;
        wd->nw = wd->cap = ws->nw;
        wd->waiters = GC_MALLOC((ws->nw ? ws->nw : 1) * sizeof(gx_Task *));
        if (ws->nw) memcpy(wd->waiters, ws->waiters, ws->nw * sizeof(gx_Task *));
    }
}

#endif /* !GX_MICRO */
