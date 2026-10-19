//! Values. Every Go value is a V: integers of every kind are i64 normalized
//! to their Go width (unsigned 64-bit values keep their bits), strings are
//! immutable byte strings, and shared storage lives in the traced heap and
//! is referenced by handle.
use super::*;
pub use core::cell::{Cell, RefCell};
#[cfg(not(goalchemy_nostd))]
pub use std::rc::Rc;
#[cfg(goalchemy_nostd)]
pub use alloc::rc::Rc;

/// A heap handle; 0 is never a live object.
pub type H = u32;

#[derive(Clone)]
pub enum V {
    Nil,
    Bool(bool),
    Int(i64),
    /// Already rounded IEEE scalar, widened exactly for binary32.
    Float(f64),
    Str(Rc<[u8]>),
    /// Struct, array, map, channel, function, interface box, or opaque object.
    Obj(H),
    /// Pointer to a scalar slot of a heap object.
    Ptr(H, u32),
    /// Slice header: backing object (0 for nil), offset, length, capacity.
    Slice(H, u32, u32, u32),
    /// Native byte slice header; the variant retains its element hint when nil.
    ByteSlice(H, u32, u32, u32),
    /// Several results of one call; consumed immediately by the caller.
    Tuple(Rc<[V]>),
    /// A resumable frame returned by a starter.
    #[cfg(not(goalchemy_nostd))]
    Frame(Rc<Frame>),
}

pub const NIL_SLICE: V = V::Slice(0, 0, 0, 0);
pub const BYTE_NIL: V = V::ByteSlice(0, 0, 0, 0);

/// Shared slice operations preserve the header kind, including typed nil.
pub fn slice_parts(x: &V) -> (H, u32, u32, u32, bool) {
    match x {
        V::Slice(h, o, l, c) => (*h, *o, *l, *c, false),
        V::ByteSlice(h, o, l, c) => (*h, *o, *l, *c, true),
        _ => fault("slice expected"),
    }
}

pub fn slice_header(h: H, o: u32, l: u32, c: u32, bytes: bool) -> V {
    if bytes { V::ByteSlice(h, o, l, c) } else { V::Slice(h, o, l, c) }
}

impl V {
    #[inline]
    pub fn i(&self) -> i64 {
        match self {
            V::Int(x) => *x,
            _ => fault("integer expected"),
        }
    }

    #[inline]
    pub fn f(&self) -> f64 {
        match self { V::Float(x) => *x, _ => fault("float expected") }
    }

    #[inline]
    pub fn b(&self) -> bool {
        match self {
            V::Bool(x) => *x,
            _ => fault("bool expected"),
        }
    }

    pub fn bytes(&self) -> Rc<[u8]> {
        match self {
            V::Str(s) => s.clone(),
            _ => fault("string expected"),
        }
    }

    /// The handle of an object value; 0 for nil.
    pub fn h(&self) -> H {
        match self {
            V::Obj(h) => *h,
            V::Nil => 0,
            _ => fault("object expected"),
        }
    }

    pub fn is_nil(&self) -> bool {
        matches!(self, V::Nil)
    }

    /// Element k of a multiple-result value.
    pub fn at(&self, k: usize) -> V {
        match self {
            V::Tuple(t) => t[k].clone(),
            _ => fault("tuple expected"),
        }
    }

    #[cfg(not(goalchemy_nostd))]
    pub fn frame(&self) -> Rc<Frame> {
        match self {
            V::Frame(f) => f.clone(),
            _ => fault("frame expected"),
        }
    }
}

pub fn s(b: &[u8]) -> V {
    V::Str(Rc::from(b))
}

pub fn int(x: i64) -> V {
    V::Int(x)
}

pub fn tuple(vs: Vec<V>) -> V {
    V::Tuple(Rc::from(vs))
}

/// Map keys: a hashable encoding of comparable values.
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Key {
    Nil,
    Bool(bool),
    Int(i64),
    Float(u64),
    NaN(u64),
    Str(Rc<[u8]>),
    H(H),
    Ptr(H, u32),
    List(Rc<[Key]>),
    Bytes(Rc<[u8]>),
    Iface(u32, Rc<Key>),
}

/// The key of a basic, pointer, channel, or other identity-compared value.
pub fn vkey(v: &V) -> Key {
    match v {
        V::Nil => Key::Nil,
        V::Bool(b) => Key::Bool(*b),
        V::Int(i) => Key::Int(*i),
        V::Float(x) => {
            if x.is_nan() {
                thread_local! { static NEXT: Cell<u64> = const { Cell::new(0) }; }
                Key::NaN(NEXT.with(|n| { let v = n.get().checked_add(1).unwrap_or_else(|| fault("NaN key identity exhausted")); n.set(v); v }))
            } else { Key::Float(if *x == 0.0 { 0 } else { x.to_bits() }) }
        },
        V::Str(s) => Key::Str(s.clone()),
        V::Obj(h) => Key::H(*h),
        V::Ptr(h, i) => Key::Ptr(*h, *i),
        _ => fault("unhashable host value"),
    }
}

pub fn key_list(ks: Vec<Key>) -> Key {
    Key::List(Rc::from(ks))
}

/// Equality of basic, pointer, channel, and other identity-compared values.
pub fn veq(a: &V, b: &V) -> bool {
    match (a, b) {
        (V::Nil, V::Nil) => true,
        (V::Bool(x), V::Bool(y)) => x == y,
        (V::Int(x), V::Int(y)) => x == y,
        (V::Float(x), V::Float(y)) => x == y,
        (V::Str(x), V::Str(y)) => x[..] == y[..],
        (V::Obj(x), V::Obj(y)) => x == y,
        (V::Ptr(x, i), V::Ptr(y, j)) => x == y && i == j,
        _ => false,
    }
}

/// An implementation fault: never a source panic.
pub fn fault(msg: &str) -> ! {
    #[cfg(not(goalchemy_nostd))]
    std::panic::resume_unwind(Box::new(format!("goalchemy fault: {}", msg)));
    #[cfg(goalchemy_nostd)]
    panic!("goalchemy fault: {}", msg)
}
