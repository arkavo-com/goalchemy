//! Print output is supplied by the embedding application.
pub fn stderr(bytes: &[u8]) {
    super::owner::output(bytes);
}
pub fn stdout(bytes: &[u8]) {
    super::owner::output(bytes);
}
