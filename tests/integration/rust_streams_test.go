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

func TestGeneratedRustStreams(t *testing.T) {
	res := buildStreams(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("rust", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	project := fmt.Sprintf("[package]\nname=\"streams-consumer\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[dependencies]\ngoalchemy-generated={path=%q}\n[[bin]]\nname=\"streams-consumer\"\npath=\"main.rs\"\n", out)
	for file, data := range map[string]string{"Cargo.toml": project, "main.rs": rustStreamsConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cargo", "run", "--release", "--offline", "--quiet")
	cmd.Dir = consumer
	cmd.Env = append(driver.ToolEnv(), "CARGO_TARGET_DIR="+filepath.Join(root, "out/rust-tdf-library/sdk/target"))
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streams consumer: %v\n%s", err, data)
	}
}

const rustStreamsConsumer = `use goalchemy_generated::*;
use std::io::{Cursor, Read, Write};
use std::sync::{Arc, Mutex};
fn get<T>(op: Operation<T>) -> T { op.wait().unwrap() }
fn kind<T>(op: Operation<T>) -> Option<ErrorKind> { op.wait().err().map(|e| e.kind) }
fn opts() -> CallOptions { CallOptions::default() }
#[derive(Clone, Default)]
struct Sink(Arc<Mutex<Vec<u8>>>);
impl Write for Sink {
    fn write(&mut self, b: &[u8]) -> std::io::Result<usize> { self.0.lock().unwrap().extend_from_slice(b); Ok(b.len()) }
    fn flush(&mut self) -> std::io::Result<()> { Ok(()) }
}
struct Failing;
impl Read for Failing { fn read(&mut self, _: &mut [u8]) -> std::io::Result<usize> { Err(std::io::Error::other("disk on fire")) } }
struct Reentrant;
impl Read for Reentrant {
    fn read(&mut self, _: &mut [u8]) -> std::io::Result<usize> {
        match Sum(Some(Stream::bytes(vec![1])), opts()).wait() {
            Err(e) => Err(std::io::Error::other(format!("{:?}", e.kind))),
            Ok(_) => Ok(0),
        }
    }
}
fn main() {
    let big: Vec<u8> = (0..3 << 20).map(|i| (i % 251) as u8).collect();
    let sink = Sink::default();
    assert_eq!(get(Copy(Some(Stream::writer(sink.clone())), Some(Stream::reader(Cursor::new(big.clone()))), opts())), big.len() as i64);
    assert!(*sink.0.lock().unwrap() == big, "chunked copy");
    assert_eq!(get(Sum(Some(Stream::bytes(vec![1, 2, 3])), opts())), 6);
    assert_eq!(get(Section(Some(Stream::bytes(b"hello, world".to_vec())), 7, 5, opts())), b"world".to_vec());
    assert_eq!(kind(Section(Some(Stream::bytes(b"short".to_vec())), 3, 5, opts())), Some(ErrorKind::Source));
    let closes = Arc::new(Mutex::new(0));
    let c = closes.clone();
    assert_eq!(get(Drain(Some(Stream::reader(Cursor::new(b"abc".to_vec())).with_close(move || { *c.lock().unwrap() += 1; Ok(()) })), opts())), 3);
    assert_eq!(*closes.lock().unwrap(), 1, "close");
    assert!(get(AtEOF(Some(Stream::bytes(vec![])), opts())), "host EOF is io.EOF");
    assert_eq!(get(FirstError(Some(Stream::reader(Failing)), opts())), b"disk on fire".to_vec());
    assert!(get(Nil(None, opts())), "nil stream");
    let reentrant = get(FirstError(Some(Stream::reader(Reentrant)), opts()));
    assert!(String::from_utf8_lossy(&reentrant).contains("Reentrant"), "reentrant host stream: {:?}", String::from_utf8_lossy(&reentrant));
    assert_eq!(kind(Sum(Some(Stream::writer(Sink::default())), opts())), Some(ErrorKind::InvalidArgument));
    let doc = get(Open(Some(Stream::bytes(b"persistent document".to_vec())), opts())).unwrap();
    assert_eq!(get(doc.Size(opts())), 19);
    assert_eq!(get(doc.ReadAt(11, 8, opts())), b"document".to_vec(), "stream kept by the instance");
    let whole = Sink::default();
    assert_eq!(get(doc.WriteTo(Some(Stream::writer(whole.clone())), opts())), 19);
    assert_eq!(*whole.0.lock().unwrap(), b"persistent document".to_vec());
    get(doc.close());
    println!("PASS streams");
}
`
