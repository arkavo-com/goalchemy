//! core.string.compare: bytewise three-way comparison.
use super::*;

pub fn scompare(a: V, b: V) -> V {
    V::Int(a.bytes()[..].cmp(&b.bytes()[..]) as i64)
}

/// Bytewise ordering for the comparison operators.
pub fn scmp(a: &V, b: &V) -> core::cmp::Ordering {
    a.bytes()[..].cmp(&b.bytes()[..])
}
