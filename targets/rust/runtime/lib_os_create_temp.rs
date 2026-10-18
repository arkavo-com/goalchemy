use super::*;
fn os_path_bytes(p: &std::path::Path) -> Vec<u8> {
    #[cfg(unix)]
    {
        use std::os::unix::ffi::OsStrExt;
        p.as_os_str().as_bytes().to_vec()
    }
    #[cfg(not(unix))]
    {
        p.to_string_lossy().into_owned().into_bytes()
    }
}
fn os_random_name() -> String {
    use std::hash::{BuildHasher, Hash, Hasher};
    static COUNTER: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let mut h = std::collections::hash_map::RandomState::new().build_hasher();
    COUNTER.fetch_add(1, std::sync::atomic::Ordering::Relaxed).hash(&mut h);
    std::process::id().hash(&mut h);
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0).hash(&mut h);
    let mut x = h.finish();
    let alphabet = b"0123456789abcdefghijklmnopqrstuvwxyz";
    let mut out = String::new();
    for _ in 0..10 {
        out.push(alphabet[(x % 36) as usize] as char);
        x /= 36;
    }
    out
}
pub fn lib_os_create_temp(dir: V, prefix: V, suffix: V) -> V {
    let (dir, prefix, suffix) = (dir.bytes(), prefix.bytes(), suffix.bytes());
    let fail = |name: Vec<u8>, code: i64| tuple(vec![V::Int(0), s(&name), V::Int(code)]);
    if dir.contains(&0) || prefix.contains(&0) || suffix.contains(&0) || prefix.contains(&b'/') || suffix.contains(&b'/') {
        return fail(vec![], 8);
    }
    let base = if dir.is_empty() {
        std::env::temp_dir()
    } else {
        match os_path(&dir) {
            Ok(p) => p,
            Err(code) => return fail(vec![], code),
        }
    };
    let (pre, suf) = (String::from_utf8_lossy(&prefix).into_owned(), String::from_utf8_lossy(&suffix).into_owned());
    for i in 0.. {
        let candidate = base.join(format!("{}{}{}", pre, os_random_name(), suf));
        let mut options = std::fs::OpenOptions::new();
        options.read(true).write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        match options.open(&candidate) {
            Ok(file) => return tuple(vec![V::Int(os_register(file)), s(&os_path_bytes(&candidate)), V::Int(0)]),
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists && i < 10000 => continue,
            Err(e) => return fail(os_path_bytes(&candidate), os_status(&e)),
        }
    }
    fail(vec![], 9)
}
