//! UTF-8 over byte strings.

#[cfg(goalchemy_nostd)]
use alloc::vec::Vec;

pub const RUNE_ERROR: i64 = 0xFFFD;

/// Decodes the rune at byte offset i: (rune, width).
pub fn decode(s: &[u8], i: usize) -> (i64, usize) {
    let n = s.len() - i;
    let b0 = s[i] as i64;
    let err = (RUNE_ERROR, 1);
    if b0 < 0x80 {
        return (b0, 1);
    }
    let at = |k: usize| s[i + k] as i64;
    if (0xC2..=0xDF).contains(&b0) {
        if n < 2 { return err; }
        let b1 = at(1);
        if !(0x80..=0xBF).contains(&b1) { return err; }
        return (((b0 & 0x1F) << 6) | (b1 & 0x3F), 2);
    }
    if (0xE0..=0xEF).contains(&b0) {
        if n < 3 { return err; }
        let b1 = at(1);
        let lo = if b0 == 0xE0 { 0xA0 } else { 0x80 };
        let hi = if b0 == 0xED { 0x9F } else { 0xBF };
        if b1 < lo || b1 > hi { return err; }
        let b2 = at(2);
        if !(0x80..=0xBF).contains(&b2) { return err; }
        return (((b0 & 0x0F) << 12) | ((b1 & 0x3F) << 6) | (b2 & 0x3F), 3);
    }
    if (0xF0..=0xF4).contains(&b0) {
        if n < 4 { return err; }
        let b1 = at(1);
        let lo = if b0 == 0xF0 { 0x90 } else { 0x80 };
        let hi = if b0 == 0xF4 { 0x8F } else { 0xBF };
        if b1 < lo || b1 > hi { return err; }
        let (b2, b3) = (at(2), at(3));
        if !(0x80..=0xBF).contains(&b2) || !(0x80..=0xBF).contains(&b3) { return err; }
        return (((b0 & 0x07) << 18) | ((b1 & 0x3F) << 12) | ((b2 & 0x3F) << 6) | (b3 & 0x3F), 4);
    }
    err
}

/// Encodes a code point; invalid code points encode U+FFFD.
pub fn encode(r: i64, out: &mut Vec<u8>) {
    let r = if r < 0 || r > 0x10FFFF || (0xD800..=0xDFFF).contains(&r) { RUNE_ERROR } else { r };
    let c = r as u32;
    if c < 0x80 {
        out.push(c as u8);
    } else if c < 0x800 {
        out.extend_from_slice(&[(0xC0 | (c >> 6)) as u8, (0x80 | (c & 0x3F)) as u8]);
    } else if c < 0x10000 {
        out.extend_from_slice(&[(0xE0 | (c >> 12)) as u8, (0x80 | ((c >> 6) & 0x3F)) as u8, (0x80 | (c & 0x3F)) as u8]);
    } else {
        out.extend_from_slice(&[
            (0xF0 | (c >> 18)) as u8,
            (0x80 | ((c >> 12) & 0x3F)) as u8,
            (0x80 | ((c >> 6) & 0x3F)) as u8,
            (0x80 | (c & 0x3F)) as u8,
        ]);
    }
}
