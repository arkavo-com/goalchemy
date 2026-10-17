/* Goalchemy C runtime: shared declarations.
 *
 * Every Go value is a gx_V. Integers of every kind are int64_t normalized
 * to their Go width (unsigned 64-bit values keep their bits). Strings are
 * immutable byte arrays. Structs, arrays, slice backing arrays, maps,
 * channels, closures, and interface boxes are Boehm-collected objects;
 * the collector scans stacks, globals, and heap objects conservatively.
 * Go panics unwind with longjmp to the innermost handler. */
#ifndef GX_H
#define GX_H

#ifndef _POSIX_C_SOURCE
#define _POSIX_C_SOURCE 200809L
#endif

#ifndef GC_THREADS
#define GC_THREADS
#endif
#include <pthread.h>
#include <gc.h>
#include <time.h>
#include <setjmp.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

enum { GX_NIL, GX_BOOL, GX_INT, GX_STR, GX_OBJ, GX_PTR, GX_SLICE, GX_TUPLE, GX_FRAME, GX_FLOAT };

/* t is the tag. Strings use p (bytes) and l (length); slices use p (first
 * element, NULL for nil), l, and c; pointers to scalars point at a slot. */
typedef struct gx_V {
    int32_t t;
    uint32_t l;
    uint32_t c;
    uint32_t pad;
    union {
        int64_t i;
        double f;
        void *p;
    } u;
} gx_V;

static inline gx_V gx_nil(void) { gx_V v = {0}; return v; }
static inline gx_V gx_bool(bool b) { gx_V v = {0}; v.t = GX_BOOL; v.u.i = b; return v; }
static inline gx_V gx_int(int64_t i) { gx_V v = {0}; v.t = GX_INT; v.u.i = i; return v; }
static inline gx_V gx_float(double f) { gx_V v = {0}; v.t = GX_FLOAT; v.u.f = f; return v; }
static inline double gx_f(gx_V v) { return v.u.f; }
static inline gx_V gx_obj(void *p) { gx_V v = {0}; v.t = p ? GX_OBJ : GX_NIL; v.u.p = p; return v; }
static inline gx_V gx_ptr(gx_V *p) { gx_V v = {0}; v.t = GX_PTR; v.u.p = p; return v; }
static inline gx_V gx_slice(gx_V *p, uint32_t l, uint32_t c) { gx_V v = {0}; v.t = GX_SLICE; v.u.p = p; v.l = l; v.c = c; return v; }
static inline gx_V gx_nil_slice(void) { return gx_slice(NULL, 0, 0); }
static inline int64_t gx_i(gx_V v) { return v.u.i; }
static inline bool gx_b(gx_V v) { return v.u.i != 0; }
static inline bool gx_is_nil(gx_V v) { return v.t == GX_NIL; }
/* pad=1 marks pointer-free native uint8 backing on arrays and slice headers.
 * Preserve this hint even on nil and empty headers. Interior pointers are
 * traced by Boehm's existing all-interior-pointers configuration. */
static inline bool gx_byte_backing(gx_V v) { return v.pad == 1; }
static inline uint8_t *gx_bytes(gx_V v) { return (uint8_t *)v.u.p; }
static inline gx_V gx_byte_slice(uint8_t *p, uint32_t l, uint32_t c) {
    gx_V v = gx_slice(NULL, l, c); v.u.p = p; v.pad = 1; return v;
}
static inline gx_V gx_nil_byte_slice(void) { return gx_byte_slice(NULL, 0, 0); }
_Noreturn void gx_fault(const char *msg);
static inline gx_V *gx_vals(gx_V v) {
    if (gx_byte_backing(v)) gx_fault("native byte backing used as gx_V storage");
    return (gx_V *)v.u.p;
}

/* IEEE scalar helpers. */
gx_V gx_float_add_f32(gx_V a, gx_V b);
gx_V gx_float_add_f64(gx_V a, gx_V b);
gx_V gx_float_sub_f32(gx_V a, gx_V b);
gx_V gx_float_sub_f64(gx_V a, gx_V b);
gx_V gx_float_mul_f32(gx_V a, gx_V b);
gx_V gx_float_mul_f64(gx_V a, gx_V b);
gx_V gx_float_div_f32(gx_V a, gx_V b);
gx_V gx_float_div_f64(gx_V a, gx_V b);
gx_V gx_float_min_f32(gx_V a, gx_V b);
gx_V gx_float_min_f64(gx_V a, gx_V b);
gx_V gx_float_max_f32(gx_V a, gx_V b);
gx_V gx_float_max_f64(gx_V a, gx_V b);
gx_V gx_float_neg_f32(gx_V a);
gx_V gx_float_neg_f64(gx_V a);
gx_V gx_float_convert(gx_V a, int bits);
gx_V gx_integer_float_convert(gx_V a, bool uns, int bits);
gx_V gx_float_integer_convert(gx_V a, int bits, bool sign);

