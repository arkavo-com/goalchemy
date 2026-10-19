//! Go panics as Rust unwinding. The payload is the handle of a heap Panic
//! object; between a throw and its catch no safepoint runs, and catchers
//! root the panic while deferred calls run.
use super::*;
#[cfg(not(goalchemy_nostd))]
use std::panic::{catch_unwind, resume_unwind, AssertUnwindSafe};

pub struct PanicObj {
    pub value: V,
    pub recovered: bool,
    pub prev: V,
}

/// The unwinding payload of a Go panic.
pub struct GoPanicPayload(pub H);

pub fn new_panic(value: V) -> V {
    V::Obj(alloc(Obj::Panic(PanicObj { value, recovered: false, prev: V::Nil })))
}

/// Raises an existing panic object.
pub fn raise(p: V) -> ! {
    #[cfg(not(goalchemy_nostd))]
    resume_unwind(Box::new(GoPanicPayload(p.h())));
    #[cfg(goalchemy_nostd)]
    panic!("goalchemy panic: {}", String::from_utf8_lossy(&format_panic_value(&panic_value(&p))))
}

/// panic(v) with an interface value v.
pub fn throw(v: V) -> ! {
    let v = if v.is_nil() { boxv(&PANIC_NIL_ERROR, V::Nil) } else { v };
    raise(new_panic(v))
}

/// Runs f, returning the Go panic it raised.
#[cfg(not(goalchemy_nostd))]
pub fn catch<R>(f: impl FnOnce() -> R) -> Result<R, V> {
    match catch_unwind(AssertUnwindSafe(f)) {
        Ok(r) => Ok(r),
        Err(e) => match e.downcast::<GoPanicPayload>() {
            Ok(p) => Err(V::Obj(p.0)),
            Err(e) => resume_unwind(e),
        },
    }
}

fn with_panic<R>(p: &V, f: impl FnOnce(&mut PanicObj) -> R) -> R {
    with(p.h(), |o| match o {
        Obj::Panic(x) => f(x),
        _ => fault("panic expected"),
    })
}

pub fn panic_value(p: &V) -> V {
    with_panic(p, |x| x.value.clone())
}

pub fn panic_recovered(p: &V) -> bool {
    with_panic(p, |x| x.recovered)
}

pub fn panic_prev(p: &V) -> V {
    with_panic(p, |x| x.prev.clone())
}

/// Chains p after an earlier panic it replaced.
pub fn chain_panic(p: &V, earlier: &V) {
    if earlier.is_nil() || veq(p, earlier) {
        return;
    }
    with_panic(p, |x| {
        if x.prev.is_nil() {
            x.prev = earlier.clone();
        }
    })
}

fn runtime_error_text(_: &[V], a: Vec<V>) -> V {
    let mut b = b"runtime error: ".to_vec();
    b.extend_from_slice(&a[0].bytes());
    s(&b)
}

fn plain_error_text(_: &[V], a: Vec<V>) -> V {
    a[0].clone()
}

fn no_value(_: &[V], _: Vec<V>) -> V {
    V::Nil
}

fn panic_nil_text(_: &[V], _: Vec<V>) -> V {
    s(b"runtime error: panic called with nil argument")
}

pub static RUNTIME_ERROR: TypeDesc = TypeDesc {
    id: 0x7fff_0001,
    name: "runtime.Error",
    kind: "runtime_error",
    eq: eq_basic,
    key: key_basic,
    methods: &[("Error", -1, runtime_error_text), ("RuntimeError", -1, no_value)],
    basic: "",
    comparable: true,
};

pub static PLAIN_ERROR: TypeDesc = TypeDesc {
    id: 0x7fff_0002,
    name: "runtime.plainError",
    kind: "runtime_error",
    eq: eq_basic,
    key: key_basic,
    methods: &[("Error", -1, plain_error_text), ("RuntimeError", -1, no_value)],
    basic: "",
    comparable: true,
};

pub static TYPE_ASSERTION_ERROR: TypeDesc = TypeDesc {
    id: 0x7fff_0003,
    name: "*runtime.TypeAssertionError",
    kind: "runtime_error",
    eq: eq_basic,
    key: key_basic,
    methods: &[("Error", -1, plain_error_text), ("RuntimeError", -1, no_value)],
    basic: "",
    comparable: true,
};

