use super::*;
pub fn lib_os_remove(name: V) -> V {
    let r = (|| -> Result<(), i64> {
        let path = os_path(&name.bytes())?;
        let unlink = match std::fs::remove_file(&path) {
            Ok(()) => return Ok(()),
            Err(e) => e,
        };
        match std::fs::remove_dir(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotADirectory => Err(os_status(&unlink)),
            Err(e) => Err(os_status(&e)),
        }
    })();
    V::Int(match r {
        Ok(()) => 0,
        Err(code) => code,
    })
}
