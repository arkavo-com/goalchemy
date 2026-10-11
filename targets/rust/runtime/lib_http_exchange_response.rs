#![cfg(feature = "native")]
use super::*;

pub fn lib_http_exchange_response(t: &Rc<Task>, v: V) {
    let zero: fn(V) -> Vec<V> = |e| vec![V::Int(0), NIL_SLICE, e];
    let Some(id) = exchange_id(&v) else {
        t.set_rv(zero(exchange_error(b"http: invalid request or limit")));
        return;
    };
    let state = EXCHANGES.with(|e| {
        let mut e = e.borrow_mut();
        let x = e.get_mut(&id)?;
        x.responded = true;
        if x.length >= 0 && x.written != x.length {
            ExchangeState::abort(&x.stop, &x.stopped);
            return Some(false);
        }
        Some(true)
    });
    match state {
        None => t.set_rv(zero(exchange_error(EXCHANGE_CLOSED))),
        Some(false) => t.set_rv(zero(exchange_error(EXCHANGE_STATE))),
        Some(true) => exchange_op(
            t,
            id,
            zero,
            ExchangeCmd::Response,
            |(status, headers)| vec![HostWire::Int(status), HostWire::List(headers.into_iter().map(HostWire::Bytes).collect())],
            |w| match w.as_slice() {
                [HostWire::Int(status), HostWire::List(headers)] => {
                    let headers: Vec<V> = headers
                        .iter()
                        .map(|h| match h {
                            HostWire::Bytes(b) => s(b),
                            _ => host_fault("HTTP header decode"),
                        })
                        .collect();
                    let n = headers.len() as u32;
                    vec![V::Int(*status), V::Slice(vals(headers).h(), 0, n, n), V::Nil]
                }
                _ => host_fault("HTTP exchange wire decode"),
            },
        ),
    }
}