double gx_round_float(double x, int bits);
gx_V gx_float_print(double x, int bits);
double gx_integer_float(int64_t x, bool unsigned_value, int bits);
int64_t gx_float_integer(double x, int bits, bool sign);
double gx_float_min(double a, double b);
double gx_float_max(double a, double b);
gx_V gx_zero_float(void);

/* Strings. */
gx_V gx_str(const char *b, size_t n);
gx_V gx_cstr(const char *s);
static inline const uint8_t *gx_sbytes(gx_V v) { return (const uint8_t *)v.u.p; }
static inline size_t gx_slen(gx_V v) { return v.l; }

/* Storage. */
gx_V *gx_alloc_vals(size_t n);
uint8_t *gx_alloc_bytes(size_t n);
gx_V gx_byte_array(size_t n);
gx_V gx_byte_array_clone(gx_V x, size_t n);
void gx_byte_array_set(gx_V d, gx_V s, size_t n);
bool gx_byte_array_eq(gx_V a, gx_V b, size_t n);

gx_V gx_new_vals(size_t n, const gx_V *init);
gx_V gx_cellv(gx_V v);
gx_V gx_pget(gx_V p);
void gx_pset(gx_V p, gx_V x);
gx_V gx_fld(gx_V s, size_t k);
void gx_fset(gx_V s, size_t k, gx_V x);
gx_V gx_fptr(gx_V s, size_t k);
gx_V gx_aget(gx_V a, gx_V i, size_t n);
gx_V gx_agetu(gx_V a, gx_V i, size_t n);
void gx_aset(gx_V a, gx_V i, size_t n, gx_V x);
void gx_asetu(gx_V a, gx_V i, size_t n, gx_V x);
gx_V gx_nilchk(gx_V v);
gx_V gx_tuple(int n, const gx_V *vs);
static inline gx_V gx_at(gx_V t, int k) { return ((gx_V *)t.u.p)[k]; }
gx_V gx_lenv(gx_V s);
gx_V gx_capv(gx_V s);
gx_V gx_strlen(gx_V s);
bool gx_nil_slice_p(gx_V s);

/* Equality and map keys: keys are encoded as byte strings. */
typedef struct gx_Buf {
    uint8_t *b;
    size_t n, cap;
} gx_Buf;
void gx_buf_put(gx_Buf *b, const void *p, size_t n);
typedef bool (*gx_EqFn)(gx_V a, gx_V b);
typedef void (*gx_KeyFn)(gx_V v, gx_Buf *out);
bool gx_veq(gx_V a, gx_V b);
void gx_vkey(gx_V v, gx_Buf *out);
void gx_key_open(gx_Buf *out);
void gx_key_close(gx_Buf *out);
void gx_byte_array_key(gx_V a, size_t n, gx_Buf *out);

/* Native non-cryptographic checksum capability. */
gx_V gx_lib_checksum_crc32_ieee(gx_V data);

/* Functions, dynamic types, and interfaces. */
typedef gx_V (*gx_Code)(gx_V *env, gx_V *args, int n);
typedef struct gx_Func {
    int64_t fid;
    gx_Code code;
    int n;
    gx_V env[];
} gx_Func;
typedef struct gx_Method {
    const char *id;
    int64_t fid;
    gx_Code code;
} gx_Method;
typedef struct gx_TypeDesc {
    uint32_t id;
    const char *name;
    const char *kind;
    gx_EqFn eq;
    gx_KeyFn key;
    const gx_Method *methods;
    int nmethods;
    const char *basic;
    bool comparable;
} gx_TypeDesc;
typedef struct gx_Box {
    const gx_TypeDesc *t;
    gx_V v;
} gx_Box;

gx_V gx_func(int64_t fid, gx_Code code, int n, const gx_V *env);
gx_V gx_callv(gx_V f, int n, const gx_V *args);
gx_V gx_fnchk(gx_V f);
int64_t gx_fid_of(gx_V f);
gx_V gx_bound(int64_t fid, gx_V f, gx_V recv);
const gx_Method *gx_method(const gx_TypeDesc *t, const char *id);
gx_V gx_boxv(const gx_TypeDesc *t, gx_V v);
const gx_TypeDesc *gx_dyn_type(gx_V x);
gx_V gx_unboxed(gx_V x);
bool gx_ifeq(gx_V a, gx_V b);
void gx_ikey(gx_V a, gx_Buf *out);
const char *gx_implements_all(const gx_TypeDesc *t, int n, const char *const *ids);
bool gx_implements(gx_V x, int n, const char *const *ids);
const char *gx_missing_method(gx_V x, int n, const char *const *ids, const char *const *names);
bool gx_is_type(gx_V x, const gx_TypeDesc *t);
gx_V gx_icall(gx_V x, const char *id, int n, const gx_V *args);
gx_V gx_ibound(gx_V x, const char *id);
bool gx_eq_uncomparable(const char *name);
void gx_key_unhashable(const char *name);
bool gx_eq_basic(gx_V a, gx_V b);

