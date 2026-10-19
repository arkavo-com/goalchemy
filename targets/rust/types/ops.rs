//! Storage and value operations used by generated code.
use super::*;

/// A new cell holding v: the target of a pointer to a scalar variable.
pub fn cellv(v: V) -> V {
    V::Ptr(alloc(Obj::Vals(vec![v])), 0)
}

/// *p for a pointer to a scalar.
#[inline]
pub fn pget(p: &V) -> V {
    match p {
        V::Ptr(h, i) => slot(*h, *i as usize),
        V::Nil => panic_nil_deref(),
        _ => fault("pointer expected"),
    }
}

#[inline]
pub fn pset(p: &V, x: V) {
    match p {
        V::Ptr(h, i) => set_slot(*h, *i as usize, x),
        V::Nil => panic_nil_deref(),
        _ => fault("pointer expected"),
    }
}

/// Field k of a struct.
#[inline]
pub fn fld(x: &V, k: usize) -> V {
    slot(x.h(), k)
}

#[inline]
pub fn fset(x: &V, k: usize, v: V) {
    set_slot(x.h(), k, v)
}

/// &x.f for a scalar field.
pub fn fptr(x: &V, k: usize) -> V {
    let h = x.h();
    if h == 0 {
        panic_nil_deref();
    }
    V::Ptr(h, k as u32)
}

/// Element i of an array of length n.
#[inline]
pub fn aget(x: &V, i: &V, n: usize) -> V {
    slot(x.h(), idx(i, n))
}

#[inline]
pub fn agetu(x: &V, i: &V, n: usize) -> V {
    slot(x.h(), idxu(i, n))
}

#[inline]
pub fn aset(x: &V, i: &V, n: usize, v: V) {
    set_slot(x.h(), idx(i, n), v)
}

#[inline]
pub fn asetu(x: &V, i: &V, n: usize, v: V) {
    set_slot(x.h(), idxu(i, n), v)
}

pub fn slen(x: &V) -> V {
    match x {
        V::Slice(_, _, l, _) | V::ByteSlice(_, _, l, _) => V::Int(*l as i64),
        _ => fault("slice expected"),
    }
}

pub fn scap(x: &V) -> V {
    match x {
        V::Slice(_, _, _, c) | V::ByteSlice(_, _, _, c) => V::Int(*c as i64),
        _ => fault("slice expected"),
    }
}

pub fn strlen(x: &V) -> V {
    V::Int(x.bytes().len() as i64)
}

pub fn nil_slice(x: &V) -> bool {
    matches!(x, V::Slice(0, ..) | V::ByteSlice(0, ..))
}

/// x.(T) for a concrete T: whether x holds T.
pub fn is_type(x: &V, t: &'static TypeDesc) -> bool {
    matches!(dyn_type(x), Some(d) if core::ptr::eq(d, t))
}

/// The value of a non-nil interface.
pub fn unboxed(x: &V) -> V {
    unbox(x).map(|(_, v)| v).unwrap_or(V::Nil)
}

/// x.(I) for an interface I: whether x is non-nil and has every method.
pub fn implements(x: &V, ids: &[&'static str]) -> bool {
    match dyn_type(x) {
        Some(t) => implements_all(t, ids).is_none(),
        None => false,
    }
}

/// The source name of the first method x's type lacks.
pub fn missing_method(x: &V, ids: &[&'static str], names: &[&'static str]) -> Option<&'static str> {
    let t = dyn_type(x)?;
    let id = implements_all(t, ids)?;
    ids.iter().position(|i| *i == id).map(|k| names[k])
}

pub fn zero_nil() -> V {
    V::Nil
}

pub fn zero_int() -> V {
    V::Int(0)
}

pub fn zero_bool() -> V {
    V::Bool(false)
}

pub fn zero_string() -> V {
    s(b"")
}

pub fn zero_slice() -> V {
    NIL_SLICE
}


pub fn zero_byte_slice() -> V { BYTE_NIL }

pub fn byte_array(v: Vec<u8>) -> V { V::Obj(alloc(Obj::Bytes(v))) }

/// A byte-only snapshot never creates a payload of scalar V values.
pub fn byte_snapshot(h: H, offset: usize, len: usize) -> Vec<u8> {
    if len == 0 { return Vec::new(); }
    with(h, |o| match o {
        Obj::Bytes(v) => v[offset..offset + len].to_vec(),
        _ => fault("byte backing expected"),
    })
}

pub fn byte_array_clone(x: &V) -> V {
    let v = with(x.h(), |o| match o {
        Obj::Bytes(v) => v.clone(),
        _ => fault("byte array expected"),
    });
    byte_array(v)
}

pub fn byte_array_set(d: &V, s: &V) {
    if d.h() == s.h() { return; }
    let v = byte_snapshot(s.h(), 0, vals_len(s.h()));
    with(d.h(), |o| match o {
        Obj::Bytes(dst) => dst.copy_from_slice(&v),
        _ => fault("byte array expected"),
    });
}

pub fn byte_array_eq(a: &V, b: &V) -> bool {
    if a.h() == b.h() { return true; }
    let v = byte_snapshot(a.h(), 0, vals_len(a.h()));
    with(b.h(), |o| match o {
        Obj::Bytes(w) => v == *w,
        _ => fault("byte array expected"),
    })
}

pub fn byte_array_key(a: &V) -> Key {
    Key::Bytes(Rc::from(byte_snapshot(a.h(), 0, vals_len(a.h()))))
}

/// Typed zeros for spare capacity; reuse must preserve existing backing values.
pub fn zero_append_growth(result: V, previous: &V, zero: fn()->V) -> V {
    let (h,_,l,c,bytes)=slice_parts(&result);
    let(old,_,_,_,_)=slice_parts(previous);
    if h != old && !bytes {
        // A zero factory may allocate traced aggregate storage; never call it
        // while the heap's RefCell is borrowed by with().
        let zeros: Vec<V> = (l..c).map(|_| zero()).collect();
        with(h, |o| match o {
            Obj::Vals(v) => { for (i,value) in (l..c).zip(zeros) { v[i as usize]=value; } },
            _ => fault("slice backing expected"),
        });
    }
    result
}
