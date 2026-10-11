//! Streamed lib/http exchanges: each exchange owns a driver thread running
//! reqwest on a current-thread Tokio runtime; operations are commands.
#![cfg(feature = "native")]
use super::*;
use std::cell::{Cell, RefCell};
use std::collections::BTreeMap;
use std::sync::{
    atomic::{AtomicBool, Ordering},
    mpsc, Arc,
};
use tokio::sync::{mpsc as amp, oneshot, Notify};

pub const EXCHANGE_CLOSED: &[u8] = b"http: exchange closed";
pub const EXCHANGE_STATE: &[u8] = b"http: invalid exchange operation";
pub const EXCHANGE_FAILED: &str = "http: transport failure";

pub enum ExchangeCmd {
    Write(Vec<u8>, mpsc::Sender<Result<(), String>>),
    Response(mpsc::Sender<Result<(i64, Vec<Vec<u8>>), String>>),
    Read(usize, mpsc::Sender<Result<Option<Vec<u8>>, String>>),
}

/// Request body handoff: a chunk is acknowledged when the connection takes it.
struct ChannelBody {
    rx: amp::Receiver<(Vec<u8>, oneshot::Sender<()>)>,
    length: Option<u64>,
}
impl http_body::Body for ChannelBody {
    type Data = bytes::Bytes;
    type Error = std::io::Error;
    fn poll_frame(
        mut self: std::pin::Pin<&mut Self>,
        cx: &mut std::task::Context<'_>,
    ) -> std::task::Poll<Option<Result<http_body::Frame<bytes::Bytes>, std::io::Error>>> {
        match self.rx.poll_recv(cx) {
            std::task::Poll::Ready(Some((data, ack))) => {
                let _ = ack.send(());
                std::task::Poll::Ready(Some(Ok(http_body::Frame::data(bytes::Bytes::from(data)))))
            }
            std::task::Poll::Ready(None) => std::task::Poll::Ready(None),
            std::task::Poll::Pending => std::task::Poll::Pending,
        }
    }
    fn size_hint(&self) -> http_body::SizeHint {
        match self.length {
            Some(n) => http_body::SizeHint::with_exact(n),
            None => http_body::SizeHint::default(),
        }
    }
}

pub struct ExchangeState {
    pub commands: amp::UnboundedSender<ExchangeCmd>,
    pub stop: Arc<Notify>,
    pub stopped: Arc<AtomicBool>,
    pub length: i64,
    pub written: i64,
    pub responded: bool,
    pub deadline: i64,
    pub context: V,
}
impl ExchangeState {
    pub fn abort(stop: &Arc<Notify>, stopped: &Arc<AtomicBool>) {
        stopped.store(true, Ordering::Release);
        stop.notify_one();
    }
}

thread_local! {pub static EXCHANGES: RefCell<BTreeMap<u64, ExchangeState>> = RefCell::new(BTreeMap::new()); static NEXT_EXCHANGE: Cell<u64> = Cell::new(1);}

/// Aborts every exchange of this owner, on retirement.
pub fn exchange_retire() {
    EXCHANGES.with(|e| {
        for (_, x) in std::mem::take(&mut *e.borrow_mut()) {
            ExchangeState::abort(&x.stop, &x.stopped)
        }
    })
}

pub fn own_exchange(state: ExchangeState) -> V {
    let id = NEXT_EXCHANGE.with(|n| {
        let id = n.get();
        n.set(id.checked_add(1).unwrap_or_else(|| host_fault("exchange ID exhausted")));
        id
    });
    EXCHANGES.with(|e| e.borrow_mut().insert(id, state));
    on_native_retire(exchange_retire);
    V::Obj(alloc(Obj::NativeKey(id, sched(|s| s.domain))))
}

/// The exchange ID of a handle; None for nil. Closed exchanges have no state.
pub fn exchange_id(v: &V) -> Option<u64> {
    if v.is_nil() {
        return None;
    }
    with(v.h(), |o| match o {
        Obj::NativeKey(id, owner) if *owner == sched(|s| s.domain) => Some(*id),
        _ => host_fault("foreign HTTP exchange"),
    })
}

