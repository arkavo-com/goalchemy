#![cfg(feature = "native")]
use super::*;
use std::sync::{atomic::AtomicBool, Arc};

const MAX_EXCHANGE_MILLIS: i64 = 24 * 60 * 60 * 1000;

#[allow(clippy::too_many_arguments)]
pub fn lib_http_open(t: &Rc<Task>, context: V, method: V, url: V, headers: V, length: V, timeout: V, options: V) {
    let fail = |e: V| vec![V::Nil, e];
    let invalid = || exchange_error(b"http: invalid request or limit");
    if context.is_nil() {
        t.set_rv(fail(invalid()));
        return;
    }
    check_context(&context);
    let error = std_context_context_err(context.clone());
    if !error.is_nil() {
        t.set_rv(fail(error));
        return;
    }
    let method = method.bytes().to_vec();
    let (length, timeout) = (length.i(), timeout.i());
    if !(method == b"GET" || method == b"POST") || length < -1 || method == b"GET" && length != 0 || !(1..=MAX_EXCHANGE_MILLIS).contains(&timeout) {
        t.set_rv(fail(invalid()));
        return;
    }
    let (url, pairs) = match http_url_headers(&url, &headers) {
        Ok(v) => v,
        Err(_) => {
            t.set_rv(fail(invalid()));
            return;
        }
    };
    let (h, o, n, _, _) = slice_parts(&options);
    let values: Vec<Vec<u8>> = (0..n as usize).map(|i| slot(h, o as usize + i).bytes().to_vec()).collect();
    let tls = match http_tls(&values) {
        Ok(tls) => tls,
        Err(bad) => {
            t.set_rv(fail(if bad { exchange_error(b"http: invalid TLS option") } else { invalid() }));
            return;
        }
    };
    let client = match http_client(tls, None) {
        Ok(c) => c,
        Err(()) => {
            t.set_rv(fail(exchange_error(b"http: invalid TLS option")));
            return;
        }
    };
    let mut request = client.request(if method == b"GET" { reqwest::Method::GET } else { reqwest::Method::POST }, url);
    for (n, v) in pairs {
        request = request.header(n, v)
    }
    if length == 0 && method == b"POST" {
        request = request.body(Vec::new())
    }
    let stop = Arc::new(tokio::sync::Notify::new());
    let commands = match exchange_driver(client, request, length, stop.clone()) {
        Ok(c) => c,
        Err(e) => host_fault(&format!("HTTP exchange submission: {e}")),
    };
    let deadline = deadline_after(clock_now(), timeout.saturating_mul(1_000_000));
    let parent = with_ctx(&context, |c| c.deadline);
    let deadline = parent.map_or(deadline, |p| p.min(deadline));
    t.set_rv(vec![
        own_exchange(ExchangeState { commands, stop, stopped: Arc::new(AtomicBool::new(false)), length, written: 0, responded: false, deadline, context }),
        V::Nil,
    ])
}