/* Panics. */
typedef struct gx_Panic {
    gx_V value;
    bool recovered;
    struct gx_Panic *prev;
} gx_Panic;
typedef struct gx_Handler {
    jmp_buf jb;
    struct gx_Handler *prev;
    size_t source_depth;
} gx_Handler;
extern gx_Handler *gx_handler;
extern gx_Panic *gx_thrown;
/* Pushes h; evaluates nonzero when a panic arrived (gx_thrown holds it). */
#define GX_TRY(h) ((h).prev = gx_handler, (h).source_depth = gx_source_depth, gx_handler = &(h), setjmp((h).jb) != 0)
#define GX_END(h) (gx_handler = (h).prev)
_Noreturn void gx_raise(gx_Panic *p);
_Noreturn void gx_throw(gx_V v);
_Noreturn void gx_runtime_panic(const char *msg);
_Noreturn void gx_plain_panic(const char *msg);
_Noreturn void gx_panic_nil_deref(void);
_Noreturn void gx_assert_panic(gx_V x, const char *iface, const char *target, const char *missing);
gx_Panic *gx_new_panic(gx_V v);
void gx_chain_panic(gx_Panic *p, gx_Panic *earlier);
gx_V gx_runtime_error(const char *msg);
extern const gx_TypeDesc GX_RUNTIME_ERROR, GX_PLAIN_ERROR, GX_TYPE_ASSERTION_ERROR, GX_PANIC_NIL_ERROR, GX_STRING_TYPE;
size_t gx_idx(gx_V i, size_t len);
size_t gx_idxu(gx_V i, size_t len);

typedef struct gx_Deferred {
    gx_V f;
    gx_V *args;
    int n;
    int64_t fid;
    bool start;
    struct gx_Deferred *next;
} gx_Deferred;
gx_Deferred *gx_defer(gx_Deferred *list, gx_V f, int n, const gx_V *args, int64_t fid, bool start);
void gx_run_defers(gx_Deferred *list, gx_Panic *p);
gx_V gx_recover(int64_t fid);

/* Integers. */
static inline int64_t gx_w8(int64_t x) { return (int8_t)x; }
static inline int64_t gx_w16(int64_t x) { return (int16_t)x; }
static inline int64_t gx_w32(int64_t x) { return (int32_t)x; }
static inline int64_t gx_w64(int64_t x) { return x; }
static inline int64_t gx_wu8(int64_t x) { return x & 0xFF; }
static inline int64_t gx_wu16(int64_t x) { return x & 0xFFFF; }
static inline int64_t gx_wu32(int64_t x) { return x & 0xFFFFFFFFLL; }
static inline int64_t gx_wu64(int64_t x) { return x; }
static inline int64_t gx_wadd(int64_t a, int64_t b) { return (int64_t)((uint64_t)a + (uint64_t)b); }
static inline int64_t gx_wsub(int64_t a, int64_t b) { return (int64_t)((uint64_t)a - (uint64_t)b); }
static inline int64_t gx_wmul(int64_t a, int64_t b) { return (int64_t)((uint64_t)a * (uint64_t)b); }
static inline int64_t gx_wneg(int64_t a) { return (int64_t)(0 - (uint64_t)a); }
unsigned gx_count(gx_V n);
unsigned gx_countu(gx_V n);
_Noreturn void gx_div_zero(void);
gx_V gx_u64s(gx_V v);
int gx_cmpu(gx_V a, gx_V b);
int gx_scmp(gx_V a, gx_V b);

/* Bounds and UTF-8. */
void gx_check2(int64_t lo, int64_t hi, int64_t limit, const char *word, bool u);
void gx_check3(int64_t lo, int64_t hi, int64_t max, int64_t limit, const char *word, bool u);
int64_t gx_utf8_decode(const uint8_t *s, size_t n, size_t i, size_t *width);
void gx_utf8_encode(int64_t r, gx_Buf *out);