pub fn exchange_error(message: &[u8]) -> V {
    std_errors_new(s(message))
}

/// Starts the driver thread for one exchange.
pub fn exchange_driver(
    client: reqwest::Client,
    request: reqwest::RequestBuilder,
    length: i64,
    stop: Arc<Notify>,
) -> Result<amp::UnboundedSender<ExchangeCmd>, String> {
    let (tx, mut rx) = amp::unbounded_channel::<ExchangeCmd>();
    let spawned = std::thread::Builder::new().name("goalchemy-http".into()).spawn(move || {
        let runtime = match tokio::runtime::Builder::new_current_thread().enable_all().build() {
            Ok(r) => r,
            Err(_) => return,
        };
        runtime.block_on(async move {
            let (body_tx, request) = if length != 0 {
                let (body_tx, body_rx) = amp::channel(1);
                let body = ChannelBody { rx: body_rx, length: (length > 0).then_some(length as u64) };
                (Some(body_tx), request.body(reqwest::Body::wrap(body)))
            } else {
                (None, request)
            };
            let mut body_tx = body_tx;
            let send = tokio::task::spawn(request.send());
            let mut send = Some(send);
            let mut response: Option<reqwest::Response> = None;
            let mut head: Option<(i64, Vec<Vec<u8>>)> = None;
            let mut pending: Vec<u8> = Vec::new();
            let mut offset = 0usize;
            loop {
                let cmd = tokio::select! {
                    c = rx.recv() => match c { Some(c) => c, None => break },
                    _ = stop.notified() => break,
                };
                match cmd {
                    ExchangeCmd::Write(data, reply) => {
                        let result = async {
                            let tx = body_tx.as_ref().ok_or_else(|| EXCHANGE_FAILED.to_string())?;
                            let (ack, acked) = oneshot::channel();
                            tx.send((data, ack)).await.map_err(|_| EXCHANGE_FAILED.to_string())?;
                            acked.await.map_err(|_| EXCHANGE_FAILED.to_string())
                        };
                        let r = tokio::select! { r = result => r, _ = stop.notified() => { let _ = reply.send(Err(EXCHANGE_FAILED.into())); break } };
                        let _ = reply.send(r);
                    }
                    ExchangeCmd::Response(reply) => {
                        body_tx = None;
                        if head.is_none() {
                            let r = match send.take() {
                                Some(s) => tokio::select! { r = s => r, _ = stop.notified() => { let _ = reply.send(Err(EXCHANGE_FAILED.into())); break } },
                                None => { let _ = reply.send(Err(EXCHANGE_FAILED.into())); continue }
                            };
                            match r {
                                Ok(Ok(resp)) => match exchange_headers(&resp) {
                                    Ok(h) => { head = Some((resp.status().as_u16() as i64, h)); response = Some(resp) }
                                    Err(e) => { let _ = reply.send(Err(e)); continue }
                                },
                                _ => { let _ = reply.send(Err(EXCHANGE_FAILED.into())); continue }
                            }
                        }
                        let _ = reply.send(Ok(head.clone().unwrap()));
                    }
                    ExchangeCmd::Read(max, reply) => {
                        let r = async {
                            if offset < pending.len() {
                                let n = max.min(pending.len() - offset);
                                let out = pending[offset..offset + n].to_vec();
                                offset += n;
                                return Ok::<Option<Vec<u8>>, String>(Some(out));
                            }
                            let resp = response.as_mut().ok_or_else(|| EXCHANGE_FAILED.to_string())?;
                            loop {
                                match resp.chunk().await.map_err(|_| EXCHANGE_FAILED.to_string())? {
                                    None => return Ok(None),
                                    Some(c) if c.is_empty() => continue,
                                    Some(c) => {
                                        let n = max.min(c.len());
                                        pending = c.to_vec();
                                        offset = n;
                                        return Ok(Some(pending[..n].to_vec()));
                                    }
                                }
                            }
                        };
                        let r = tokio::select! { r = r => r, _ = stop.notified() => { let _ = reply.send(Err(EXCHANGE_FAILED.into())); break } };
                        let _ = reply.send(r);
                    }
                }
            }
            drop(response);
            drop(client);
        });
        drop(runtime);
    });
    spawned.map(|_| tx).map_err(|e| e.to_string())
}

