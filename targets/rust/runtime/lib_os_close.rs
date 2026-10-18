use super::*;
pub fn lib_os_close(file: V) -> V {
    V::Int(match os_forget(file.i()) {
        None => 8,
        Some(f) => {
            drop(f);
            0
        }
    })
}
