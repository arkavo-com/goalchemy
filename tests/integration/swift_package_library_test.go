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

func TestGeneratedSwiftPackageLibrary(t *testing.T) {
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
	probe := `package api
import("github.com/eugenioenko/goalchemy/lib/context";"github.com/eugenioenko/goalchemy/lib/callback";"github.com/eugenioenko/goalchemy/lib/crypto";"github.com/eugenioenko/goalchemy/lib/checksum")
func Hold(ctx context.Context)([]byte,error){return callback.Request(ctx,"hold",nil)}
func Native(ctx context.Context)([]byte,error){k,e:=crypto.GenerateP256();if e!=nil{return nil,e};defer k.Close();public:=k.PublicPEM;p,e:=public();if e!=nil{return nil,e};if len(p)==0{panic("public key")};hash:=crypto.SHA256;return hash([]byte{0,255,128})}
func CRC(ctx context.Context,data []byte)(uint32,error){return checksum.CRC32IEEE(data),nil}
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
			if ds := driver.EmitWithOptions("swift", res, out, driver.EmitOptions{Names: testutil.Names(compact)}); diagnostics.HasErrors(ds) {
				t.Fatal(ds)
			}
			// Caller-owned sources must not enter the generated native target.
			for _, file := range []string{"Caller.swift", "rt/types/Caller.swift", "rt/native/Caller.c"} {
				if err := os.WriteFile(filepath.Join(out, file), []byte("caller-owned invalid source outside inventory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			manifest := fmt.Sprintf(`// swift-tools-version: 6.0
import PackageDescription
let package = Package(name:"PackageConsumer",dependencies:[.package(name:"GoalchemyGenerated",path:%q)],targets:[.executableTarget(name:"PackageConsumer",dependencies:[.product(name:"GoalchemyGenerated",package:"GoalchemyGenerated")],path:".",sources:["main.swift"])],swiftLanguageModes:[.v5])
`, out)
			for file, source := range map[string]string{"Package.swift": manifest, "main.swift": swiftPackageLibraryConsumer} {
				if err := os.WriteFile(filepath.Join(consumer, file), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", "swift run -c release -Xswiftc -Onone")
			cmd.Dir, cmd.Env = consumer, driver.ToolEnv()
			data, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("independent native SwiftPM consumer: %v\n%s", err, data)
			}
			if !strings.Contains(string(data), "PASS Swift package library") {
				t.Fatal(string(data))
			}
			t.Log(string(data))
		})
	}
}

const swiftPackageLibraryConsumer = `import Foundation
import GoalchemyGenerated
func require(_ value: Bool,_ text: String) {if !value {fatalError(text)}}
let order = GoString("order-var/order-init/state-var/state-init/api-var/api-init")
let input = Value(Count:2,Bytes:[1,255,128],Order:GoString("caller"))
func check(_ value: Value) {require(value.Count==29 && value.Bytes==[3,255,128] && value.Order==order,"fresh source packages/init/closure/promoted method")}
var saved=try Export(input).wait();check(saved)
require(input.Count==2 && input.Bytes==[1,255,128] && input.Order==GoString("caller"),"input mutated")
var retained: GoalchemyFailure?
do{_ = try ErrorGlobal().wait();fatalError("missing source failure")}catch let failure as GoalchemyFailure{retained=failure;require(failure.kind=="source","source classification")}
require(retained?.fields["Code"] as? GoString == GoString("saved") && retained?.fields["Bytes"] as? [UInt8] == [9],"source error field snapshot")
let diagnostic=retained!.message
let operations=(0..<12).map{_ in Export(input)}
let group=DispatchGroup()
for operation in operations {group.enter();DispatchQueue.global().async{defer{group.leave()};do{check(try operation.wait())}catch{fatalError("overlap failure: \(error)")}}}
require(group.wait(timeout:.now()+10 ) == .success,"overlap timeout")
for _ in 0..<40 {check(try Export(input).wait())}
check(saved);require(retained!.message==diagnostic && retained?.fields["Bytes"] as? [UInt8] == [9],"retained error changed")
saved.Bytes![0]=99;check(try Export(input).wait());require(saved.Bytes![0]==99,"publication storage alias")
require(try Native().wait()==[247,66,185,101,241,86,193,3,116,188,35,174,169,110,58,138,255,143,172,214,252,7,157,239,234,163,2,25,173,134,242,17],"native bound crypto and SHA")
require(try CRC(Array("123456789".utf8)).wait()==0xcbf43926,"CRC")
let started=DispatchSemaphore(value:0),stopped=DispatchSemaphore(value:0),ack=DispatchSemaphore(value:0)
let activeDone=DispatchSemaphore(value:0),queuedDone=DispatchSemaphore(value:0),laterDone=DispatchSemaphore(value:0)
let provider: CallbackProvider={request,settle in
 started.signal()
 return {
  require(request.cancellation.isCanceled,"provider cancellation token")
  stopped.signal()
  DispatchQueue.global().async {require(ack.wait(timeout:.now()+10 ) == .success,"cleanup ACK timeout");settle(.success([]))}
 }
}
let forbidden: CallbackProvider={_,_ in fatalError("queued canceled source entered native provider")}
// Prepare operations before the owner holds the library lock, then overlap waits.
let active=Hold(CallOptions(callbacks:["hold":provider]))
let queued=Hold(CallOptions(callbacks:["hold":forbidden]))
let later=Export(input)
DispatchQueue.global().async{defer{activeDone.signal()};do{_ = try active.wait();fatalError("active cancel succeeded")}catch let error as GoalchemyFailure{require(error.message=="context canceled","active cancellation identity")}catch{fatalError("active wrong error")}}
require(started.wait(timeout:.now()+10 ) == .success,"native resource not acquired")
queued.cancel()
DispatchQueue.global().async{defer{queuedDone.signal()};do{_ = try queued.wait();fatalError("queued cancel succeeded")}catch let error as GoalchemyFailure{require(error.message=="context canceled","queued cancellation identity")}catch{fatalError("queued wrong error")}}
DispatchQueue.global().async{defer{laterDone.signal()};do{check(try later.wait())}catch{fatalError("fresh owner after cleanup")}}
active.cancel();require(stopped.wait(timeout:.now()+10 ) == .success,"cancel hook not invoked")
require(activeDone.wait(timeout:.now()+0.03 ) == .timedOut,"active returned before resource ACK")
require(queuedDone.wait(timeout:.now() ) == .timedOut && laterDone.wait(timeout:.now() ) == .timedOut,"next owner entered before resource ACK")
ack.signal()
require(activeDone.wait(timeout:.now()+10 ) == .success && queuedDone.wait(timeout:.now()+10 ) == .success && laterDone.wait(timeout:.now()+10 ) == .success,"operation did not settle after ACK")
check(try Export(input).wait())
print("PASS Swift package library init/overlap/retained/errors/active+queued cancellation/resource ACK/native crypto/CRC")
`
