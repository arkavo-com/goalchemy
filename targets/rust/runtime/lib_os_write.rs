use super::*;
pub fn lib_os_write(file: V, data: V) -> V {
    let (h, o, n, _, _) = slice_parts(&data);
    if n as usize > OS_MAX_CHUNK {
        return tuple(vec![V::Int(0), V::Int(8)]);
    }
    let body = if h == 0 { vec![] } else { byte_snapshot(h, o as usize, n as usize) };
    let r = os_with(file.i(), |f| {
        use std::io::Write;
        let mut done = 0usize;
        while done < body.len() {
            match f.write(&body[done..]) {
                Ok(0) => return (done, 9),
                Ok(w) => done += w,
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                Err(e) => return (done, os_status(&e)),
            }
        }
        (done, 0)
    });
    let (done, code) = r.unwrap_or((0, 8));
    tuple(vec![V::Int(done as i64), V::Int(code)])
}
