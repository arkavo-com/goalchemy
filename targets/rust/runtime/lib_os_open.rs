use super::*;
use std::collections::HashMap;
use std::sync::{Mutex, OnceLock};
pub const OS_MAX_CHUNK: usize = 1 << 20;
pub struct OsFile {
    pub file: std::fs::File,
}
fn os_files() -> &'static Mutex<(i64, HashMap<i64, OsFile>)> {
    static FILES: OnceLock<Mutex<(i64, HashMap<i64, OsFile>)>> = OnceLock::new();
    FILES.get_or_init(|| Mutex::new((0, HashMap::new())))
}
pub fn os_register(file: std::fs::File) -> i64 {
    let mut g = os_files().lock().unwrap_or_else(|e| e.into_inner());
    g.0 += 1;
    let id = g.0;
    g.1.insert(id, OsFile { file });
    id
}
pub fn os_with<T>(handle: i64, f: impl FnOnce(&mut std::fs::File) -> T) -> Option<T> {
    let mut g = os_files().lock().unwrap_or_else(|e| e.into_inner());
    g.1.get_mut(&handle).map(|e| f(&mut e.file))
}
pub fn os_forget(handle: i64) -> Option<std::fs::File> {
    let mut g = os_files().lock().unwrap_or_else(|e| e.into_inner());
    g.1.remove(&handle).map(|e| e.file)
}
pub fn lib_os_open(name: V, flag: V, perm: V) -> V {
    let flag = flag.i();
    let r = (|| -> Result<i64, i64> {
        let path = os_path(&name.bytes())?;
        if flag & !63 != 0 || flag & 3 == 3 {
            return Err(8);
        }
        let access = flag & 3;
        let mut options = std::fs::OpenOptions::new();
        options.read(access != 1).write(access != 0);
        if flag & 4 != 0 {
            options.append(true);
        }
        if flag & 8 != 0 {
            if flag & 16 != 0 {
                options.create_new(true);
            } else {
                options.create(true);
            }
        }
        if flag & 32 != 0 && access != 0 {
            options.truncate(true);
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode((perm.i() & 0o777) as u32);
        }
        let _ = &perm;
        let file = options.open(path).map_err(|e| os_status(&e))?;
        Ok(os_register(file))
    })();
    match r {
        Ok(id) => tuple(vec![V::Int(id), V::Int(0)]),
        Err(code) => tuple(vec![V::Int(0), V::Int(code)]),
    }
}
