#![cfg(feature = "native")]
use super::*;

pub fn lib_http_exchange_read(t: &Rc<Task>, v: V, max: V) {
    let zero: fn(V) -> Vec<V> = |e| vec![BYTE_NIL, V::Bool(false), e];
    let max = max.i();
    let id = match exchange_id(&v) {
        Some(id) if (1..=1 << 20).contains(&max) => id,
        _ => {
            t.set_rv(zero(exchange_error(b"http: invalid request or limit")));
            return;
        }
    };
    match EXCHANGES.with(|e| e.borrow().get(&id).map(|x| x.responded)) {
        None => t.set_rv(zero(exchange_error(EXCHANGE_CLOSED))),
        Some(false) => t.set_rv(zero(exchange_error(EXCHANGE_STATE))),
        Some(true) => exchange_op(
            t,
            id,
            zero,
            move |reply| ExchangeCmd::Read(max as usize, reply),
            |chunk| match chunk {
                Some(data) => vec![HostWire::Bytes(data)],
                None => vec![HostWire::Bool(true)],
            },
            |w| match w.as_slice() {
                [HostWire::Bytes(data)] => vec![native_slice_bytes(data.clone()), V::Bool(false), V::Nil],
                [HostWire::Bool(true)] => vec![BYTE_NIL, V::Bool(true), V::Nil],
                _ => host_fault("HTTP exchange wire decode"),
            },
        ),
    }
}
