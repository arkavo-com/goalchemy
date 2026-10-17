package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/subset"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func TestGeneratedRustPackageLibrary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	mod := fmt.Sprintf("module packageconsumer\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", root)
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte(mod), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"api", "state", "order"} {
		data, err := os.ReadFile(filepath.Join(root, "tests/integration/testdata/package_library", pkg, pkg+".go"))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.ReplaceAll(string(data), "github.com/eugenioenko/goalchemy/tests/integration/testdata/package_library/", "packageconsumer/"))
		dir := filepath.Join(source, pkg)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, pkg+".go"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Native provider ACKs and capability method values exercise module-to-root
	// extern wrappers without changing the common library fixture's public API.
	probe := `package api
import("github.com/eugenioenko/goalchemy/lib/context";"github.com/eugenioenko/goalchemy/lib/callback";"github.com/eugenioenko/goalchemy/lib/crypto")
func Hold(ctx context.Context)([]byte,error){return callback.Request(ctx,"hold",nil)}
func Native(ctx context.Context)([]byte,error){k,e:=crypto.GenerateP256();if e!=nil{return nil,e};defer k.Close();public:=k.PublicPEM;p,e:=public();if e!=nil{return nil,e};if len(p)==0{panic("public key")};hash:=crypto.SHA256;return hash([]byte{0,255,128})}
`
	if err := os.WriteFile(filepath.Join(source, "api", "probe.go"), []byte(probe), 0600); err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: filepath.Join(source, "api"), Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			out, consumer := t.TempDir(), t.TempDir()
			if ds := driver.EmitWithOptions("rust", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			// Mirror the SDK's adapter placement exactly; explicit paths must still
			// load the native modules from src, rather than a generated/ directory.
			if err := os.Rename(filepath.Join(out, "src", "lib.rs"), filepath.Join(out, "src", "generated.rs")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "src", "lib.rs"), []byte("mod generated;\npub use generated::*;\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "src", "Caller.rs"), []byte("caller-owned unrelated Rust source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "src", "main.rs"), []byte("unlisted caller executable source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			project := fmt.Sprintf("[package]\nname=\"package-consumer\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[dependencies]\ngoalchemy-generated={path=%q}\n[[bin]]\nname=\"package-consumer\"\npath=\"main.rs\"\n", out)
			for file, data := range map[string]string{"Cargo.toml": project, "main.rs": rustPackageLibraryConsumer} {
				if err := os.WriteFile(filepath.Join(consumer, file), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "cargo", "run", "--release", "--offline", "--quiet")
			cmd.Dir = consumer
			cmd.Env = append(driver.ToolEnv(), "CARGO_TARGET_DIR="+filepath.Join(root, "out/rust-tdf-library/sdk/target"), "GOALCHEMY_GC_THRESHOLD=1")
			build := exec.CommandContext(ctx, "cargo", "build", "--release", "--offline", "--quiet")
			build.Dir, build.Env = out, cmd.Env
			if data, err := build.CombinedOutput(); err != nil {
				t.Fatalf("explicit Cargo library inventory: %v\n%s", err, data)
			}
			if data, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("independent native Cargo library consumer: %v\n%s", err, data)
			} else {
				t.Log(string(data))
			}
		})
	}
}

const rustPackageLibraryConsumer = `use goalchemy_generated::*;
use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc,Barrier,Mutex,mpsc};
use std::sync::atomic::{AtomicUsize,Ordering};
use std::task::{Context,Wake,Waker};
use std::time::Duration;
const ORDER:&[u8]=b"order-var/order-init/state-var/state-init/api-var/api-init";
fn value()->Value{Value{Count:2,Bytes:vec![7],Order:b"caller".to_vec()}}
fn output(v:&Value){assert_eq!(v.Count,29);assert_eq!(v.Bytes,vec![3,255,128]);assert_eq!(v.Order,ORDER);}
struct Noop;impl Wake for Noop{fn wake(self:Arc<Self>){}}
fn pending<T>(op:&mut Operation<T>)->bool{let w=Waker::from(Arc::new(Noop));Pin::new(op).poll(&mut Context::from_waker(&w)).is_pending()}
fn main(){
 let input=value();let mut first=Export(input.clone(),CallOptions::default()).wait().unwrap();output(&first);assert_eq!(input,value());
 let saved=ErrorGlobal(CallOptions::default()).wait().unwrap_err();assert_eq!(saved.kind,ErrorKind::Source);assert_eq!(saved.text("Code"),"saved");
 let start=Arc::new(Barrier::new(13));let mut jobs=Vec::new();
 for _ in 0..12{let barrier=start.clone();jobs.push(std::thread::spawn(move||{barrier.wait();output(&Export(value(),CallOptions::default()).wait().unwrap());}));}
 start.wait();for job in jobs{job.join().unwrap();}
 output(&first);assert_eq!(saved.text("Code"),"saved");assert_eq!(saved.diagnostic,b"saved");
 first.Bytes[0]=99;let mut changed=saved.clone();changed.fields.insert("Code".into(),HostWire::Bytes(b"caller".to_vec()));
 output(&Export(value(),CallOptions::default()).wait().unwrap());assert_eq!(ErrorGlobal(CallOptions::default()).wait().unwrap_err().text("Code"),"saved");assert_eq!(saved.text("Code"),"saved");
 // Native key/function values are wrappers in the parent module, while the
 // source caller remains in its package. Verify SHA256 against a fixed vector.
 let digest=Native(CallOptions::default()).wait().unwrap();assert_eq!(digest,vec![247,66,185,101,241,86,193,3,116,188,35,174,169,110,58,138,255,143,172,214,252,7,157,239,234,163,2,25,173,134,242,17]);
 let (acquired_tx,acquired)=mpsc::channel();let (stopped_tx,stopped)=mpsc::channel();let (ack_tx,ack)=mpsc::channel();let ack=Arc::new(Mutex::new(ack));let released=Arc::new(AtomicUsize::new(0));let finished=released.clone();
 let mut options=CallOptions::default();options.providers.insert("hold".into(),Arc::new(move|request|{
  acquired_tx.send(()).unwrap();while !request.cancellation.is_canceled(){std::thread::sleep(Duration::from_millis(1));}
  stopped_tx.send(()).unwrap();ack.lock().unwrap().recv_timeout(Duration::from_secs(10)).unwrap();finished.fetch_add(1,Ordering::Release);Err(ProviderError::Rejected("stopped".into()))
 }));
 let mut held=Hold(options);acquired.recv_timeout(Duration::from_secs(5)).unwrap();
 let mut queued_input=value();let mut queued=Export(queued_input.clone(),CallOptions::default());queued_input.Count=99;queued_input.Bytes[0]=99;
 let canceled=ErrorGlobal(CallOptions::default());canceled.cancel();assert_eq!(canceled.wait().unwrap_err().kind,ErrorKind::Canceled);assert!(pending(&mut held)&&pending(&mut queued));
 held.cancel();stopped.recv_timeout(Duration::from_secs(5)).unwrap();assert!(pending(&mut held)&&pending(&mut queued));assert_eq!(released.load(Ordering::Acquire),0);
 ack_tx.send(()).unwrap();assert_eq!(held.wait().unwrap_err().kind,ErrorKind::Canceled);assert_eq!(released.load(Ordering::Acquire),1);output(&queued.wait().unwrap());output(&Export(value(),CallOptions::default()).wait().unwrap());
 println!("PASS independent native modules/SDK rename/init/overlap/retained values/errors/cancellation/ACK/native capability wrappers/GC");
}
`
