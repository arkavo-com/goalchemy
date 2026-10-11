package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
)

func TestGeneratedRustSourceStreams(t *testing.T) {
	res := buildSourceStreams(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("rust", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	project := fmt.Sprintf("[package]\nname=\"sourcestreams-consumer\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[dependencies]\ngoalchemy-generated={path=%q}\n[[bin]]\nname=\"sourcestreams-consumer\"\npath=\"main.rs\"\n", out)
	for file, data := range map[string]string{"Cargo.toml": project, "main.rs": rustSourceStreamsConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cargo", "run", "--release", "--offline", "--quiet")
	cmd.Dir = consumer
	cmd.Env = append(driver.ToolEnv(), "CARGO_TARGET_DIR="+filepath.Join(root, "out/rust-tdf-library/sdk/target"), fmt.Sprintf("THROUGHPUT_MIB=%d", throughputMiB(256)))
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source streams consumer: %v\n%s", err, data)
	}
	t.Logf("%s", data)
}

const rustSourceStreamsConsumer = `use goalchemy_generated::*;
use std::io::{Read, Seek, SeekFrom, Write};
fn get<T>(op: Operation<T>) -> T { op.wait().unwrap() }
fn opts() -> CallOptions { CallOptions::default() }
fn pattern(n: usize, from: usize) -> Vec<u8> { (0..n).map(|i| ((from + i) % 251) as u8).collect() }
fn lib_kind(e: &std::io::Error) -> Option<ErrorKind> { e.get_ref().and_then(|x| x.downcast_ref::<LibraryError>()).map(|x| x.kind) }
fn rss_mib() -> u64 {
    let s = std::fs::read_to_string("/proc/self/status").unwrap();
    s.lines().find(|l| l.starts_with("VmRSS:")).unwrap().split_whitespace().nth(1).unwrap().parse::<u64>().unwrap() >> 10
}
struct Source { left: u64, at: u64, block: Vec<u8> }
impl Read for Source {
    fn read(&mut self, b: &mut [u8]) -> std::io::Result<usize> {
        let n = (b.len() as u64).min(self.left).min(128 << 10) as usize;
        let off = (self.at % 251) as usize;
        b[..n].copy_from_slice(&self.block[off..off + n]);
        self.left -= n as u64; self.at += n as u64;
        Ok(n)
    }
}
fn fnv(h: &mut u32, b: &[u8]) { for x in b { *h = (*h ^ *x as u32).wrapping_mul(16777619); } }
fn main() {
    let mut r = get(Pattern(3 << 20, opts())).unwrap();
    assert!(r.can_read() && !r.can_write() && !r.can_seek(), "kind");
    assert_eq!(r.write(&[1]).unwrap_err().kind(), std::io::ErrorKind::Unsupported, "write on a reader");
    let mut all = Vec::new();
    r.read_to_end(&mut all).unwrap();
    assert!(all == pattern(3 << 20, 0), "read_to_end");
    assert_eq!(r.read(&mut [0; 4]).unwrap(), 0, "EOF");
    r.close().wait().unwrap();

    let r = get(Pattern(1000, opts())).unwrap();
    let mut out = Vec::new();
    std::io::copy(&mut r.clone(), &mut out).unwrap();
    assert!(out == pattern(1000, 0), "io::copy");
    r.close().wait().unwrap();

    let mut rs = get(Text(b"hello, world".to_vec(), opts())).unwrap();
    assert_eq!(rs.seek(SeekFrom::Start(7)).unwrap(), 7, "seek");
    let mut rest = Vec::new();
    rs.read_to_end(&mut rest).unwrap();
    assert!(rest == b"world", "read after seek");
    assert_eq!(rs.seek(SeekFrom::End(-5)).unwrap(), 7, "seek from end");
    rs.close().wait().unwrap();

    let mut f = get(Failing(10, opts())).unwrap();
    assert_eq!(f.read(&mut [0; 64]).unwrap(), 10, "data before the error");
    assert_eq!(lib_kind(&f.read(&mut [0; 64]).unwrap_err()), Some(ErrorKind::Source), "source error");
    f.close().wait().unwrap();

    assert!(get(Nil(opts())).is_none(), "nil reader");

    let big = pattern(3 << 20, 0);
    let mut rc = get(Through(Some(Stream::reader(std::io::Cursor::new(big.clone()))), opts())).unwrap();
    let mut back = Vec::new();
    rc.read_to_end(&mut back).unwrap();
    assert!(back == big, "host to source to host");
    rc.close().wait().unwrap();
    assert_eq!(lib_kind(&rc.read(&mut [0; 4]).unwrap_err()), Some(ErrorKind::Closed), "read after close");
    rc.close().wait().unwrap();

    let sink = get(NewSink(true, opts())).unwrap();
    let mut w = get(sink.Writer(opts())).unwrap();
    assert!(w.can_write() && !w.can_read(), "writer kind");
    let data = pattern(5 << 20, 0);
    w.write_all(&data).unwrap();
    w.close().wait().unwrap();
    assert_eq!(get(sink.Closes(opts())), 1, "writer close runs the source Close");
    assert_eq!(lib_kind(&w.write(&[1]).unwrap_err()), Some(ErrorKind::Closed), "write after close");
    sink.io().write_all(b"tail").unwrap();
    assert_eq!(get(sink.Count(opts())), data.len() as i64 + 4, "count");
    let mut contents = get(sink.Contents(opts())).unwrap();
    let mut got = Vec::new();
    contents.read_to_end(&mut got).unwrap();
    assert!(got[..data.len()] == data[..] && &got[data.len()..] == b"tail", "contents");
    contents.close().wait().unwrap();
    sink.close().wait().unwrap();

    let doc = get(Open(b"persistent document".to_vec(), opts())).unwrap();
    let mut view = doc.io();
    assert_eq!(view.seek(SeekFrom::Start(11)).unwrap(), 11, "view seek");
    let mut rest = Vec::new();
    view.read_to_end(&mut rest).unwrap();
    assert!(rest == b"document", "view read");
    assert!(get(doc.Reads(opts())) >= 2, "reads");
    view.close().wait().unwrap();
    doc.close().wait().unwrap();
    assert_eq!(doc.Reads(opts()).wait().unwrap_err().kind, ErrorKind::Closed, "handle closed by its view");

    let n = 1usize << 20;
    let r = get(Pattern(n as i64, opts())).unwrap();
    let total = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let threads: Vec<_> = (0..8).map(|_| {
        let mut r = r.clone();
        let total = total.clone();
        std::thread::spawn(move || {
            let mut buf = [0u8; 4096];
            loop {
                let k = r.read(&mut buf).unwrap();
                if k == 0 { return; }
                total.fetch_add(k, std::sync::atomic::Ordering::SeqCst);
            }
        })
    }).collect();
    for t in threads { t.join().unwrap(); }
    assert_eq!(total.load(std::sync::atomic::Ordering::SeqCst), n, "concurrent reads");
    r.close().wait().unwrap();

    let mib: u64 = std::env::var("THROUGHPUT_MIB").ok().and_then(|s| s.parse().ok()).unwrap_or(256);
    let size = mib << 20;
    let block = pattern(251 * 1024, 0);
    let start = std::time::Instant::now();
    let mut r = get(Through(Some(Stream::reader(Source { left: size, at: 0, block: block.clone() })), opts())).unwrap();
    let (base, mut peak) = (rss_mib(), 0);
    let (mut h, mut count) = (2166136261u32, 0u64);
    let mut buf = vec![0u8; 1 << 20];
    loop {
        let k = r.read(&mut buf).unwrap();
        if k == 0 { break; }
        fnv(&mut h, &buf[..k]);
        if count >> 24 != (count + k as u64) >> 24 { peak = peak.max(rss_mib()); }
        count += k as u64;
    }
    r.close().wait().unwrap();
    let seconds = start.elapsed().as_secs_f64();
    let mut want = 2166136261u32;
    let mut src = Source { left: size, at: 0, block };
    let mut b2 = vec![0u8; 64 << 10];
    loop { let k = src.read(&mut b2).unwrap(); if k == 0 { break; } fnv(&mut want, &b2[..k]); }
    assert!(count == size && h == want, "throughput copy");
    assert!(peak < base + 256, "rss grew from {base} to {peak} MiB");
    println!("throughput: {mib} MiB in {seconds:.1}s ({:.1} MiB/s), rss {base} MiB at start, peak {peak} MiB", mib as f64 / seconds);
    println!("PASS source streams");
}
`
