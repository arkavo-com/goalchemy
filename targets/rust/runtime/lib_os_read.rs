use super::*;
pub fn os_chunk(r: Result<Vec<u8>, i64>) -> V {
    match r {
        Ok(data) => {
            let eof = data.is_empty();
            let n = data.len() as u32;
            tuple(vec![V::ByteSlice(byte_array(data).h(), 0, n, n), V::Bool(eof), V::Int(0)])
        }
        Err(code) => tuple(vec![BYTE_NIL, V::Bool(false), V::Int(code)]),
    }
}
pub fn lib_os_read(file: V, max: V) -> V {
    let max = max.i();
    if max < 1 || max as usize > OS_MAX_CHUNK {
        return os_chunk(Err(8));
    }
    let r = os_with(file.i(), |f| {
        use std::io::Read;
        let mut buf = vec![0u8; max as usize];
        loop {
            match f.read(&mut buf) {
                Ok(n) => {
                    buf.truncate(n);
                    return Ok(buf);
                }
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                Err(e) => return Err(os_status(&e)),
            }
        }
    });
    os_chunk(r.unwrap_or(Err(8)))
}
