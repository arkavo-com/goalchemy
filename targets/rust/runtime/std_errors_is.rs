//! std.errors.is: walks the Unwrap chain comparing and calling Is methods.
use super::*;

pub fn std_errors_is(err: V, target: V) -> V {
    if err.is_nil() || target.is_nil() {
        return V::Bool(err.is_nil() && target.is_nil());
    }
    let _root = temp_root(&[err.clone(), target.clone()]);
    let (tt, tv) = unbox(&target).unwrap();
    let mut cur = err;
    loop {
        let Some((ct, cv)) = unbox(&cur) else { return V::Bool(false) };
        let _keep = temp_root(&[cur.clone()]);
        if tt.comparable && core::ptr::eq(ct, tt) && (ct.eq)(&cv, &tv) {
            return V::Bool(true);
        }
        if let Some((_, is)) = ct.method("Is") {
            if is(&[], vec![cv.clone(), target.clone()]).b() {
                return V::Bool(true);
            }
        }
        let Some((_, unwrap)) = ct.method("Unwrap") else { return V::Bool(false) };
        cur = unwrap(&[], vec![cv]);
    }
}