fn exchange_headers(response: &reqwest::Response) -> Result<Vec<Vec<u8>>, String> {
    let close = response
        .headers()
        .get_all(reqwest::header::CONNECTION)
        .iter()
        .any(|v| v.as_bytes().split(|c| *c == b',').any(|t| t.trim_ascii().eq_ignore_ascii_case(b"close")));
    let mut names: Vec<_> = response.headers().keys().collect();
    names.sort_by(|a, b| a.as_str().cmp(b.as_str()));
    let mut total = 0usize;
    let mut out = Vec::new();
    for name in names {
        if name == reqwest::header::TRANSFER_ENCODING || close && name == reqwest::header::CONNECTION {
            continue;
        }
        let canonical = name
            .as_str()
            .split('-')
            .map(|s| {
                let mut s = s.to_owned();
                s[0..1].make_ascii_uppercase();
                s
            })
            .collect::<Vec<_>>()
            .join("-");
        for value in response.headers().get_all(name) {
            total += canonical.len() + value.as_bytes().len() + 4;
            if total > 65536 {
                return Err("http: response headers exceed limit".into());
            }
            out.push(canonical.as_bytes().to_vec());
            out.push(value.as_bytes().to_vec());
        }
    }
    Ok(out)
}

/// Sends a command from a worker and waits for its reply. The per-operation
/// boundary carries the exchange deadline; canceling the operation aborts it.
pub fn exchange_op<R: Send + 'static>(
    t: &Rc<Task>,
    id: u64,
    zero: fn(V) -> Vec<V>,
    command: impl FnOnce(mpsc::Sender<Result<R, String>>) -> ExchangeCmd + Send + 'static,
    wire: fn(R) -> Vec<HostWire>,
    decode: impl FnOnce(Vec<HostWire>) -> Vec<V> + 'static,
) {
    let (commands, stop, stopped, deadline, context) = EXCHANGES.with(|e| {
        let e = e.borrow();
        let x = &e[&id];
        (x.commands.clone(), x.stop.clone(), x.stopped.clone(), x.deadline, x.context.clone())
    });
    let remaining = deadline.saturating_sub(clock_now());
    if remaining <= 0 {
        ExchangeState::abort(&stop, &stopped);
        t.set_rv(zero(context_deadline_exceeded()));
        return;
    }
    let boundary = host_boundary(context.clone(), Some(remaining));
    let (abort_stop, abort_flag) = (stop.clone(), stopped.clone());
    let token = register_host(
        t,
        boundary,
        vec![context],
        zero,
        move |w: Vec<HostWire>| match w.as_slice() {
            [HostWire::Text(e)] => zero(exchange_error(e.as_bytes())),
            _ => decode(w),
        },
        move || ExchangeState::abort(&abort_stop, &abort_flag),
        || {},
    );
    launch_host(token, move || {
        let (reply, result) = mpsc::channel();
        if stopped.load(Ordering::Acquire) || commands.send(command(reply)).is_err() {
            return vec![HostWire::Text(EXCHANGE_FAILED.into())];
        }
        match result.recv() {
            Ok(Ok(r)) => wire(r),
            Ok(Err(e)) => vec![HostWire::Text(e)],
            Err(_) => vec![HostWire::Text(EXCHANGE_FAILED.into())],
        }
    });
}

pub fn lib_http_exchange_close(t: &Rc<Task>, v: V) {
    if let Some(id) = exchange_id(&v) {
        if let Some(x) = EXCHANGES.with(|e| e.borrow_mut().remove(&id)) {
            ExchangeState::abort(&x.stop, &x.stopped)
        }
    }
    t.set_rv(vec![])
}