/* Maps. */
typedef struct gx_Entry {
    gx_V k, v;
    bool live;
    uint8_t *key;
    size_t keyn;
} gx_Entry;
typedef struct gx_Map {
    gx_Entry **entries;
    size_t n, cap;
    size_t *slots; /* open-addressing index: entry index + 1, 0 empty */
    size_t nslots, live;
    gx_KeyFn key_of;
} gx_Map;
typedef struct gx_MapIter {
    gx_Entry **entries;
    size_t n, i;
    gx_V k, v;
} gx_MapIter;
gx_Entry *gx_map_find(gx_Map *m, const uint8_t *key, size_t n);
void gx_map_insert(gx_Map *m, gx_V k, gx_V v, uint8_t *key, size_t n);
bool gx_map_remove(gx_Map *m, const uint8_t *key, size_t n);
void gx_map_reset(gx_Map *m);

/* Tasks and frames. Executable source ownership is deliberately process-static. */
typedef struct gx_Sched gx_Sched;
typedef struct gx_Context gx_Context;
typedef struct gx_HostPending gx_HostPending;
typedef struct gx_Mailbox gx_Mailbox;
extern size_t gx_source_depth;
void gx_source_enter(void);
void gx_source_leave(void);
bool gx_entry_reserve(void);
void gx_entry_release(void);
bool gx_owner_current(gx_Sched *s);
void gx_owner_check(gx_Sched *s);
_Noreturn void gx_host_fault(const char *message);
void gx_retire(gx_Sched *s);

typedef struct gx_Task gx_Task;
typedef struct gx_Frame gx_Frame;
typedef void (*gx_Step)(gx_Task *t, gx_Frame *f);
typedef int (*gx_Results)(gx_Frame *f, gx_V *out);
struct gx_Frame {
    gx_V *l;
    int nl;
    uint32_t pc;
    gx_Deferred *defers;
    gx_Frame *parent;
    gx_Panic *panicking;
    gx_Step step;
    gx_Results results;
    void (*completion)(gx_Task *, gx_Frame *, gx_Panic *);
    gx_Frame *a, *b;
    void (*prim)(gx_Task *t, void *arg);
    void *prim_arg;
};
struct gx_Task {
    int id;
    gx_Sched *owner;
    bool queued;
    gx_Frame *frame;
    gx_V *rv;
    int nrv;
    bool blocked, done;
    gx_Panic *resume_panic;
    void (*cleanup)(void *arg);
    void *cleanup_arg;
    gx_Panic *cur_panic;
    int64_t defer_target;
    gx_Task *next_all;
};
typedef struct gx_Timer {
    int64_t at, seq;
    gx_Task *task;
    void (*f)(gx_V);
    gx_V arg;
} gx_Timer;
typedef struct gx_Unawaited {
    void *key;
    gx_Panic *panic;
    struct gx_Unawaited *next;
} gx_Unawaited;
struct gx_Sched {
    gx_Unawaited *unawaited, *unawaited_tail;
    gx_Task **runq;
    size_t qhead, qlen, qcap;
    gx_Task *cur;
    int next_id;
    int64_t rng, clock, seq;
    gx_Timer *timers;
    size_t ntimers, tcap;
    bool harness, host, retiring, retired;
    pthread_t thread;
    struct timespec epoch;
    gx_Task *all;
    gx_Context *contexts;
    gx_HostPending *pending;
    gx_Mailbox *mailbox;
    uint64_t generation, operation;
    unsigned context_cancel_depth;
    jmp_buf *escape;
    const char *fatal, *fault;
    gx_Panic *panic;
    void (*retire)(gx_Sched *s);
    void (*library_poll)(void);
    void (*native_cleanup)(void);
    /* Library instance identity: native keys created under one domain stay
     * usable by later calls of the instance. Zero means this owner's
     * generation. keep_native retains them past this owner's cleanup. */
    uint64_t domain;
    bool keep_native;
};
extern gx_Sched *gx_sched;
static inline uint64_t gx_domain(void) { return gx_sched->domain ? gx_sched->domain : gx_sched->generation; }
/* Releases native resources of a retired library instance domain. */
extern void (*gx_native_domain_release)(uint64_t domain);
gx_Frame *gx_new_frame(int nl, gx_Step step, gx_Results results);
static inline gx_V gx_vframe(gx_Frame *f) { gx_V v = {0}; v.t = GX_FRAME; v.u.p = f; return v; }
static inline gx_Frame *gx_frameof(gx_V v) { return (gx_Frame *)v.u.p; }
gx_Task *gx_new_task(int id, gx_Frame *f);
gx_Task *gx_cur_task(void);
void gx_own_task(gx_Sched *s, gx_Task *t);
void gx_set_rv(gx_Task *t, int n, const gx_V *vs);
static inline gx_V gx_rv(gx_Task *t, int k) { return t->rv[k]; }
gx_Sched *gx_new_sched(gx_Task *main, bool harness);
int64_t gx_seed(void);

