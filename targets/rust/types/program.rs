//! Program entry, globals, and unrecovered-panic reports.
use super::*;
#[cfg(not(goalchemy_nostd))]
use std::sync::atomic::{AtomicBool, Ordering};
#[cfg(not(goalchemy_nostd))]
static EXECUTABLE_ENTRY: AtomicBool = AtomicBool::new(false);
#[cfg(not(goalchemy_nostd))]
pub struct EntryReservation;
#[cfg(not(goalchemy_nostd))]
impl Drop for EntryReservation { fn drop(&mut self) { EXECUTABLE_ENTRY.store(false, Ordering::Release); } }
#[cfg(not(goalchemy_nostd))]
pub fn reserve_entry() -> Result<EntryReservation, HostFault> {
    if EXECUTABLE_ENTRY.compare_exchange(false, true, Ordering::Acquire, Ordering::Relaxed).is_err() {
        return Err(HostFault("overlapping executable entry/reset".into()));
    }
    Ok(EntryReservation)
}
thread_local! { static SOURCE_DEPTH: Cell<usize> = Cell::new(0); }
pub struct SourceGuard;
impl Drop for SourceGuard { fn drop(&mut self) { SOURCE_DEPTH.with(|d| d.set(d.get() - 1)); } }
/// A bounded generated native stack, not recovery from native stack exhaustion.
/// Ordinary host calls are capped at 128; virtual conformance keeps its prior
/// large-stack policy. Cooperative chains are separately bounded by call().
pub fn source_guard() -> SourceGuard {
    SOURCE_DEPTH.with(|d| {
        #[cfg(not(goalchemy_nostd))]
        let host = SCHED.with(|s| s.borrow().epoch.is_some());
        #[cfg(not(goalchemy_nostd))]
        if host && d.get() >= 128 { std::panic::resume_unwind(Box::new(SourceStackFatal)); }
        #[cfg(goalchemy_nostd)]
        if d.get() >= super::owner::max_depth() { fault("source call depth limit exceeded"); }
        d.set(d.get() + 1);
    });
    SourceGuard
}


thread_local! {
    static GLOBALS: RefCell<Option<Fr>> = RefCell::new(None);
}

/// Allocates the program's global variables as a permanent root.
pub fn init_globals(n: usize) {
    GLOBALS.with(|g| {
        let mut globals = g.borrow_mut();
        // Each compiler init_zero_globals still zeroes the slots on each entry.
        // Reuse one separately traced allocation, never leak another ROOTS entry.
        if globals.as_ref().map_or(false, |f| f.len() == n) { return }
        *globals = Some(Fr::unrooted(n));
    });
}

#[inline]
pub fn gg(i: usize) -> V {
    GLOBALS.with(|g| g.borrow().as_ref().expect("globals not initialized").g(i))
}

#[inline]
pub fn gs(i: usize, x: V) {
    GLOBALS.with(|g| g.borrow().as_ref().expect("globals not initialized").s(i, x));
}

fn indented(b: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(b.len());
    for &c in b {
        out.push(c);
        if c == b'\n' {
            out.push(b'\t');
        }
    }
    out
}

pub fn format_panic_value(v: &V) -> Vec<u8> {
    let Some((t, x)) = unbox(v) else { return b"nil".to_vec() };
    let _root = temp_root(&[v.clone()]);
    for m in ["Error", "String"] {
        if let Some((_, code)) = t.method(m) {
            return indented(&code(&[], vec![x]).bytes());
        }
    }
    let builtin = !t.name.contains('.');
    let wrap = |inner: Vec<u8>, quote: bool| -> Vec<u8> {
        if builtin {
            return inner;
        }
        let mut b = t.name.as_bytes().to_vec();
        b.extend_from_slice(if quote { b"(\"" } else { b"(" });
        b.extend(inner);
        b.extend_from_slice(if quote { b"\")" } else { b")" });
        b
    };
    match t.basic {
        "string" => wrap(indented(&x.bytes()), true),
        "bool" => wrap(if x.b() { b"true".to_vec() } else { b"false".to_vec() }, false),
        "int" => wrap(x.i().to_string().into_bytes(), false),
        "uint" => wrap((x.i() as u64).to_string().into_bytes(), false),
        "float32" => wrap(float_print(x.f(), 32).into_bytes(), false),
        "float64" => wrap(float_print(x.f(), 64).into_bytes(), false),
        _ => format!("({}) 0xc000000000", t.name).into_bytes(),
    }
}

pub fn format_chain(p: &V) -> Vec<u8> {
    let mut b = Vec::new();
    let prev = panic_prev(p);
    if !prev.is_nil() {
        b.extend(format_chain(&prev));
        b.push(b'\t');
    }
    b.extend_from_slice(b"panic: ");
    b.extend(format_panic_value(&panic_value(p)));
    if panic_recovered(p) {
        b.extend_from_slice(b" [recovered]");
    }
    b.push(b'\n');
    b
}

#[cfg(not(goalchemy_nostd))]
pub fn report_panic(p: &V) -> ! {
    let _root = temp_root(&[p.clone()]);
    out::stderr(&format_chain(p));
    std::process::exit(2)
}

#[cfg(not(goalchemy_nostd))]
fn report_stats() {
    if std::env::var("GOALCHEMY_HEAP_STATS").is_ok() {
        let (peak, n) = heap_stats();
        out::stderr(format!("heap: peak {} live {} collections {}\n", peak, heap_live(), n).as_bytes());
    }
}

/// Runs body on a thread with a large stack, reporting unrecovered panics.
#[cfg(not(goalchemy_nostd))]
pub fn run_large(body: impl FnOnce() + Send + 'static) -> ! {
    std::panic::set_hook(Box::new(|info| {
        if info.payload().is::<GoPanicPayload>() {
            return;
        }
        let msg = info
            .payload()
            .downcast_ref::<String>()
            .cloned()
            .or_else(|| info.payload().downcast_ref::<&str>().map(|s| s.to_string()))
            .unwrap_or_default();
        out::stderr(format!("goalchemy runtime fault: {}\n", msg).as_bytes());
    }));
    let h = std::thread::Builder::new().stack_size(1 << 30).spawn(move || {
        body();
        report_stats();
    });
    match h.map(|h| h.join()) {
        Ok(Ok(())) => std::process::exit(0),
        _ => std::process::exit(101),
    }
}

/// The entry point of a sequential program.
#[cfg(not(goalchemy_nostd))]
pub fn program_main(nglobals: usize, init: fn(), entry: fn()) -> ! {
    let reservation = reserve_entry().unwrap_or_else(|e| host_fault(&e.0));
    run_large(move || {
        let _reservation = reservation;
        init_globals(nglobals);
        init();
        if let Err(p) = catch(entry) {
            report_panic(&p);
        }
    })
}

pub fn trace_globals(out: &mut Vec<V>) {
    GLOBALS.with(|g| { if let Some(f) = g.borrow().as_ref() { f.trace(out); } });
}

#[cfg(goalchemy_nostd)]
pub fn reset_program() {
    GLOBALS.reset();
    SOURCE_DEPTH.reset();
}
