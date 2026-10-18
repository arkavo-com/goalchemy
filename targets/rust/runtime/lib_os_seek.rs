use super::*;
pub fn lib_os_seek(file: V, offset: V, whence: V) -> V {
    let (offset, whence) = (offset.i(), whence.i());
    let from = match whence {
        0 => std::io::SeekFrom::Start(offset as u64),
        1 => std::io::SeekFrom::Current(offset),
        2 => std::io::SeekFrom::End(offset),
        _ => return tuple(vec![V::Int(0), V::Int(8)]),
    };
    if whence == 0 && offset < 0 {
        return tuple(vec![V::Int(0), V::Int(8)]);
    }
    let r = os_with(file.i(), |f| {
        use std::io::Seek;
        f.seek(from).map_err(|e| os_status(&e))
    });
    match r.unwrap_or(Err(8)) {
        Ok(pos) => tuple(vec![V::Int(pos as i64), V::Int(0)]),
        Err(code) => tuple(vec![V::Int(0), V::Int(code)]),
    }
}
