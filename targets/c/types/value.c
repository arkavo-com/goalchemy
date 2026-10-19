/* Values, strings, storage, equality, and key encoding. */
#include "gx.h"

#include <stdio.h>
#include <stdlib.h>
#include <math.h>
#include <stdatomic.h>

#ifndef GX_MICRO
_Noreturn void gx_fault(const char *msg) {
    if (gx_sched && gx_sched->host && !gx_sched->retired && pthread_equal(gx_sched->thread, pthread_self())) gx_host_fault(msg);
    fprintf(stderr, "goalchemy runtime fault: goalchemy fault: %s\n", msg);
    fflush(stderr);
    abort();
}

#endif

gx_V gx_str(const char *b, size_t n) {
    gx_V v = {0};
    v.t = GX_STR;
    v.l = (uint32_t)n;
    if (n > 0) {
        char *p = GC_MALLOC_ATOMIC(n);
        memcpy(p, b, n);
        v.u.p = p;
    }
    return v;
}

gx_V gx_cstr(const char *s) { return gx_str(s, strlen(s)); }

gx_V *gx_alloc_vals(size_t n) {
    if (n > SIZE_MAX / sizeof(gx_V)) gx_fault("allocation exceeds host limits");
    gx_V *p = GC_MALLOC(n ? n * sizeof(gx_V) : sizeof(gx_V));
    if (!p) gx_fault("allocation failed");
    return p;
}

uint8_t *gx_alloc_bytes(size_t n) {
    if (n > UINT32_MAX / 2) gx_fault("allocation exceeds host limits");
    uint8_t *p = GC_MALLOC_ATOMIC(n ? n : 1);
    if (!p) gx_fault("allocation failed");
    /* Atomic allocation need not be cleared by every collector build. */
    memset(p, 0, n ? n : 1);
    return p;
}

gx_V gx_byte_array(size_t n) {
    gx_V v = gx_obj(gx_alloc_bytes(n)); v.pad = 1; return v;
}

gx_V gx_byte_array_clone(gx_V x, size_t n) {
    gx_V v = gx_byte_array(n);
    if (n) memcpy(v.u.p, x.u.p, n);
    return v;
}

void gx_byte_array_set(gx_V d, gx_V s, size_t n) {
    if (n) memmove(d.u.p, s.u.p, n);
}

bool gx_byte_array_eq(gx_V a, gx_V b, size_t n) {
    return !n || memcmp(a.u.p, b.u.p, n) == 0;
}

void gx_byte_array_key(gx_V a, size_t n, gx_Buf *out) {
    gx_key_open(out);
    /* Keep the existing fixed-array scalar key encoding. */
    for (size_t i = 0; i < n; i++) gx_vkey(gx_int(gx_bytes(a)[i]), out);
    gx_key_close(out);
}

gx_V gx_new_vals(size_t n, const gx_V *init) {
    gx_V *p = gx_alloc_vals(n);
    if (init && n) memcpy(p, init, n * sizeof(gx_V));
    gx_V v = {0};
    v.t = GX_OBJ;
    v.u.p = p;
    return v;
}

gx_V gx_cellv(gx_V x) {
    gx_V *p = gx_alloc_vals(1);
    p[0] = x;
    return gx_ptr(p);
}

gx_V gx_pget(gx_V p) {
    if (p.t != GX_PTR) gx_panic_nil_deref();
    return *(gx_V *)p.u.p;
}

void gx_pset(gx_V p, gx_V x) {
    if (p.t != GX_PTR) gx_panic_nil_deref();
    *(gx_V *)p.u.p = x;
}

gx_V gx_nilchk(gx_V v) {
    if (v.t == GX_NIL) gx_panic_nil_deref();
    return v;
}

gx_V gx_fld(gx_V s, size_t k) {
    if (s.t != GX_OBJ) gx_panic_nil_deref();
    return ((gx_V *)s.u.p)[k];
}

void gx_fset(gx_V s, size_t k, gx_V x) {
    if (s.t != GX_OBJ) gx_panic_nil_deref();
    ((gx_V *)s.u.p)[k] = x;
}

gx_V gx_fptr(gx_V s, size_t k) {
    if (s.t != GX_OBJ) gx_panic_nil_deref();
    return gx_ptr((gx_V *)s.u.p + k);
}

gx_V gx_aget(gx_V a, gx_V i, size_t n) {
    size_t k = gx_idx(i, n);
    gx_nilchk(a);
    return gx_byte_backing(a) ? gx_int(gx_bytes(a)[k]) : gx_vals(a)[k];
}

gx_V gx_agetu(gx_V a, gx_V i, size_t n) {
    size_t k = gx_idxu(i, n);
    gx_nilchk(a);
    return gx_byte_backing(a) ? gx_int(gx_bytes(a)[k]) : gx_vals(a)[k];
}

