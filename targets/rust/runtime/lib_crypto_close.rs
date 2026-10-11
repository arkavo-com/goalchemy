//! Maintained native OpenSSL capabilities; keys belong to this source owner.
#![cfg(feature = "native")]
use super::*;
use base64::{
    engine::general_purpose::{STANDARD, URL_SAFE_NO_PAD},
    Engine,
};
use openssl::{
    bn::{BigNum, BigNumContext},
    derive::Deriver,
    ec::{EcGroup, EcKey},
    ecdsa::EcdsaSig,
    encrypt::{Decrypter, Encrypter},
    hash::MessageDigest,
    nid::Nid,
    pkey::{Id, PKey, Private, Public},
    rsa::{Padding, Rsa},
    sign::{Signer, Verifier},
    symm::{decrypt_aead, encrypt_aead, Cipher},
};
use std::collections::BTreeMap;
pub const NATIVE_MAX: usize = 64 * 1024 * 1024;
#[derive(Clone)]
enum NativeKey {
    Private(PKey<Private>),
    Public(PKey<Public>),
}
impl NativeKey {
    fn public(&self) -> Result<PKey<Public>, openssl::error::ErrorStack> {
        match self {
            Self::Public(k) => Ok(k.clone()),
            Self::Private(k) => PKey::public_key_from_der(&k.public_key_to_der()?),
        }
    }
    fn private(&self) -> Result<PKey<Private>, String> {
        match self {
            Self::Private(k) => Ok(k.clone()),
            _ => Err(bad()),
        }
    }
}
thread_local! {static KEYS:RefCell<BTreeMap<u64,NativeKey>>=RefCell::new(BTreeMap::new());static NEXT_KEY:Cell<u64>=Cell::new(1);}
thread_local! {static NATIVE_RETIRE: RefCell<Vec<fn()>> = RefCell::new(Vec::new());}
/// Registers a release of other native resources, such as HTTP exchanges,
/// run when the owner retires.
pub fn on_native_retire(release: fn()) {
    NATIVE_RETIRE.with(|r| {
        let mut r = r.borrow_mut();
        if !r.iter().any(|f| std::ptr::fn_addr_eq(*f, release)) {
            r.push(release)
        }
    })
}
pub fn crypto_retire() {
    KEYS.with(|k| k.borrow_mut().clear());
    for release in NATIVE_RETIRE.with(|r| r.borrow().clone()) {
        release()
    }
}
fn own_key(k: NativeKey) -> V {
    let id = NEXT_KEY.with(|n| {
        let id = n.get();
        n.set(
            id.checked_add(1)
                .unwrap_or_else(|| host_fault("key ID exhausted")),
        );
        id
    });
    KEYS.with(|keys| keys.borrow_mut().insert(id, k));
    V::Obj(alloc(Obj::NativeKey(id, sched(|s| s.domain))))
}
fn key_id(v: &V) -> Result<u64, String> {
    if v.is_nil() {
        return Err(bad());
    }
    with(v.h(), |o| match o {
        Obj::NativeKey(id, owner) if *owner == sched(|s| s.domain) => Ok(*id),
        _ => Err("foreign key".into()),
    })
}
fn acquire_key(v: &V) -> Result<NativeKey, String> {
    let id = key_id(v)?;
    KEYS.with(|keys| {
        keys.borrow()
            .get(&id)
            .cloned()
            .ok_or_else(|| "crypto: key is closed".into())
    })
}
pub fn lib_crypto_close(t: &Rc<Task>, v: V) {
    if !v.is_nil() {
        match key_id(&v) {
            Ok(id) => {
                KEYS.with(|keys| keys.borrow_mut().remove(&id));
            }
            Err(e) => host_fault(&e),
        }
    }
    t.set_rv(vec![])
}
fn bad() -> String {
    "crypto: invalid input or key".into()
}
fn native<R>(r: Result<R, openssl::error::ErrorStack>) -> Result<R, String> {
    r.map_err(|_| bad())
}
pub fn native_bytes(v: &V) -> Result<Vec<u8>, String> {
    let (h, o, n, _, bytes) = match v {
        V::ByteSlice(h, o, n, c) => (*h, *o, *n, *c, true),
        V::Slice(h, o, n, c) => (*h, *o, *n, *c, false),
        _ => return Err(bad()),
    };
    if !bytes || n as usize > NATIVE_MAX {
        return Err(bad());
    };
    Ok(if h == 0 {
        vec![]
    } else {
        byte_snapshot(h, o as usize, n as usize)
    })
}
pub fn native_slice_bytes(v: Vec<u8>) -> V {
    let n = v.len() as u32;
    V::ByteSlice(byte_array(v).h(), 0, n, n)
}
pub fn native_strings(v: V) -> Result<Vec<Vec<u8>>, String> {
    let (h, o, n, _, _) = slice_parts(&v);
    (0..n as usize)
        .map(|i| match slot(h, o as usize + i) {
            V::Str(b) => Ok(b.to_vec()),
            _ => Err(bad()),
        })
        .collect()
}
fn strings_value(v: Vec<Vec<u8>>) -> V {
    let n = v.len() as u32;
    V::Slice(vals(v.iter().map(|b| s(b)).collect()).h(), 0, n, n)
}
/// The JWK curve name and fixed coordinate width of a supported EC group.
/// OpenSSL names explicit parameters that resemble a named curve, so the
/// encoding is checked too.
fn curve(group: &openssl::ec::EcGroupRef) -> Result<(&'static str, usize), String> {
    if group.asn1_flag() != openssl::ec::Asn1Flag::NAMED_CURVE {
        return Err(bad());
    }
    match group.curve_name() {
        Some(Nid::X9_62_PRIME256V1) => Ok(("P-256", 32)),
        Some(Nid::SECP384R1) => Ok(("P-384", 48)),
        Some(Nid::SECP521R1) => Ok(("P-521", 66)),
        _ => Err(bad()),
    }
}
fn digest(bits: &str) -> MessageDigest {
    match bits {
        "256" => MessageDigest::sha256(),
        "384" => MessageDigest::sha384(),
        _ => MessageDigest::sha512(),
    }
}
fn check_key(k: NativeKey) -> Result<NativeKey, String> {
    if let NativeKey::Private(private) = &k {
        match private.id() {
            Id::RSA => {
                if !native(native(private.rsa())?.check_key())? {
                    return Err(bad());
                }
            }
            Id::EC => native(native(private.ec_key())?.check_key())?,
            _ => return Err(bad()),
        }
    }
    let public = native(k.public())?;
    match public.id() {
        Id::RSA => {
            let rsa = native(public.rsa())?;
            if rsa.n().is_negative()
                || ![2048, 3072, 4096].contains(&rsa.n().num_bits())
                || rsa.e().is_negative()
                || !rsa.e().is_odd()
                || rsa.e().num_bits() < 2
                || rsa.e().num_bits() > isize::BITS as i32 - 1
            {
                return Err(bad());
            }
        }
        Id::EC => {
            let ec = native(public.ec_key())?;
            curve(ec.group())?;
            native(ec.check_key())?
        }
        _ => return Err(bad()),
    };
    Ok(k)
}
fn der_complete(b: &[u8]) -> bool {
    if b.len() < 2 || b[0] != 0x30 {
        return false;
    }
    let n = b[1] as usize;
    if n < 128 {
        return n + 2 == b.len();
    }
    let c = n & 127;
    if c == 0 || c > 4 || b.len() < 2 + c || b[2] == 0 {
        return false;
    }
    let mut len = 0usize;
    for x in &b[2..2 + c] {
        len = (len << 8) | *x as usize
    }
    len >= 128 && len + 2 + c == b.len()
}
fn import_pem(b: &[u8]) -> Result<NativeKey, String> {
    if b.len() > 65536 {
        return Err(bad());
    }
    let text = std::str::from_utf8(b).map_err(|_| bad())?.trim();
    let first = text.lines().next().ok_or_else(bad)?;
    let label = first
        .strip_prefix("-----BEGIN ")
        .and_then(|s| s.strip_suffix("-----"))
        .ok_or_else(bad)?;
    let end = format!("-----END {label}-----");
    if !text.ends_with(&end) {
        return Err(bad());
    }
    let middle = &text[first.len()..text.len() - end.len()];
    let body = middle.chars().filter(|c| c.is_ascii_whitespace()).count();
    let raw: String = middle
        .chars()
        .filter(|c| !c.is_ascii_whitespace())
        .collect();
    if body + raw.len() != middle.len() {
        return Err(bad());
    }
    let der = STANDARD.decode(raw.as_bytes()).map_err(|_| bad())?;
    if STANDARD.encode(&der) != raw || !der_complete(&der) {
        return Err(bad());
    }
    let k = match label {
        "PUBLIC KEY" => NativeKey::Public(native(PKey::public_key_from_der(&der))?),
        "PRIVATE KEY" => NativeKey::Private(native(PKey::private_key_from_pkcs8(&der))?),
        "RSA PUBLIC KEY" => NativeKey::Public(native(PKey::from_rsa(native(
            Rsa::public_key_from_der_pkcs1(&der),
        )?))?),
        "RSA PRIVATE KEY" => NativeKey::Private(native(PKey::from_rsa(native(
            Rsa::private_key_from_der(&der),
        )?))?),
        "CERTIFICATE" => NativeKey::Public(native(
            native(openssl::x509::X509::from_der(&der))?.public_key(),
        )?),
        _ => return Err(bad()),
    };
    check_key(k)
}
fn hmac(k: &[u8], d: &[u8]) -> Result<Vec<u8>, String> {
    let empty = [0u8; 64];
    let key = native(PKey::hmac(if k.is_empty() { &empty } else { k }))?;
    let mut signer = native(Signer::new(MessageDigest::sha256(), &key))?;
    native(signer.update(d))?;
    native(signer.sign_to_vec())
}
fn crypto(op: &str, a: Vec<V>) -> Result<V, String> {
    let bytes = |i| native_bytes(&a[i]);
    let key = |i| acquire_key(&a[i]);
    match op {
        "random" => {
            let n = a[0].i();
            if !(0..=NATIVE_MAX as i64).contains(&n) {
                return Err(bad());
            }
            let mut out = vec![0; n as usize];
            openssl::rand::rand_bytes(&mut out)
                .unwrap_or_else(|e| host_fault(&format!("native random: {e}")));
            Ok(native_slice_bytes(out))
        }
        "sha256" => Ok(native_slice_bytes(
            openssl::sha::sha256(&bytes(0)?).to_vec(),
        )),
        "hmac_sha256" => Ok(native_slice_bytes(hmac(&bytes(0)?, &bytes(1)?)?)),
        "hmac_sha256_verify" => {
            let mac = bytes(2)?;
            if mac.len() != 32 {
                return Err(bad());
            }
            Ok(V::Bool(openssl::memcmp::eq(
                &mac,
                &hmac(&bytes(0)?, &bytes(1)?)?,
            )))
        }
        "hkdf_sha256" => {
            let n = a[3].i();
            if !(0..=8160).contains(&n) {
                return Err(bad());
            }
            let secret = bytes(0)?;
            let salt = bytes(1)?;
            let info = bytes(2)?;
            let mut ctx = native(openssl::pkey_ctx::PkeyCtx::new_id(Id::HKDF))?;
            native(ctx.derive_init())?;
            native(ctx.set_hkdf_md(openssl::md::Md::sha256()))?;
            native(ctx.set_hkdf_key(&secret))?;
            native(ctx.set_hkdf_salt(&salt))?;
            native(ctx.add_hkdf_info(&info))?;
            let mut out = vec![0; n as usize];
            if n > 0 {
                native(ctx.derive(Some(&mut out)))?;
            }
            Ok(native_slice_bytes(out))
        }
        "aes256_gcm_encrypt" | "aes256_gcm_decrypt" => {
            let k = bytes(0)?;
            let nonce = bytes(1)?;
            let data = if op.ends_with("decrypt") {
                let (h, o, n, _, kind) = slice_parts(&a[2]);
                if !kind || n as usize > NATIVE_MAX + 16 {
                    return Err(bad());
                };
                if h == 0 {
                    vec![]
                } else {
                    byte_snapshot(h, o as usize, n as usize)
                }
            } else {
                bytes(2)?
            };
            let aad = bytes(3)?;
            if k.len() != 32 || nonce.len() != 12 {
                return Err(bad());
            }
            let out = if op.ends_with("encrypt") {
                let mut tag = [0; 16];
                let mut out = native(encrypt_aead(
                    Cipher::aes_256_gcm(),
                    &k,
                    Some(&nonce),
                    &aad,
                    &data,
                    &mut tag,
                ))?;
                out.extend(tag);
                out
            } else {
                if data.len() < 16 {
                    return Err(bad());
                }
                native(decrypt_aead(
                    Cipher::aes_256_gcm(),
                    &k,
                    Some(&nonce),
                    &aad,
                    &data[..data.len() - 16],
                    &data[data.len() - 16..],
                ))?
            };
            Ok(native_slice_bytes(out))
        }
        "generate_rsa2048" | "generate_rsa4096" => Ok(own_key(NativeKey::Private(native(
            PKey::from_rsa(native(Rsa::generate(if op.ends_with("2048") {
                2048
            } else {
                4096
            }))?),
        )?))),
        "generate_p256" | "generate_p384" | "generate_p521" => {
            let group = native(EcGroup::from_curve_name(match op {
                "generate_p256" => Nid::X9_62_PRIME256V1,
                "generate_p384" => Nid::SECP384R1,
                _ => Nid::SECP521R1,
            }))?;
            Ok(own_key(NativeKey::Private(native(PKey::from_ec_key(
                native(EcKey::generate(&group))?,
            ))?)))
        }
        "import_pem" => Ok(own_key(import_pem(&a[0].bytes())?)),
        "public_pem" => Ok(s(&native(native(key(0)?.public())?.public_key_to_pem())?)),
        "private_pem" => Ok(s(&native(key(0)?.private()?.private_key_to_pem_pkcs8())?)),
        "public_jwk" => {
            let p = native(key(0)?.public())?;
            let mut out = vec![vec![]; 6];
            if p.id() == Id::RSA {
                let r = native(p.rsa())?;
                out[0] = b"RSA".to_vec();
                out[2] = URL_SAFE_NO_PAD.encode(r.n().to_vec()).into_bytes();
                out[3] = URL_SAFE_NO_PAD.encode(r.e().to_vec()).into_bytes()
            } else {
                let ec = native(p.ec_key())?;
                let (crv, width) = curve(ec.group())?;
                let mut x = native(BigNum::new())?;
                let mut y = native(BigNum::new())?;
                let mut ctx = native(BigNumContext::new())?;
                native(ec.public_key().affine_coordinates_gfp(
                    ec.group(),
                    &mut x,
                    &mut y,
                    &mut ctx,
                ))?;
                out[0] = b"EC".to_vec();
                out[1] = crv.as_bytes().to_vec();
                out[4] = URL_SAFE_NO_PAD
                    .encode(native(x.to_vec_padded(width as i32))?)
                    .into_bytes();
                out[5] = URL_SAFE_NO_PAD
                    .encode(native(y.to_vec_padded(width as i32))?)
                    .into_bytes()
            };
            Ok(strings_value(out))
        }
        "ecdh" => {
            let p = key(0)?.private()?;
            let q = native(key(1)?.public())?;
            if p.id() != Id::EC || q.id() != Id::EC {
                return Err(bad());
            };
            let (_, width) = curve(native(p.ec_key())?.group())?;
            if curve(native(q.ec_key())?.group())?.1 != width {
                return Err(bad());
            }
            let mut derive = native(Deriver::new(&p))?;
            native(derive.set_peer(&q))?;
            let out = native(derive.derive_to_vec())?;
            if out.len() != width {
                return Err(bad());
            }
            Ok(native_slice_bytes(out))
        }
        "rsa_oaep_encrypt" => {
            let p = native(key(0)?.public())?;
            if p.id() != Id::RSA {
                return Err(bad());
            };
            let data = bytes(1)?;
            let size = native(p.rsa())?.size() as usize;
            if data.len() > size - 42 {
                return Err(bad());
            };
            let mut enc = native(Encrypter::new(&p))?;
            native(enc.set_rsa_padding(Padding::PKCS1_OAEP))?;
            native(enc.set_rsa_oaep_md(MessageDigest::sha1()))?;
            native(enc.set_rsa_mgf1_md(MessageDigest::sha1()))?;
            let mut out = vec![0; size];
            let n = native(enc.encrypt(&data, &mut out))?;
            out.truncate(n);
            Ok(native_slice_bytes(out))
        }
        "rsa_oaep_decrypt" => {
            let p = key(0)?.private()?;
            if p.id() != Id::RSA {
                return Err(bad());
            };
            let data = bytes(1)?;
            let size = native(p.rsa())?.size() as usize;
            if data.len() != size {
                return Err(bad());
            };
            let mut dec = native(Decrypter::new(&p))?;
            native(dec.set_rsa_padding(Padding::PKCS1_OAEP))?;
            native(dec.set_rsa_oaep_md(MessageDigest::sha1()))?;
            native(dec.set_rsa_mgf1_md(MessageDigest::sha1()))?;
            let mut out = vec![0; size];
            let n = native(dec.decrypt(&data, &mut out))?;
            out.truncate(n);
            Ok(native_slice_bytes(out))
        }
        "rs256_sign" | "rs384_sign" | "rs512_sign" | "es256_sign" | "es384_sign" | "es512_sign"
        | "rs256_verify" | "rs384_verify" | "rs512_verify" | "es256_verify" | "es384_verify"
        | "es512_verify" => {
            let rsa = op.starts_with("rs");
            let verify = op.ends_with("verify");
            let public = native(key(0)?.public())?;
            if public.id() != if rsa { Id::RSA } else { Id::EC } {
                return Err(bad());
            };
            let width = if rsa {
                native(public.rsa())?.size() as usize
            } else {
                let (_, n) = curve(native(public.ec_key())?.group())?;
                if n != match &op[2..5] {
                    "256" => 32,
                    "384" => 48,
                    _ => 66,
                } {
                    return Err(bad());
                }
                n
            };
            let md = digest(&op[2..5]);
            if !verify {
                let p = key(0)?.private()?;
                let mut signer = native(Signer::new(md, &p))?;
                if rsa {
                    native(signer.set_rsa_padding(Padding::PKCS1))?
                };
                native(signer.update(&bytes(1)?))?;
                let mut sig = native(signer.sign_to_vec())?;
                if !rsa {
                    let s = native(EcdsaSig::from_der(&sig))?;
                    sig = native(s.r().to_vec_padded(width as i32))?;
                    sig.extend(native(s.s().to_vec_padded(width as i32))?)
                };
                return Ok(native_slice_bytes(sig));
            }
            let mut sig = bytes(2)?;
            if sig.len() != if rsa { width } else { 2 * width } {
                return Err(bad());
            };
            if !rsa {
                sig = native(
                    native(EcdsaSig::from_private_components(
                        native(BigNum::from_slice(&sig[..width]))?,
                        native(BigNum::from_slice(&sig[width..]))?,
                    ))?
                    .to_der(),
                )?
            };
            let mut verifier = native(Verifier::new(md, &public))?;
            if rsa {
                native(verifier.set_rsa_padding(Padding::PKCS1))?
            };
            native(verifier.update(&bytes(1)?))?;
            Ok(V::Bool(verifier.verify(&sig).unwrap_or(false)))
        }
        _ => host_fault("unknown native crypto operation"),
    }
}
pub fn crypto_call(t: &Rc<Task>, op: &str, args: Vec<V>, zero: V) {
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| crypto(op, args)));
    match r {
        Ok(Ok(v)) => t.set_rv(vec![v, V::Nil]),
        Ok(Err(e)) => t.set_rv(vec![zero, std_errors_new(s(e.as_bytes()))]),
        Err(e) => host_fault(&native_fault_message(&e)),
    }
}

pub fn native_encoding(v: V, url: bool, decode: bool) -> V {
    let engine = if url { &URL_SAFE_NO_PAD } else { &STANDARD };
    let r = (|| -> Result<V, String> {
        if decode {
            let b = v.bytes();
            if b.len() > 4 * ((NATIVE_MAX + 2) / 3) {
                return Err(bad());
            };
            let out = engine.decode(&b).map_err(|_| bad())?;
            if out.len() > NATIVE_MAX || engine.encode(&out).as_bytes() != b.as_ref() {
                return Err(bad());
            };
            Ok(native_slice_bytes(out))
        } else {
            Ok(s(engine.encode(native_bytes(&v)?).as_bytes()))
        }
    })();
    match r {
        Ok(v) => tuple(vec![v, V::Nil]),
        Err(e) => tuple(vec![
            if decode { BYTE_NIL } else { s(b"") },
            std_errors_new(s(e.as_bytes())),
        ]),
    }
}
