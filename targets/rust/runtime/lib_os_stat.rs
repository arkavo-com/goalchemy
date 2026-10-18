use super::*;
pub fn lib_os_stat(name: V) -> V {
    let r = (|| -> Result<(i64, i64, bool), i64> {
        let path = os_path(&name.bytes())?;
        let m = std::fs::metadata(path).map_err(|e| os_status(&e))?;
        #[cfg(unix)]
        let mode = {
            use std::os::unix::fs::PermissionsExt;
            (m.permissions().mode() & 0o777) as i64
        };
        #[cfg(not(unix))]
        let mode = if m.is_dir() { 0o777 } else { 0o666 };
        Ok((m.len() as i64, mode, m.is_dir()))
    })();
    match r {
        Ok((size, mode, dir)) => tuple(vec![V::Int(size), V::Int(mode), V::Bool(dir), V::Int(0)]),
        Err(code) => tuple(vec![V::Int(0), V::Int(0), V::Bool(false), V::Int(code)]),
    }
}