void gx_aset(gx_V a, gx_V i, size_t n, gx_V x) {
    size_t k = gx_idx(i, n);
    gx_nilchk(a);
    if (gx_byte_backing(a)) gx_bytes(a)[k] = (uint8_t)x.u.i;
    else gx_vals(a)[k] = x;
}

void gx_asetu(gx_V a, gx_V i, size_t n, gx_V x) {
    size_t k = gx_idxu(i, n);
    gx_nilchk(a);
    if (gx_byte_backing(a)) gx_bytes(a)[k] = (uint8_t)x.u.i;
    else gx_vals(a)[k] = x;
}

gx_V gx_tuple(int n, const gx_V *vs) {
    gx_V *p = gx_alloc_vals((size_t)n);
    if (n) memcpy(p, vs, (size_t)n * sizeof(gx_V));
    gx_V v = {0};
    v.t = GX_TUPLE;
    v.l = (uint32_t)n;
    v.u.p = p;
    return v;
}

gx_V gx_lenv(gx_V s) { return gx_int(s.l); }
gx_V gx_capv(gx_V s) { return gx_int(s.c); }
gx_V gx_strlen(gx_V s) { return gx_int(s.l); }
bool gx_nil_slice_p(gx_V s) { return s.u.p == NULL; }

void gx_buf_put(gx_Buf *b, const void *p, size_t n) {
    if (b->n + n > b->cap) {
        size_t c = b->cap ? b->cap * 2 : 32;
        while (c < b->n + n) c *= 2;
        uint8_t *nb = GC_MALLOC_ATOMIC(c);
        if (b->n) memcpy(nb, b->b, b->n);
        b->b = nb;
        b->cap = c;
    }
    if (n) memcpy(b->b + b->n, p, n);
    b->n += n;
}

bool gx_veq(gx_V a, gx_V b) {
    if (a.t != b.t) return false;
    switch (a.t) {
    case GX_NIL:
        return true;
    case GX_BOOL:
    case GX_INT:
        return a.u.i == b.u.i;
    case GX_FLOAT:
        return a.u.f == b.u.f;
    case GX_STR:
        return a.l == b.l && (a.l == 0 || memcmp(a.u.p, b.u.p, a.l) == 0);
    case GX_OBJ:
    case GX_PTR:
        return a.u.p == b.u.p;
    }
    return false;
}

void gx_vkey(gx_V v, gx_Buf *out) {
    uint8_t tag;
    switch (v.t) {
    case GX_NIL:
        gx_buf_put(out, "N", 1);
        return;
    case GX_BOOL:
        tag = v.u.i ? 'T' : 'F';
        gx_buf_put(out, &tag, 1);
        return;
    case GX_INT:
        gx_buf_put(out, "I", 1);
        gx_buf_put(out, &v.u.i, 8);
        return;
    case GX_FLOAT: {
        uint64_t raw;
        if (isnan(v.u.f)) {
#ifdef GX_MICRO
            /* Micro entry is serialized; no 64-bit atomic library on RV32. */
            static uint64_t next_nan = 0;
            raw = next_nan++;
#else
            static _Atomic uint64_t next_nan = 0;
            raw = atomic_fetch_add(&next_nan, 1);
#endif
            if (raw == UINT64_MAX) gx_fault("NaN key identity exhausted");
            gx_buf_put(out, "Q", 1);
        } else {
            double x = v.u.f == 0.0 ? 0.0 : v.u.f;
            memcpy(&raw, &x, sizeof(raw)); gx_buf_put(out, "D", 1);
        }
        gx_buf_put(out, &raw, sizeof(raw)); return;
    }
    case GX_STR:
        gx_buf_put(out, "S", 1);
        gx_buf_put(out, &v.l, 4);
        gx_buf_put(out, v.u.p, v.l);
        return;
    case GX_OBJ:
    case GX_PTR:
        gx_buf_put(out, "P", 1);
        gx_buf_put(out, &v.u.p, sizeof(void *));
        return;
    }
    gx_fault("unhashable host value");
}

void gx_key_open(gx_Buf *out) { gx_buf_put(out, "(", 1); }
void gx_key_close(gx_Buf *out) { gx_buf_put(out, ")", 1); }

gx_V gx_zero_nil(void) { return gx_nil(); }
gx_V gx_zero_int(void) { return gx_int(0); }
gx_V gx_zero_bool(void) { return gx_bool(false); }
gx_V gx_zero_string(void) { return gx_str(NULL, 0); }
gx_V gx_zero_slice(void) { return gx_nil_slice(); }

gx_V gx_zero_byte_slice(void) { return gx_nil_byte_slice(); }

/* Spare slots belong to the declared element type, including nested values. */
gx_V gx_zero_append_growth(gx_V result, gx_V previous, gx_ZeroFn zero) {
    if(result.u.p != previous.u.p && !gx_byte_backing(result))
        for(uint32_t i=result.l;i<result.c;i++) gx_vals(result)[i]=zero();
    return result;
}
