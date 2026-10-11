#![cfg(feature = "native")]
use super::*;

pub fn lib_http_exchange_write(t: &Rc<Task>, v: V, data: V) {
    let zero: fn(V) -> Vec<V> = |e| vec![e];
    let chunk = match native_bytes(&data) {
        Ok(c) if c.len() <= 1 << 20 => c,
        _ => {
            t.set_rv(zero(exchange_error(b"http: invalid request or limit")));
            return;
        }
    };
    let Some(id) = exchange_id(&v) else {
        t.set_rv(zero(exchange_error(b"http: invalid request or limit")));
        return;
    };
    let state = EXCHANGES.with(|e| {
        let mut e = e.borrow_mut();
        let x = e.get_mut(&id)?;
        let n = chunk.len() as i64;
        if x.responded || x.length == 0 && n != 0 || x.length >= 0 && n > x.length - x.written {
            return Some(false);
        }
        x.written += n;
        Some(true)
    });
    match state {
        None => t.set_rv(zero(exchange_error(EXCHANGE_CLOSED))),
        Some(false) => t.set_rv(zero(exchange_error(EXCHANGE_STATE))),
        Some(true) if chunk.is_empty() => t.set_rv(vec![V::Nil]),
        Some(true) => exchange_op(t, id, zero, move |reply| ExchangeCmd::Write(chunk, reply), |()| vec![], |_| vec![V::Nil]),
    }
}
