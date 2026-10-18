use super::*;
pub fn lib_os_sync(file: V) -> V {
    let r = os_with(file.i(), |f| f.sync_all().map_err(|e| os_status(&e)));
    V::Int(match r.unwrap_or(Err(8)) {
        Ok(()) => 0,
        Err(code) => code,
    })
}
