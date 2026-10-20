//! One serialized, non-reentrant entry owns all runtime state. No OS/TLS.
//! The gate also protects the non-Send Rc graph; no runtime values escape it.
use super::*;
use core::cell::UnsafeCell;
use core::sync::atomic::{AtomicBool, Ordering};

static ACTIVE: AtomicBool = AtomicBool::new(false);

/// Storage accessed only while the private entry gate is held. Initializers
/// never recursively access the same Local; reset runs after all frames drop.
pub(crate) struct Local<T> {
    value: UnsafeCell<Option<T>>,
    init: fn() -> T,
}
// SAFETY: generated public APIs acquire ACTIVE before any runtime access and
// release all runtime values before releasing it. Local is crate-private.
unsafe impl<T> Sync for Local<T> {}
impl<T> Local<T> {
    pub const fn new(init: fn() -> T) -> Self {
        Self {
            value: UnsafeCell::new(None),
            init,
        }
    }
    pub fn with<R>(&self, f: impl FnOnce(&T) -> R) -> R {
        // SAFETY: exclusive entry ownership. Do not hold a mutable reference
        // across f: runtime operations may reenter the same RefCell storage.
        unsafe {
            if (*self.value.get()).is_none() {
                *self.value.get() = Some((self.init)());
            }
            f((*self.value.get()).as_ref().unwrap())
        }
    }
    pub fn reset(&self) {
        // SAFETY: only the entry guard resets, after source frames unwind/drop.
        unsafe {
            *self.value.get() = None;
        }
    }
}

macro_rules! thread_local {
    ($(#[$attr:meta])* $vis:vis static $name:ident: $ty:ty = const $init:block;) => {
        $(#[$attr])* $vis static $name: super::Local<$ty> = super::Local::new(|| $init);
    };
    ($( $(#[$attr:meta])* $vis:vis static $name:ident: $ty:ty = $init:expr; )*) => {
        $( $(#[$attr])* $vis static $name: super::Local<$ty> = super::Local::new(|| $init); )*
    };
}

/// The embedding application supplies the allocator and panic handler.
#[derive(Clone, Copy)]
pub struct RuntimeOptions {
    /// Minimum allocation units between collections; scales with live objects.
    /// Byte buffers add one unit per KiB.
    /// One collects at every safepoint. This is not a hard byte limit.
    pub gc_threshold: usize,
    /// Maximum nested source calls; tune for the application's actual stack.
    pub max_call_depth: usize,
    /// Receives Go print/println bytes. None discards output without buffering.
    pub output: Option<fn(&[u8])>,
}
impl Default for RuntimeOptions {
    fn default() -> Self {
        Self {
            gc_threshold: 256,
            max_call_depth: 64,
            output: None,
        }
    }
}
#[derive(Clone, Copy, Debug, Default)]
pub struct HeapStats {
    pub peak_objects: usize,
    pub live_objects: usize,
    pub collections: usize,
}
#[derive(Debug)]
pub struct CallResult<T> {
    pub value: T,
    pub heap: HeapStats,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum NoStdError {
    Busy,
    Source(Vec<u8>),
}

static OPTIONS: Local<RuntimeOptions> = Local::new(RuntimeOptions::default);
pub fn gc_threshold() -> usize {
    OPTIONS.with(|o| o.gc_threshold.max(1))
}
pub fn max_depth() -> usize {
    OPTIONS.with(|o| o.max_call_depth.max(1))
}
pub fn output(bytes: &[u8]) {
    OPTIONS.with(|o| {
        if let Some(write) = o.output {
            write(bytes);
        }
    });
}

struct EntryGuard;
impl Drop for EntryGuard {
    fn drop(&mut self) {
        reset_program();
        reset_heap();
        OPTIONS.reset();
        ACTIVE.store(false, Ordering::Release);
    }
}

pub fn invoke<T>(
    options: RuntimeOptions,
    globals: usize,
    zero: fn(),
    body: impl FnOnce() -> Result<T, NoStdError>,
) -> Result<CallResult<T>, NoStdError> {
    if ACTIVE
        .compare_exchange(false, true, Ordering::Acquire, Ordering::Relaxed)
        .is_err()
    {
        return Err(NoStdError::Busy);
    }
    let _guard = EntryGuard;
    // SAFETY: gate acquired; no references to the previous options survive.
    unsafe {
        *OPTIONS.value.get() = Some(options);
    }
    init_globals(globals);
    zero();
    let value = body()?;
    collect();
    let (peak_objects, collections) = heap_stats();
    let heap = HeapStats {
        peak_objects,
        live_objects: heap_live(),
        collections,
    };
    Ok(CallResult { value, heap })
}

pub fn library_length(n: usize) -> u32 {
    u32::try_from(n).unwrap_or_else(|_| library_invalid("native value too large"))
}
pub fn library_invalid(message: &str) -> ! {
    fault(message)
}
pub fn library_bytes(bytes: Vec<u8>) -> V {
    let n = library_length(bytes.len());
    V::ByteSlice(alloc(Obj::Bytes(bytes)), 0, n, n)
}
pub fn library_byte_output(v: &V) -> Vec<u8> {
    let (h, o, n, _, _) = slice_parts(v);
    if n == 0 {
        Vec::new()
    } else {
        byte_snapshot(h, o as usize, n as usize)
    }
}
