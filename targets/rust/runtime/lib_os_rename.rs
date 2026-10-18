use super::*;
pub fn lib_os_rename(oldpath: V, newpath: V) -> V {
    let r = (|| -> Result<(), i64> {
        let from = os_path(&oldpath.bytes())?;
        let to = os_path(&newpath.bytes())?;
        std::fs::rename(from, to).map_err(|e| os_status(&e))
    })();
    V::Int(match r {
        Ok(()) => 0,
        Err(code) => code,
    })
}
