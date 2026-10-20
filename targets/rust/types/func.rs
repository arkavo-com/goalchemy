//! Function values, dynamic types, and interface boxes.
use super::*;

/// Generated and runtime function bodies: environment and arguments in,
/// result out (Nil for none, a Tuple for several).
pub type Code = fn(&[V], Vec<V>) -> V;

pub struct Func {
    pub fid: i64,
    pub code: Code,
    pub env: Vec<V>,
}

/// A function value; fid is the identity recover compares against.
pub fn func(fid: i64, code: Code, env: Vec<V>) -> V {
    V::Obj(alloc(Obj::Func(Func { fid, code, env })))
}

fn parts(f: &V) -> (Code, Vec<V>) {
    if f.is_nil() {
        panic_nil_deref();
    }
    with(f.h(), |o| match o {
        Obj::Func(x) => (x.code, x.env.clone()),
        _ => fault("call of non-function"),
    })
}

/// Calls a function value; nil panics.
pub fn callv(f: &V, args: Vec<V>) -> V {
    let (code, env) = parts(f);
    code(&env, args)
}

pub fn fnchk(f: V) -> V {
    if f.is_nil() {
        panic_nil_deref();
    }
    f
}

pub fn fid_of(f: &V) -> i64 {
    if f.is_nil() {
        return -1;
    }
    with(f.h(), |o| match o {
        Obj::Func(x) => x.fid,
        _ => -1,
    })
}

fn bound_code(env: &[V], args: Vec<V>) -> V {
    let mut all = Vec::with_capacity(args.len() + 1);
    all.push(env[1].clone());
    all.extend(args);
    callv(&env[0], all)
}

/// A method value: f with recv bound as its first argument.
pub fn bound(fid: i64, f: V, recv: V) -> V {
    func(fid, bound_code, vec![f, recv])
}

pub struct TypeDesc {
    pub id: u32,
    pub name: &'static str,
    pub kind: &'static str,
    pub eq: fn(&V, &V) -> bool,
    pub key: fn(&V) -> Key,
    /// Method ID, recover identity, and body taking the receiver first.
    pub methods: &'static [(&'static str, i64, Code)],
    /// "int", "uint", "bool", or "string" for basic underlying types.
    pub basic: &'static str,
    pub comparable: bool,
}

impl TypeDesc {
    pub fn method(&self, id: &str) -> Option<(i64, Code)> {
        self.methods.iter().find(|m| m.0 == id).map(|m| (m.1, m.2))
    }
}

pub fn boxv(t: &'static TypeDesc, v: V) -> V {
    V::Obj(alloc(Obj::Box(t, v)))
}

/// The dynamic type and value of a non-nil interface.
pub fn unbox(x: &V) -> Option<(&'static TypeDesc, V)> {
    if x.is_nil() {
        return None;
    }
    with(x.h(), |o| match o {
        Obj::Box(t, v) => Some((*t, v.clone())),
        _ => fault("interface expected"),
    })
}

pub fn dyn_type(x: &V) -> Option<&'static TypeDesc> {
    unbox(x).map(|(t, _)| t)
}

pub fn ifeq(a: &V, b: &V) -> bool {
    match (unbox(a), unbox(b)) {
        (None, None) => true,
        (Some((ta, va)), Some((tb, vb))) => core::ptr::eq(ta, tb) && (ta.eq)(&va, &vb),
        _ => false,
    }
}

pub fn ikey(a: &V) -> Key {
    match unbox(a) {
        None => Key::Nil,
        Some((t, v)) => Key::Iface(t.id, Rc::new((t.key)(&v))),
    }
}

pub fn implements_all(t: &TypeDesc, ids: &[&'static str]) -> Option<&'static str> {
    ids.iter().copied().find(|id| t.method(id).is_none())
}

/// Calls a method of an interface value with the receiver first.
pub fn icall(x: &V, id: &str, args: Vec<V>) -> V {
    let Some((t, v)) = unbox(x) else { panic_nil_deref() };
    let (_, code) = t.method(id).unwrap_or_else(|| fault("missing method"));
    let mut all = Vec::with_capacity(args.len() + 1);
    all.push(v);
    all.extend(args);
    code(&[], all)
}

/// The method value x.M of an interface value.
pub fn ibound(x: &V, id: &'static str) -> V {
    let Some((t, v)) = unbox(x) else { panic_nil_deref() };
    let (fid, code) = t.method(id).unwrap_or_else(|| fault("missing method"));
    bound(fid, func(fid, code, Vec::new()), v)
}

/// Equality of uncomparable values: always a run-time panic.
pub fn eq_uncomparable(name: &str) -> bool {
    throw(runtime_error(&format!("comparing uncomparable type {}", name)))
}

pub fn key_unhashable(name: &str) -> Key {
    throw(runtime_error(&format!("hash of unhashable type {}", name)))
}

pub fn eq_basic(a: &V, b: &V) -> bool {
    veq(a, b)
}

pub fn key_basic(a: &V) -> Key {
    vkey(a)
}
