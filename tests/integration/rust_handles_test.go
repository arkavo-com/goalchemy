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

func TestGeneratedRustHandles(t *testing.T) {
	res := buildHandles(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("rust", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	project := fmt.Sprintf("[package]\nname=\"handles-consumer\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[dependencies]\ngoalchemy-generated={path=%q}\n[[bin]]\nname=\"handles-consumer\"\npath=\"main.rs\"\n", out)
	for file, data := range map[string]string{"Cargo.toml": project, "main.rs": rustHandlesConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cargo", "run", "--release", "--offline", "--quiet")
	cmd.Dir = consumer
	cmd.Env = append(driver.ToolEnv(), "CARGO_TARGET_DIR="+filepath.Join(root, "out/rust-tdf-library/sdk/target"), "GOALCHEMY_GC_THRESHOLD=1")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("handles consumer: %v\n%s", err, data)
	}
}

const rustHandlesConsumer = `use goalchemy_generated::*;
use std::sync::{Arc,Mutex};
use std::time::Duration;
fn get<T>(op:Operation<T>)->T{op.wait().unwrap()}
fn kind<T>(op:Operation<T>)->Option<ErrorKind>{op.wait().err().map(|e|e.kind)}
fn opts()->CallOptions{CallOptions::default()}
fn main(){
 // Instance state, init once, sentinel identity, native key and identity.
 let c=get(New(b"a".to_vec(),opts())).unwrap();
 for i in 1..=3{assert_eq!(get(c.Add(2,opts())),2*i,"persistent field");}
 assert_eq!(kind(c.Add(-1,opts())),Some(ErrorKind::Source));
 let s=get(c.Snap(opts()));
 assert!(s.Name==b"a".to_vec()&&s.N==6&&s.Inits==1&&s.Created==1&&s.LastNeg,"instance globals {:?}",s);
 assert!(get(c.PublicKey(opts())).starts_with(b"-----BEGIN PUBLIC KEY-----"),"native key survives its creating call");
 for _ in 0..3{assert_eq!(get(Inits(opts())),1,"free functions keep fresh globals");}
 assert_eq!(get(c.Self_(opts())),Some(c.clone()),"identity");
 let child=get(c.Child(opts())).unwrap();
 assert_eq!(get(child.Bump(opts())),7);assert_eq!(get(child.Parent(opts())),Some(c.clone()));assert_eq!(get(c.Snap(opts())).N,7);
 get(child.close());
 assert_eq!(kind(child.Bump(opts())),Some(ErrorKind::Closed));
 assert_eq!(get(c.Add(1,opts())),8,"closing a derived handle keeps the instance");
 assert_eq!(get(Nil(opts())),None);
 get(c.close());
 // Handle parameters.
 let a=get(New(b"a".to_vec(),opts())).unwrap();let b=get(a.Sibling(b"b".to_vec(),opts())).unwrap();let other=get(New(b"other".to_vec(),opts())).unwrap();
 get(a.Add(2,opts()));get(b.Add(3,opts()));
 assert_eq!(get(Sum(Some(&a),Some(&b),opts())),5);
 let s=get(b.Snap(opts()));assert!(s.Created==2&&s.Inits==1,"sibling joins the instance");
 assert_eq!(kind(Sum(Some(&a),Some(&other),opts())),Some(ErrorKind::InstanceMismatch));
 assert_eq!(get(Sum(None,None,opts())),-1);
 get(a.close());get(b.close());get(other.close());
 // Value receivers run on a copy of the handle's object.
 let c=get(New(b"a".to_vec(),opts())).unwrap();
 assert_eq!(get(c.Name(opts())),b"a".to_vec(),"value receiver on a handle with private state");
 get(c.close());
 let p=get(NewCoord(1,2,opts())).unwrap();
 assert_eq!((get(p.Total(opts())),get(p.Shift(5,opts())),get(p.Total(opts()))),(3,6,3),"value receiver must not change the handle");
 let m=get(p.Moved(10,opts()));
 assert!(m.X==11&&m.Y==2&&get(p.Total(opts()))==3,"value result is a copy {:?}",m);
 assert!(get(p.Less(m,opts()))&&!get(p.Less(get(p.Moved(-1,opts())),opts())),"value parameter of the handle type");
 assert_eq!((get(p.Move(1,opts())),get(p.Total(opts()))),(2,4),"pointer receiver changes the handle");
 get(p.close());
 assert_eq!(kind(get(NewCoord(-1,0,opts())).unwrap().close()),Some(ErrorKind::Source),"value receiver Close");
 // Calls from several host threads are serialized; instances are independent.
 let x=get(New(b"x".to_vec(),opts())).unwrap();let y=get(New(b"y".to_vec(),opts())).unwrap();
 let threads:Vec<_>=(0..8).map(|i|{let h=if i%2==0{x.clone()}else{y.clone()};std::thread::spawn(move||{for _ in 0..50{get(h.Add(1,opts()));}})}).collect();
 for t in threads{t.join().unwrap();}
 let (sx,sy)=(get(x.Snap(opts())),get(y.Snap(opts())));
 assert!(sx.N==200&&sy.N==200&&sx.Created==1&&sy.Created==1,"serialized calls");
 get(x.close());get(y.close());
 // Close.
 let c=get(New(b"a".to_vec(),opts())).unwrap();let sib=get(c.Sibling(b"bad-close".to_vec(),opts())).unwrap();
 get(c.close());get(c.close());
 assert_eq!(kind(c.Add(1,opts())),Some(ErrorKind::Closed));
 assert_eq!(get(sib.Snap(opts())).Closes,1,"source Close ran once");
 assert_eq!(kind(sib.close()),Some(ErrorKind::Source));
 assert_eq!(kind(sib.Snap(opts())),Some(ErrorKind::Closed));
 // Cancellation and Close while pending.
 let c=get(New(b"a".to_vec(),opts())).unwrap();
 let op=c.Wait(100000,opts());std::thread::sleep(Duration::from_millis(100));op.cancel();
 assert_eq!(kind(op),Some(ErrorKind::Canceled));
 assert_eq!(get(c.Add(1,opts())),1,"usable after cancellation");
 let waited=c.Wait(100000,opts());std::thread::sleep(Duration::from_millis(100));
 let queued=c.Add(1,opts());std::thread::sleep(Duration::from_millis(50));
 get(c.close());
 assert_eq!(kind(waited),Some(ErrorKind::Canceled));
 assert_eq!(kind(queued),Some(ErrorKind::Closed));
 // Poisoning.
 let c=get(New(b"a".to_vec(),opts())).unwrap();
 assert_eq!(kind(c.Boom(opts())),Some(ErrorKind::SourcePanic));
 assert_eq!(kind(c.Add(1,opts())),Some(ErrorKind::Poisoned));
 get(c.close());
 assert_eq!(kind(c.Add(1,opts())),Some(ErrorKind::Closed));
 // Warnings: abandoned goroutines and unclosed handles.
 let warnings=Arc::new(Mutex::new(Vec::<String>::new()));let sink=warnings.clone();
 set_library_warn(Some(Arc::new(move|m:&str|sink.lock().unwrap().push(m.to_string()))));
 let c=get(New(b"a".to_vec(),opts())).unwrap();
 get(c.Leak(opts()));
 assert!(warnings.lock().unwrap().iter().any(|m|m.contains("goroutine")),"abandoned goroutine warning");
 assert_eq!(get(c.Add(1,opts())),1);
 drop(c);
 for _ in 0..200{if warnings.lock().unwrap().iter().any(|m|m.contains("Counter handle was not closed")){break}std::thread::sleep(Duration::from_millis(10));}
 assert!(warnings.lock().unwrap().iter().any(|m|m.contains("Counter handle was not closed")),"drop warning {:?}",warnings.lock().unwrap());
 println!("PASS handles");
}
`
