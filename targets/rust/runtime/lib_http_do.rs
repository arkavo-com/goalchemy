//! Dedicated TLS-verifying transport. Native runtime teardown precedes ACK.
#![cfg(feature = "native")]
use super::*;
use std::io::Write;
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc,
};
use std::time::Duration;
fn http_zero(e: V) -> Vec<V> {
    vec![V::Int(0), NIL_SLICE, BYTE_NIL, e]
}
fn wire_decode(w: Vec<HostWire>) -> Vec<V> {
    match w.as_slice() {
        [HostWire::Bytes(err)] => http_zero(std_errors_new(s(err))),
        [HostWire::Int(status), HostWire::List(headers), HostWire::Bytes(body)] => {
            let headers: Vec<V> = headers
                .iter()
                .map(|h| match h {
                    HostWire::Bytes(b) => s(b),
                    _ => host_fault("HTTP header decode"),
                })
                .collect();
            let n = headers.len() as u32;
            vec![
                V::Int(*status),
                V::Slice(vals(headers).h(), 0, n, n),
                native_slice_bytes(body.clone()),
                V::Nil,
            ]
        }
        _ => host_fault("HTTP wire decode"),
    }
}
struct Capped {
    out: Vec<u8>,
    max: usize,
    over: bool,
}
impl std::io::Write for Capped {
    fn write(&mut self, b: &[u8]) -> std::io::Result<usize> {
        if b.len() > self.max - self.out.len() {
            self.over = true;
            return Err(std::io::Error::other("limit"));
        }
        self.out.extend_from_slice(b);
        Ok(b.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

pub struct HttpTls {
    identity: Option<reqwest::Identity>,
    roots: Vec<reqwest::Certificate>,
    insecure: bool,
    socket: Option<Vec<u8>>,
}

/// Parses DoWith options. Err(false) is an invalid request and Err(true) a
/// credential or root CA that does not parse.
pub fn http_tls(options: &[Vec<u8>]) -> Result<Option<HttpTls>, bool> {
    if options.is_empty() {
        return Ok(None);
    }
    if options.len() % 2 != 0 {
        return Err(false);
    }
    let mut values: Vec<(&[u8], &[u8])> = Vec::new();
    let mut total = 0usize;
    for p in options.chunks(2) {
        total = total.saturating_add(p[0].len() + p[1].len());
        let known = [&b"client-cert"[..], b"client-key", b"root-cas", b"insecure-skip-verify", b"unix-socket"].contains(&p[0].as_slice());
        if total > 1 << 20 || !known || values.iter().any(|(n, _)| *n == p[0].as_slice()) {
            return Err(false);
        }
        values.push((&p[0], &p[1]));
    }
    let get = |name: &[u8]| values.iter().find(|(n, _)| *n == name).map(|(_, v)| *v);
    let skip = get(b"insecure-skip-verify");
    let socket = get(b"unix-socket");
    if get(b"client-cert").is_some() != get(b"client-key").is_some()
        || skip.is_some_and(|v| v != b"true" && v != b"false")
        || socket.is_some_and(|v| v.is_empty() || v.len() > 4096 || v.contains(&0))
        || socket.is_some() && cfg!(not(unix))
    {
        return Err(false);
    }
    let identity = match (get(b"client-cert"), get(b"client-key")) {
        (Some(cert), Some(key)) => {
            let mut pem = cert.to_vec();
            pem.push(b'\n');
            pem.extend_from_slice(key);
            Some(reqwest::Identity::from_pem(&pem).map_err(|_| true)?)
        }
        _ => None,
    };
    let roots = match get(b"root-cas") {
        Some(pem) => {
            let roots = reqwest::Certificate::from_pem_bundle(pem).map_err(|_| true)?;
            if roots.is_empty() {
                return Err(true);
            }
            roots
        }
        None => Vec::new(),
    };
    Ok(Some(HttpTls {
        identity,
        roots,
        insecure: skip == Some(&b"true"[..]),
        socket: socket.map(|v| v.to_vec()),
    }))
}

/// Checks a URL and alternating header slice as Do does.
pub fn http_url_headers(
    url: &V,
    headers: &V,
) -> Result<(reqwest::Url, Vec<(reqwest::header::HeaderName, reqwest::header::HeaderValue)>), String> {
    let raw = url.bytes();
    if raw.len() > 8192 || raw.iter().any(|c| *c <= 32 || *c == 127 || *c == b'\\') {
        return Err("invalid".into());
    };
    let raw = std::str::from_utf8(&raw).map_err(|_| "invalid")?;
    if raw.contains('\u{feff}') {
        return Err("invalid".into());
    };
    let url = reqwest::Url::parse(raw).map_err(|_| "invalid")?;
    if !["http", "https"].contains(&url.scheme())
        || url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.fragment().is_some()
        || url.port() == Some(0)
    {
        return Err("invalid".into());
    }
    let (h, o, n, _, _) = slice_parts(&headers);
    if n % 2 != 0 || n > 32768 {
        return Err("invalid".into());
    };
    let mut flat = Vec::new();
    let mut bounded = 0usize;
    for i in 0..n as usize {
        let value = slot(h, o as usize + i).bytes();
        bounded = bounded.checked_add(value.len() + 2).ok_or("invalid")?;
        if bounded > 65536 {
        return Err("invalid".into());
        };
        flat.push(value.to_vec())
    }
    let mut total = 0usize;
    let mut pairs = Vec::new();
    for p in flat.chunks(2) {
        total = total
        .checked_add(p[0].len() + p[1].len() + 4)
        .ok_or("invalid")?;
        if total > 65536 || p[1].iter().any(|c| *c == 127 || *c < 32 && *c != 9) {
        return Err("invalid".into());
        };
        let name = reqwest::header::HeaderName::from_bytes(&p[0]).map_err(|_| "invalid")?;
        if [
        "host",
        "content-length",
        "transfer-encoding",
        "connection",
        "proxy-authorization",
        "proxy-connection",
        "upgrade",
        "trailer",
        "te",
        ]
        .contains(&name.as_str())
        {
        return Err("invalid".into());
        };
        let value = reqwest::header::HeaderValue::from_bytes(&p[1]).map_err(|_| "invalid")?;
        pairs.push((name, value))
    }
    Ok((url, pairs))
}

/// A dedicated client with DoWith transport options; Err when they do not build.
pub fn http_client(tls: Option<HttpTls>, timeout: Option<Duration>) -> Result<reqwest::Client, ()> {
    let mut builder = reqwest::Client::builder()
        .http1_only()
        .http1_max_headers(1024)
        .redirect(reqwest::redirect::Policy::none())
        .retry(reqwest::retry::never())
        .pool_max_idle_per_host(0);
    if let Some(timeout) = timeout {
        builder = builder.timeout(timeout);
    }
    let configured = tls.is_some();
    if let Some(tls) = tls {
        if let Some(identity) = tls.identity {
            builder = builder.identity(identity)
        }
        if !tls.roots.is_empty() {
            builder = builder.tls_certs_merge(tls.roots)
        }
        builder = builder.tls_danger_accept_invalid_certs(tls.insecure);
        #[cfg(unix)]
        if let Some(socket) = tls.socket {
            use std::os::unix::ffi::OsStringExt;
            builder = builder.unix_socket(std::path::PathBuf::from(std::ffi::OsString::from_vec(socket)))
        }
    }
    match builder.build() {
        Ok(client) => Ok(client),
        Err(_) if configured => Err(()),
        Err(e) => host_fault(&format!("HTTP client acquisition: {e}")),
    }
}

pub fn lib_http_do(
    t: &Rc<Task>,
    context: V,
    method: V,
    url: V,
    headers: V,
    input: V,
    max: V,
    timeout: V,
) {
    http_do(t, context, method, url, headers, input, max, timeout, Vec::new())
}

#[allow(clippy::too_many_arguments)]
pub fn http_do(
    t: &Rc<Task>,
    context: V,
    method: V,
    url: V,
    headers: V,
    input: V,
    max: V,
    timeout: V,
    options: Vec<Vec<u8>>,
) {
    let invalid = || std_errors_new(s(b"http: invalid request or limit"));
    if context.is_nil() {
        t.set_rv(http_zero(invalid()));
        return;
    }
    check_context(&context);
    let error = std_context_context_err(context.clone());
    if !error.is_nil() {
        t.set_rv(http_zero(error));
        return;
    }
    let boundary = host_boundary(context.clone(), Some(timeout.i().saturating_mul(1_000_000)));
    let snapshot = (|| -> Result<_, String> {
        let method = std::str::from_utf8(&method.bytes())
            .map_err(|_| "invalid")?
            .to_owned();
        let (url, mut pairs) = http_url_headers(&url, &headers)?;
        if !["GET", "POST"].contains(&method.as_str())
            || !(0..=NATIVE_MAX as i64).contains(&max.i())
            || !(1..=300000).contains(&timeout.i())
        {
            return Err("invalid".into());
        }
        let body = native_bytes(&input)?;
        if method == "GET" && !body.is_empty() {
            return Err("invalid".into());
        }
        let mut automatic = true;
        for key in ["accept-encoding", "range"] {
            if let Some((_, v)) = pairs.iter().find(|(n, _)| n.as_str() == key) {
                if !v.is_empty() {
                    automatic = false
                }
            }
        }
        if automatic {
            pairs.retain(|(n, _)| n.as_str() != "accept-encoding");
            pairs.push((
                reqwest::header::ACCEPT_ENCODING,
                reqwest::header::HeaderValue::from_static("gzip"),
            ));
        }
        let tls = match http_tls(&options) {
            Ok(tls) => tls,
            Err(false) => return Err("invalid".into()),
            Err(true) => return Err("tls".into()),
        };
        Ok((method, url, pairs, body, max.i() as usize, automatic, tls))
    })();
    let (method, url, pairs, body, max, automatic, tls) = match snapshot {
        Ok(v) => v,
        Err(e) if e == "tls" => {
            t.set_rv(http_zero(std_errors_new(s(b"http: invalid TLS option"))));
            return;
        }
        Err(_) => {
            t.set_rv(http_zero(invalid()));
            return;
        }
    };
    let remaining = boundary.deadline.unwrap().saturating_sub(clock_now());
    if remaining <= 0 {
        t.set_rv(http_zero(context_deadline_exceeded()));
        return;
    }
    let stopped = Arc::new(AtomicBool::new(false));
    let stop = stopped.clone();
    let token = register_host(
        t,
        boundary,
        vec![context],
        http_zero,
        wire_decode,
        move || stop.store(true, Ordering::Release),
        || {},
    );
    launch_host(token, move || {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap_or_else(|e| host_fault(&format!("HTTP runtime acquisition: {e}")));
        let result=runtime.block_on(async move{
 let client=match http_client(tls,Some(Duration::from_nanos(remaining as u64))){Ok(client)=>client,Err(())=>return Err("http: invalid TLS option")};
 let work=async move{
 let mut request=client.request(if method=="GET"{reqwest::Method::GET}else{reqwest::Method::POST},url);for (n,v) in pairs{request=request.header(n,v)}
 let mut response=request.body(body).send().await.map_err(|_|"http: transport failure")?;
 let code=response.status().as_u16();let bodyless=code==204||code==304||(100..200).contains(&code)||response.headers().get(reqwest::header::CONTENT_LENGTH).is_some_and(|v|v.as_bytes()==b"0");
 let decoded=automatic&&!bodyless&&response.headers().get(reqwest::header::CONTENT_ENCODING).is_some_and(|v|v.as_bytes().eq_ignore_ascii_case(b"gzip"));
 let mut total=0usize;let mut headers=Vec::new();let mut names:Vec<_>=response.headers().keys().collect();names.sort_by(|a,b|a.as_str().cmp(b.as_str()));for name in names{if decoded&&(name==reqwest::header::CONTENT_ENCODING||name==reqwest::header::CONTENT_LENGTH){continue}let canonical=name.as_str().split('-').map(|s|{let mut s=s.to_owned();s[0..1].make_ascii_uppercase();s}).collect::<Vec<_>>().join("-");for value in response.headers().get_all(name){total+=canonical.len()+value.as_bytes().len()+4;if total>65536{return Err("http: response headers exceed limit")};headers.push(HostWire::Bytes(canonical.as_bytes().to_vec()));headers.push(HostWire::Bytes(value.as_bytes().to_vec()))}}
 let status=response.status().as_u16() as i64;let mut output=Vec::new();
 if decoded{
  let mut gz=flate2::write::MultiGzDecoder::new(Capped{out:Vec::new(),max,over:false});let mut any=false;
  while let Some(chunk)=response.chunk().await.map_err(|_|"http: response body failure")?{any=true;if gz.write_all(&chunk).is_err(){return Err(if gz.get_ref().over{"http: response body exceeds limit"}else{"http: transport failure"})}}
  if any&&gz.try_finish().is_err(){return Err(if gz.get_ref().over{"http: response body exceeds limit"}else{"http: transport failure"})}
  output=std::mem::take(&mut gz.get_mut().out);
 }else{while let Some(chunk)=response.chunk().await.map_err(|_|"http: response body failure")?{if chunk.len()>max.saturating_sub(output.len()){return Err("http: response body exceeds limit")};output.extend_from_slice(&chunk)}}
 drop(response);drop(client);Ok(vec![HostWire::Int(status),HostWire::List(headers),HostWire::Bytes(output)])};
 tokio::pin!(work);loop{tokio::select!{result=&mut work=>break result,_=tokio::time::sleep(Duration::from_millis(2))=>{if stopped.load(Ordering::Acquire){break Err("http: canceled")}}}}
 });
        drop(runtime);
        match result {
            Ok(w) => w,
            Err(e) => vec![HostWire::Bytes(e.as_bytes().to_vec())],
        }
    });
}
