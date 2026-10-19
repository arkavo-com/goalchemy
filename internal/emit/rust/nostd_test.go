package rust_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/link"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

func noStdCommand(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = append(driver.ToolEnv(), "CARGO_BUILD_JOBS=1")
	data, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, data)
	}
	return string(data)
}

func TestNoStdProgram(t *testing.T) {
	res := buildNamingFixture(t, `package main
 type Node struct { Next *Node; Data []byte }
 var state = 2
 func churn() int {
  total := 0
  for i:=0; i<20000; i++ {
   n:=&Node{Data:make([]byte,128)}
   n.Next=n
   n.Data[0]=byte(i)
   m:=map[int]*Node{1:n}
   total+=int(m[1].Next.Data[0])
  }
  return total
 }
 func main() { defer println("done"); println(state, churn()); state++ }
 `)
	for _, mode := range []string{"source", "readable", "compact"} {
		t.Run(mode, func(t *testing.T) {
			out := t.TempDir()
			if ds := driver.EmitWithOptions("rust-nostd", res, out, driver.EmitOptions{Names: mode}); len(ds) > 0 {
				t.Fatal(ds)
			}
			data, err := os.ReadFile(filepath.Join(out, "goalchemy.manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest link.Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Target != "rust-nostd" {
				t.Fatalf("target: %s", manifest.Target)
			}
			for _, f := range manifest.RuntimeFiles {
				if strings.Contains(f, "sched.rs") || strings.Contains(f, "conc.rs") {
					t.Fatalf("host runtime linked: %s", f)
				}
			}
			got, err := testutil.Runners["rust-nostd"](out)
			if err != nil {
				t.Fatal(err)
			}
			if got.Exit != 0 || got.Stderr != "2 2546416\ndone\n" {
				t.Fatalf("output: %s", got)
			}
			noStdCommand(t, out, "cargo", "check", "--offline", "--lib")
			// A caller can opt into any installed bare-metal target in CI.
			if target := os.Getenv("GOALCHEMY_NOSTD_TARGET"); target != "" {
				noStdCommand(t, out, "cargo", "build", "--offline", "--release", "--lib", "--target", target)
			}
		})
	}
	if _, exists := res.Catalog.Targets["rust-nostd"]; exists {
		t.Fatal("profile mutated shared catalog")
	}
}

func TestNoStdLibraryMemory(t *testing.T) {
	res := buildNamingFixture(t, `package fixture
 import "github.com/eugenioenko/goalchemy/lib/errors"
 type Sample struct { Bytes []byte; Total int; Ratio float64 }
 var calls int
 func Process(data []byte, n int) Sample {
  calls++
  defer println("returned")
  sum:=0
  for i:=0;i<n;i++ { x:=make([]int,4); x[0]=i; sum+=x[0] }
  return Sample{Bytes:append(data,byte(calls)),Total:sum,Ratio:1.25}
 }
 func Fail() error { return errors.New("expected") }
 func Pair() ([]byte, string, error) { return []byte{0,255}, "a\x00\xff", nil }
 func Touch(a [2]int) [2]int { a[1]++; return a }
 `)
	out := t.TempDir()
	if ds := driver.Emit("rust-nostd", res, out); len(ds) > 0 {
		t.Fatal(ds)
	}
	noStdCommand(t, out, "cargo", "build", "--offline", "--release", "--lib")
	if target := os.Getenv("GOALCHEMY_NOSTD_TARGET"); target != "" {
		noStdCommand(t, out, "cargo", "build", "--offline", "--release", "--lib", "--target", target)
	}
	const consumer = `
use goalchemy_out::*;
use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicUsize, Ordering};
struct Count;
static LIVE: AtomicUsize = AtomicUsize::new(0);
static PEAK: AtomicUsize = AtomicUsize::new(0);
static BLOCKED: AtomicUsize = AtomicUsize::new(0);
unsafe impl GlobalAlloc for Count {
 unsafe fn alloc(&self,l:Layout)->*mut u8 { let p=System.alloc(l); if !p.is_null(){let n=LIVE.fetch_add(l.size(),Ordering::SeqCst)+l.size();PEAK.fetch_max(n,Ordering::SeqCst);} p }
 unsafe fn dealloc(&self,p:*mut u8,l:Layout){LIVE.fetch_sub(l.size(),Ordering::SeqCst);System.dealloc(p,l)}
}
#[global_allocator] static ALLOC: Count = Count;
fn output(_: &[u8]) { assert!(matches!(Fail(Default::default()), Err(NoStdError::Busy))); }
fn blocked_output(_: &[u8]) { BLOCKED.store(1,Ordering::SeqCst); while BLOCKED.load(Ordering::SeqCst)==1 { std::thread::yield_now(); } }
fn main() {
 let before = LIVE.load(Ordering::SeqCst);
 for _ in 0..25 {
  let r=Process(vec![1,2,3], 5000, RuntimeOptions{gc_threshold:32,output:Some(output),..Default::default()}).unwrap();
  assert_eq!(r.value.Bytes,vec![1,2,3,1]);
  assert_eq!(r.value.Total,12497500);
  assert_eq!(r.value.Ratio,1.25);
  assert!(r.heap.collections>100);
  assert!(r.heap.peak_objects<100);
  drop(r);
  assert_eq!(LIVE.load(Ordering::SeqCst),before,"retained runtime allocations");
  assert_eq!(Fail(Default::default()).unwrap_err(),NoStdError::Source(b"expected".to_vec()));
  assert_eq!(LIVE.load(Ordering::SeqCst),before,"error path retained runtime allocations");
  let pair=Pair(Default::default()).unwrap();
  assert_eq!(pair.value,(vec![0,255],vec![97,0,255]));
  drop(pair);
  assert_eq!(Touch(vec![4,5],Default::default()).unwrap().value,vec![4,6]);
  assert_eq!(LIVE.load(Ordering::SeqCst),before,"value conversion retained runtime allocations");
 }
 // Runtime access can move between callers without sharing Rc values.
 for _ in 0..4 { std::thread::spawn(|| { assert_eq!(Process(vec![],2,Default::default()).unwrap().value.Total,1); }).join().unwrap(); }
 let worker=std::thread::spawn(|| Process(vec![],2,RuntimeOptions{output:Some(blocked_output),..Default::default()}).unwrap());
 while BLOCKED.load(Ordering::SeqCst)==0 {std::thread::yield_now();}
 assert!(matches!(Fail(Default::default()),Err(NoStdError::Busy)));
 BLOCKED.store(2,Ordering::SeqCst);
 drop(worker.join().unwrap());
 assert!(PEAK.load(Ordering::SeqCst)-before < 256*1024,"transient allocation budget");
 println!("peak allocated bytes: {}",PEAK.load(Ordering::SeqCst)-before);
}
`
	if err := os.WriteFile(filepath.Join(out, "consumer.rs"), []byte(consumer), 0600); err != nil {
		t.Fatal(err)
	}
	noStdCommand(t, out, "rustc", "--edition=2021", "-C", "panic=abort", "consumer.rs", "--extern", "goalchemy_out=target/release/libgoalchemy_out.rlib", "-o", "consumer")
	t.Log(noStdCommand(t, out, filepath.Join(out, "consumer")))
}

func TestNoStdRejectsUnsupported(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"recover", `package main; func main(){ defer func(){recover()}(); panic("bad") }`, "recover"},
		{"channels", `package main; func main(){ c:=make(chan int,1); c<-1; println(<-c) }`, "concurrency"},
		{"clock", `package main; import "github.com/eugenioenko/goalchemy/lib/clock"; func main(){println(clock.Unix())}`, "lib.clock.unix"},
		{"handles", `package fixture; type Thing struct { Value int }; func New() *Thing { return &Thing{} }`, "owned value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := buildNamingFixture(t, c.source)
			out := t.TempDir()
			ds := driver.Emit("rust-nostd", res, out)
			if len(ds) == 0 || !strings.Contains(ds[0].Message, c.want) {
				t.Fatalf("diagnostics: %v", ds)
			}
			entries, _ := os.ReadDir(out)
			if len(entries) > 0 {
				t.Fatal("rejected output was partially written")
			}
		})
	}
}
