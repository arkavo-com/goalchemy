//! Integer kinds: every kind is an i64 normalized to its Go width; 64-bit
//! unsigned values are stored as their two's-complement bits.
use super::*;

#[inline] pub fn w8(x: i64) -> i64 { x as i8 as i64 }
#[inline] pub fn w16(x: i64) -> i64 { x as i16 as i64 }
#[inline] pub fn w32(x: i64) -> i64 { x as i32 as i64 }
#[inline] pub fn w64(x: i64) -> i64 { x }
#[inline] pub fn wu8(x: i64) -> i64 { x & 0xFF }
#[inline] pub fn wu16(x: i64) -> i64 { x & 0xFFFF }
#[inline] pub fn wu32(x: i64) -> i64 { x & 0xFFFF_FFFF }
#[inline] pub fn wu64(x: i64) -> i64 { x }

/// Validates a signed shift count, clamped to 64.
pub fn count(n: &V) -> u32 {
    let n = n.i();
    if n < 0 {
        runtime_panic("negative shift amount");
    }
    n.min(64) as u32
}

/// Validates an unsigned shift count, clamped to 64.
pub fn countu(n: &V) -> u32 {
    let n = n.i();
    if n < 0 || n > 64 { 64 } else { n as u32 }
}

pub fn div_zero() -> ! {
    runtime_panic("integer divide by zero")
}

/// The decimal form of an unsigned 64-bit value, for print.
pub fn u64s(v: V) -> V {
    s((v.i() as u64).to_string().as_bytes())
}

pub fn cmpu(a: &V, b: &V) -> core::cmp::Ordering {
    (a.i() as u64).cmp(&(b.i() as u64))
}