pub static PANIC_NIL_ERROR: TypeDesc = TypeDesc {
    id: 0x7fff_0004,
    name: "*runtime.PanicNilError",
    kind: "runtime_error",
    eq: eq_basic,
    key: key_basic,
    methods: &[("Error", -1, panic_nil_text), ("RuntimeError", -1, no_value)],
    basic: "",
    comparable: true,
};

pub static STRING_TYPE: TypeDesc = TypeDesc {
    id: 0x7fff_0005,
    name: "string",
    kind: "string",
    eq: eq_basic,
    key: key_basic,
    methods: &[],
    basic: "string",
    comparable: true,
};

pub fn runtime_error(msg: &str) -> V {
    boxv(&RUNTIME_ERROR, s(msg.as_bytes()))
}

pub fn runtime_panic(msg: &str) -> ! {
    throw(runtime_error(msg))
}

pub fn plain_panic(msg: &[u8]) -> ! {
    throw(boxv(&PLAIN_ERROR, s(msg)))
}

pub fn panic_nil_deref() -> ! {
    runtime_panic("invalid memory address or nil pointer dereference")
}

pub fn nilchk(v: V) -> V {
    if v.is_nil() {
        panic_nil_deref();
    }
    v
}

/// Checks a signed index against a length.
#[inline]
pub fn idx(i: &V, len: usize) -> usize {
    let i = i.i();
    if i < 0 {
        runtime_panic(&format!("index out of range [{}]", i));
    }
    if i as u64 >= len as u64 {
        runtime_panic(&format!("index out of range [{}] with length {}", i, len));
    }
    i as usize
}

/// Checks an unsigned 64-bit index against a length.
#[inline]
pub fn idxu(i: &V, len: usize) -> usize {
    let u = i.i() as u64;
    if u >= len as u64 {
        runtime_panic(&format!("index out of range [{}] with length {}", u, len));
    }
    u as usize
}

pub fn assert_panic(x: &V, iface: &str, target: &str, missing: Option<&str>) -> ! {
    let msg = match dyn_type(x) {
        None => format!("interface conversion: {} is nil, not {}", iface, target),
        Some(t) => match missing {
            Some(m) => format!("interface conversion: {} is not {}: missing method {}", t.name, target, m),
            None => format!("interface conversion: {} is {}, not {}", iface, t.name, target),
        },
    };
    throw(boxv(&TYPE_ASSERTION_ERROR, s(msg.as_bytes())))
}

/// Runs a sequential function's deferred calls after its body finished,
/// normally or by panic p (Nil when none), and re-raises a panic that is
/// still active afterwards.
#[cfg(not(goalchemy_nostd))]
pub fn run_defers(fr: &Fr, p: V) {
    let mut panicking = p;
    let _root = temp_root(&[panicking.clone()]);
    while let Some(d) = fr.pop_defer() {
        let t = cur_task();
        let saved_panic = t.cur_panic.replace(panicking.clone());
        let saved_target = t.defer_target.replace(d.fid);
        let _keep = temp_root(&[d.f.clone(), panicking.clone()]);
        let r = catch(|| {
            callv(&d.f, d.args);
        });
        t.cur_panic.replace(saved_panic);
        t.defer_target.set(saved_target);
        match r {
            Err(np) => {
                chain_panic(&np, &panicking);
                panicking = np;
            }
            Ok(()) => {
                if !panicking.is_nil() && panic_recovered(&panicking) {
                    panicking = V::Nil;
                }
            }
        }
    }
    if !panicking.is_nil() {
        raise(panicking);
    }
}

#[cfg(not(goalchemy_nostd))]
pub fn recover(fid: i64) -> V {
    let t = cur_task();
    let p = t.cur_panic.borrow().clone();
    if p.is_nil() || panic_recovered(&p) || t.defer_target.get() != fid {
        return V::Nil;
    }
    with_panic(&p, |x| {
        x.recovered = true;
        x.value.clone()
    })
}

// no_std uses the embedding application's panic handler; no source recovery.
#[cfg(goalchemy_nostd)]
pub fn catch<R>(f: impl FnOnce() -> R) -> Result<R, V> { Ok(f()) }
#[cfg(goalchemy_nostd)]
pub fn run_defers(fr: &Fr, _: V) {
    while let Some(d) = fr.pop_defer() {
        let _root = temp_root(&[d.f.clone()]);
        callv(&d.f, d.args);
    }
}