/* Program. */
extern int gx_blocked_signal;
_Noreturn void gx_report_panic(gx_Panic *p);
const char *gx_panic_text(gx_Panic *p);
gx_Buf gx_panic_report(gx_Panic *p);
void gx_format_panic_value(gx_V v, gx_Buf *out);
void gx_stderr(const void *b, size_t n);
void gx_run_large(void (*body)(void));
_Noreturn void gx_program_main(void (*init)(void), void (*entry)(void));
gx_V gx_zero_nil(void);
gx_V gx_zero_int(void);
gx_V gx_zero_bool(void);
gx_V gx_zero_string(void);
gx_V gx_zero_slice(void);
gx_V gx_zero_byte_slice(void);
typedef gx_V (*gx_ZeroFn)(void);
gx_V gx_zero_append_growth(gx_V result, gx_V previous, gx_ZeroFn zero);
typedef gx_V (*gx_CloneFn)(gx_V);

/* Channel and synchronization objects. */
typedef struct gx_WaitQ gx_WaitQ;
typedef struct gx_Waiter {
    gx_Task *task;
    gx_V val;
    bool *sel;
    int idx;
    struct gx_Waiter *next;
    gx_WaitQ *queue;
} gx_Waiter;
struct gx_WaitQ {
    gx_Waiter *head, *tail;
};
typedef struct gx_Chan {
    gx_V *buf;
    size_t head, len, size;
    bool closed;
    gx_WaitQ recvq, sendq;
    gx_ZeroFn zero;
} gx_Chan;
enum { GX_KIND_MUTEX = 1, GX_KIND_WAITGROUP = 2 };
typedef struct gx_Mutex {
    int kind;
    bool locked;
    gx_Task **waiters;
    size_t n, cap;
} gx_Mutex;
typedef struct gx_WaitGroup {
    int kind;
    int64_t n;
    gx_Task **waiters;
    size_t nw, cap;
} gx_WaitGroup;
struct gx_Context {
    gx_V done, err;
    gx_V *children;
    size_t n, cap;
    gx_Sched *owner;
    gx_Context *parent, *next_owner;
    int64_t deadline;
    bool has_deadline;
};
gx_V gx_new_mutex(void);
gx_V gx_new_waitgroup(void);
gx_V gx_opaque_clone(gx_V x);
void gx_opaque_set(gx_V d, gx_V s);

