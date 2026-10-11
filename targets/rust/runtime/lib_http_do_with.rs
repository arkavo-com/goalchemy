#![cfg(feature = "native")]
use super::*;

#[allow(clippy::too_many_arguments)]
pub fn lib_http_do_with(
    t: &Rc<Task>,
    context: V,
    method: V,
    url: V,
    headers: V,
    input: V,
    max: V,
    timeout: V,
    options: V,
) {
    let (h, o, n, _, _) = slice_parts(&options);
    let values = (0..n as usize).map(|i| slot(h, o as usize + i).bytes().to_vec()).collect();
    http_do(t, context, method, url, headers, input, max, timeout, values)
}
