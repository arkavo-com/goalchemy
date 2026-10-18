use super::*;
pub fn lib_os_read_at(file: V, offset: V, max: V) -> V {
    let (offset, max) = (offset.i(), max.i());
    if max < 1 || max as usize > OS_MAX_CHUNK || offset < 0 {
        return os_chunk(Err(8));
    }
    let r = os_with(file.i(), |f| {
        let mut buf = vec![0u8; max as usize];
        loop {
            #[cfg(unix)]
            let read = {
                use std::os::unix::fs::FileExt;
                f.read_at(&mut buf, offset as u64)
            };
            #[cfg(not(unix))]
            let read = {
                use std::os::windows::fs::FileExt;
                f.seek_read(&mut buf, offset as u64)
            };
            match read {
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