void gx_lib_crypto_random(gx_Task *t,gx_V a0);
void gx_lib_crypto_sha256(gx_Task *t,gx_V a0);
void gx_lib_crypto_hmac_sha256(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_hmac_sha256_verify(gx_Task *t,gx_V a0,gx_V a1,gx_V a2);
void gx_lib_crypto_hkdf_sha256(gx_Task *t,gx_V a0,gx_V a1,gx_V a2,gx_V a3);
void gx_lib_crypto_aes256_gcm_encrypt(gx_Task *t,gx_V a0,gx_V a1,gx_V a2,gx_V a3);
void gx_lib_crypto_aes256_gcm_decrypt(gx_Task *t,gx_V a0,gx_V a1,gx_V a2,gx_V a3);
void gx_lib_crypto_generate_rsa2048(gx_Task *t);
void gx_lib_crypto_generate_p256(gx_Task *t);
void gx_lib_crypto_generate_rsa4096(gx_Task *t);
void gx_lib_crypto_generate_p384(gx_Task *t);
void gx_lib_crypto_generate_p521(gx_Task *t);
void gx_lib_crypto_import_pem(gx_Task *t,gx_V a0);
void gx_lib_crypto_public_pem(gx_Task *t,gx_V a0);
void gx_lib_crypto_private_pem(gx_Task *t,gx_V a0);
void gx_lib_crypto_public_jwk(gx_Task *t,gx_V a0);
void gx_lib_crypto_ecdh(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_rsa_oaep_encrypt(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_rsa_oaep_decrypt(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_rs256_sign(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_rs256_verify(gx_Task *t,gx_V a0,gx_V a1,gx_V a2);
void gx_lib_crypto_es256_sign(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_es256_verify(gx_Task *t,gx_V a0,gx_V a1,gx_V a2);
void gx_lib_crypto_rs384_sign(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_rs384_verify(gx_Task *t,gx_V a0,gx_V a1,gx_V a2);
void gx_lib_crypto_rs512_sign(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_rs512_verify(gx_Task *t,gx_V a0,gx_V a1,gx_V a2);
void gx_lib_crypto_es384_sign(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_es384_verify(gx_Task *t,gx_V a0,gx_V a1,gx_V a2);
void gx_lib_crypto_es512_sign(gx_Task *t,gx_V a0,gx_V a1);
void gx_lib_crypto_es512_verify(gx_Task *t,gx_V a0,gx_V a1,gx_V a2);
void gx_lib_crypto_close(gx_Task *t,gx_V key);
gx_V gx_lib_encoding_base64_encode(gx_V v);
gx_V gx_lib_encoding_base64_decode(gx_V v);
gx_V gx_lib_encoding_base64_url_encode(gx_V v);
gx_V gx_lib_encoding_base64_url_decode(gx_V v);
gx_V gx_lib_clock_unix(void);
gx_V gx_lib_clock_unix_nano(void);
gx_V gx_lib_log_enabled(gx_V level);
void gx_lib_log_emit(gx_V level, gx_V unix_nano, gx_V message, gx_V attrs, gx_V text);
gx_V gx_lib_os_read_file(gx_V name);
gx_V gx_lib_os_write_file(gx_V name, gx_V data, gx_V perm);
int64_t gx_os_status(int e);
char *gx_os_path(gx_V name, int64_t *status);
bool gx_log_enabled(int64_t level);
void gx_log_emit(int64_t level, int64_t unix_nano, gx_V message, gx_V attrs, gx_V text);
void gx_lib_http_do(gx_Task *,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V);
void gx_http_do(gx_Task *,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V);
/* lib/http.DoWith transport options, owned by malloc; shared with exchanges. */
typedef struct gx_http_transport { char *client_cert,*client_key,*root_cas,*unix_socket; size_t client_cert_len,client_key_len,root_cas_len; bool insecure; } gx_http_transport;
int gx_http_options(gx_http_transport *t, gx_V options);
void gx_http_options_free(gx_http_transport *t);
bool gx_http_valid_url(gx_V url);
bool gx_http_header_name(gx_V name);
void gx_lib_http_open(gx_Task *,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V);
void gx_lib_http_exchange_write(gx_Task *,gx_V,gx_V);
void gx_lib_http_exchange_response(gx_Task *,gx_V);
void gx_lib_http_exchange_read(gx_Task *,gx_V,gx_V);
void gx_lib_http_exchange_close(gx_Task *,gx_V);
void gx_http_exchange_write(gx_Task *,gx_V,gx_V);
void gx_http_exchange_response(gx_Task *,gx_V);
void gx_http_exchange_read(gx_Task *,gx_V,gx_V);
/* Native resources besides keys, such as HTTP exchanges, released with the owner's keys. */
extern void (*gx_native_exchanges_release)(uint64_t domain);
void gx_native_resources_track(void);
void gx_lib_http_do_with(gx_Task *,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V,gx_V);
void gx_lib_callback_request(gx_Task *,gx_V,gx_V,gx_V);

/* Contract functions: one implementation file each. */
#define GX_INT_OPS(op)                                                                              \
    gx_V gx_##op##_i8(gx_V a, gx_V b); gx_V gx_##op##_i16(gx_V a, gx_V b);                          \
    gx_V gx_##op##_i32(gx_V a, gx_V b); gx_V gx_##op##_i64(gx_V a, gx_V b);                         \
    gx_V gx_##op##_u8(gx_V a, gx_V b); gx_V gx_##op##_u16(gx_V a, gx_V b);                          \
    gx_V gx_##op##_u32(gx_V a, gx_V b); gx_V gx_##op##_u64(gx_V a, gx_V b);
GX_INT_OPS(add) GX_INT_OPS(sub) GX_INT_OPS(mul) GX_INT_OPS(div) GX_INT_OPS(rem)
GX_INT_OPS(and) GX_INT_OPS(or) GX_INT_OPS(xor) GX_INT_OPS(andnot)
#define GX_INT_SHIFTS(op)                                                                           \
    gx_V gx_##op##_i8(gx_V a, unsigned n); gx_V gx_##op##_i16(gx_V a, unsigned n);                  \
    gx_V gx_##op##_i32(gx_V a, unsigned n); gx_V gx_##op##_i64(gx_V a, unsigned n);                 \
    gx_V gx_##op##_u8(gx_V a, unsigned n); gx_V gx_##op##_u16(gx_V a, unsigned n);                  \
    gx_V gx_##op##_u32(gx_V a, unsigned n); gx_V gx_##op##_u64(gx_V a, unsigned n);
GX_INT_SHIFTS(shl) GX_INT_SHIFTS(shr)
#define GX_INT_UNARY(op)                                                                            \
    gx_V gx_##op##_i8(gx_V a); gx_V gx_##op##_i16(gx_V a); gx_V gx_##op##_i32(gx_V a);              \
    gx_V gx_##op##_i64(gx_V a); gx_V gx_##op##_u8(gx_V a); gx_V gx_##op##_u16(gx_V a);              \
    gx_V gx_##op##_u32(gx_V a); gx_V gx_##op##_u64(gx_V a);
GX_INT_UNARY(neg) GX_INT_UNARY(not) GX_INT_UNARY(to)
gx_V gx_compare_int(gx_V a, gx_V b);
gx_V gx_compare_uint(gx_V a, gx_V b);

gx_V gx_sindex(gx_V s, gx_V i);
gx_V gx_sindexu(gx_V s, gx_V i);
gx_V gx_sslice(gx_V s, gx_V lo, gx_V hi, bool u);
gx_V gx_concat(gx_V a, gx_V b);
gx_V gx_scompare(gx_V a, gx_V b);
gx_V gx_decode_rune(gx_V s, gx_V i);
gx_V gx_from_bytes(gx_V b);
gx_V gx_to_bytes(gx_V s);
gx_V gx_from_runes(gx_V r);
gx_V gx_to_runes(gx_V s);
gx_V gx_from_rune(gx_V r);
gx_V gx_from_rune_u(gx_V r);

gx_V gx_make_slice(gx_V len, gx_V cap, gx_ZeroFn zero);
gx_V gx_make_byte_slice(gx_V len, gx_V cap);
gx_V gx_sget(gx_V s, gx_V i);
gx_V gx_sgetu(gx_V s, gx_V i);
void gx_sset(gx_V s, gx_V i, gx_V v);
void gx_ssetu(gx_V s, gx_V i, gx_V v);
gx_V gx_reslice(gx_V s, gx_V lo, gx_V hi, gx_V max, bool u);
gx_V gx_slice_array(gx_V a, size_t n, gx_V lo, gx_V hi, gx_V max, bool u);
int64_t gx_grow_cap(int64_t old, int64_t required);
gx_V gx_append_bytes(gx_V s, const uint8_t *p, uint32_t n);
gx_V gx_append(gx_V s, int n, const gx_V *vs, gx_CloneFn clone);
gx_V gx_append_slice(gx_V s, gx_V t, gx_CloneFn clone);
gx_V gx_append_string(gx_V s, gx_V str);
gx_V gx_copy(gx_V dst, gx_V src, gx_CloneFn clone);
gx_V gx_copy_string(gx_V dst, gx_V src);
void gx_clear_slice(gx_V s, gx_ZeroFn zero);
gx_V gx_slice_to_array(gx_V s, size_t n, gx_CloneFn clone);

gx_V gx_make_map(gx_KeyFn key_of);
gx_V gx_map_get(gx_V m, gx_V k, gx_KeyFn key_of, gx_ZeroFn zero);
void gx_map_set(gx_V m, gx_V k, gx_V v);
void gx_map_delete(gx_V m, gx_V k, gx_KeyFn key_of);
gx_V gx_map_len(gx_V m);
void gx_map_clear(gx_V m);
gx_V gx_map_iter(gx_V m);
gx_V gx_map_next(gx_V it);
gx_V gx_iter_key(gx_V it);
gx_V gx_iter_val(gx_V it);
gx_V gx_map_keys(gx_V m);

gx_V gx_print_string(gx_V v, bool unsigned_);
void gx_print(int n, const gx_V *args, bool newline);

gx_V gx_std_errors_new(gx_V text);
gx_V gx_std_errors_is(gx_V err, gx_V target);
gx_V gx_std_errors_unwrap(gx_V err);

void gx_ready(gx_Task *t);
void gx_block(gx_Task *t);
size_t gx_choose(size_t n);
int64_t gx_now(void);
int64_t gx_deadline(int64_t now, int64_t duration);
void gx_add_timer_at(int64_t at, gx_Task *task, void (*f)(gx_V), gx_V arg);
void gx_remove_context_timer(gx_Context *c);
void gx_add_timer(int64_t d, gx_Task *task, void (*f)(gx_V), gx_V arg);
_Noreturn void gx_fatal(const char *msg);
void gx_call(gx_Task *t, gx_V child);
void gx_ret(gx_Task *t, gx_Frame *f);
gx_V gx_sync_frame(gx_V f, int n, const gx_V *args, int nres);
gx_V gx_adapt(gx_V f, int nres);
gx_V gx_adapt_slice(gx_V s, int nres);
void gx_spawn(gx_V frame);
void gx_spawn_call(gx_V f, int n, const gx_V *args);
/* Returns 0 success, 1 overlapping entry, 3 host fault.
 * Source fatal/panic terminates the executable with exit2 after cleanup.
 * This is an executable runtime entry, not a library/SDK ABI. */
int gx_run_main_host(void (*init)(void), gx_V (*entry)(void));
/* Owned native library reports; finish runs on the collector owner before retirement. */
int gx_run_library_host(void (*init)(void), gx_V (*entry)(void),
    void (*finish)(gx_Task *), void (*poll)(void), uint8_t **report, size_t *length);
_Noreturn void gx_host_main(void (*init)(void), gx_V (*entry)(void));
_Noreturn void gx_run_main(void (*init)(void), gx_V (*entry)(void));
void gx_yield_task(gx_Task *t);
int gx_run_isolated(void (*prim)(gx_Task *, void *), void *arg, gx_V *out);
void gx_reset_scheduler(void);

void gx_recv_done(gx_Waiter *w, gx_V v, bool ok);
void gx_send_done(gx_Waiter *w, bool closed);
gx_Waiter *gx_dequeue(gx_WaitQ *q);
bool gx_has_live(gx_WaitQ *q);
void gx_enqueue(gx_WaitQ *q, gx_Waiter *w);
void gx_detach_waiter(void *arg);
bool gx_try_recv(gx_Chan *c, gx_V *v, bool *ok);
gx_V gx_make_chan(gx_V size, gx_ZeroFn zero);
void gx_chan_send(gx_Task *t, gx_V ch, gx_V v);
void gx_chan_recv(gx_Task *t, gx_V ch);
void gx_chan_close(gx_V ch);
gx_V gx_chan_len(gx_V ch);
gx_V gx_chan_cap(gx_V ch);
typedef struct gx_Case {
    gx_V ch;
    bool send;
    gx_V v;
} gx_Case;
void gx_select(gx_Task *t, bool has_default, int n, const gx_Case *cases);

void gx_std_sync_mutex_lock(gx_Task *t, gx_V m);
void gx_std_sync_mutex_unlock(gx_V m);
void gx_std_sync_waitgroup_add(gx_V wg, gx_V d);
void gx_std_sync_waitgroup_done(gx_V wg);
void gx_std_sync_waitgroup_wait(gx_Task *t, gx_V wg);
void gx_std_runtime_gosched(gx_Task *t);
void gx_std_time_sleep(gx_Task *t, gx_V d);
gx_V gx_context_canceled(void);
gx_V gx_context_deadline_exceeded(void);
gx_V gx_background(void);
void gx_cancel_ctx(gx_V c, gx_V err);
void gx_observe_context(gx_V c);
gx_V gx_new_child(gx_V parent);
gx_V gx_cancel_code(gx_V *env, gx_V *args, int n);
gx_V gx_std_context_context_err(gx_V c);
gx_V gx_std_context_background(void);
gx_V gx_std_context_with_cancel(gx_V parent);
gx_V gx_std_context_with_timeout(gx_V parent, gx_V d);
gx_V gx_std_context_context_done(gx_V c);
gx_V gx_std_context_canceled(void);
gx_V gx_std_context_deadline_exceeded(void);
typedef struct gx_FutureWaiter {
    gx_Task *task;
    struct gx_FutureWaiter *next;
} gx_FutureWaiter;
typedef struct gx_Future {
    bool done, observed;
    gx_V results;
    gx_Panic *panic;
    gx_FutureWaiter *head, *tail;
} gx_Future;
gx_V gx_lib_task_start(gx_V function);
void gx_lib_task_await(gx_Task *t, gx_V future);
gx_Panic *gx_copy_future_panic(gx_Panic *p);
void gx_lib_task_all(gx_Task *t, gx_V fns);

/* Native tokens retain only a refcounted malloc mailbox and numeric identity.
 * Copies must explicitly retain/release. Workers must not pass gx_V/source pointers. */
typedef struct gx_HostToken { gx_Mailbox *mailbox; uint64_t generation, operation; int task; } gx_HostToken;
typedef struct gx_HostBoundary { gx_Context *context; bool has_deadline; int64_t deadline; gx_V deadline_error; } gx_HostBoundary;
typedef const char *(*gx_HostAction)(void *native);
typedef const char *(*gx_HostDecode)(gx_Task *task, const uint8_t *bytes, size_t length, gx_V cancellation, gx_V roots);
gx_HostBoundary gx_host_boundary(gx_V context, int64_t timeout);
gx_HostToken gx_host_register(gx_Task *task, gx_HostBoundary boundary, gx_V roots,
    gx_HostDecode decode, gx_HostAction cancel, gx_HostAction cleanup, void *native);
/* False means already canceled/expired; publish+ACK without native submission. */
bool gx_host_should_submit(gx_HostToken token);
void gx_host_token_retain(gx_HostToken token);
void gx_host_token_release(gx_HostToken token);
/* Publication copies bytes/fault into malloc ownership. Never a source descriptor. */
bool gx_host_publish(gx_HostToken token, const void *bytes, size_t length, const char *fault);
/* Allocation-free terminal fault fallback uses registration-owned bounded bytes. */
bool gx_host_publish_fault(gx_HostToken token,const char *fault);
bool gx_host_ack(gx_HostToken token);
void gx_library_wake(void);
void gx_host_poll(void);
void gx_host_wait(int64_t deadline, bool timed);
void gx_host_context_changed(void);
size_t gx_host_live_count(gx_HostToken token);

#endif
