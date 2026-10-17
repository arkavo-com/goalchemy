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

func TestGeneratedSwiftHandles(t *testing.T) {
	res := buildHandles(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("swift", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	manifest := fmt.Sprintf(`// swift-tools-version: 6.0
import PackageDescription
let package = Package(name:"HandlesConsumer",dependencies:[.package(name:"GoalchemyGenerated",path:%q)],targets:[.executableTarget(name:"HandlesConsumer",dependencies:[.product(name:"GoalchemyGenerated",package:"GoalchemyGenerated")],path:".",sources:["main.swift"])],swiftLanguageModes:[.v5])
`, out)
	for file, source := range map[string]string{"Package.swift": manifest, "main.swift": swiftHandlesConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, file), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "swift run -c release -Xswiftc -Onone")
	cmd.Dir, cmd.Env = consumer, driver.ToolEnv()
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("handles consumer: %v\n%s", err, data)
	}
}

const swiftHandlesConsumer = `import Foundation
import GoalchemyGenerated
func check(_ ok: Bool, _ message: String) { if !ok { fatalError(message) } }
func kind<T>(_ op: GoalchemyGenerated.Operation<T>) -> String {
  do { _ = try op.wait(); return "" } catch let f as GoalchemyFailure { return f.kind } catch { return "other" }
}
func make(_ name: String) -> Counter { try! New(GoString(name)).wait()! }
// Instance state, init once, sentinel identity, native key and identity.
var c: Counter? = make("a")
for i in 1...3 { check(try! c!.Add(2).wait() == Int64(2 * i), "persistent field") }
check(kind(c!.Add(-1)) == "source", "source error")
var s = try! c!.Snap().wait()
check(s.Name.description == "a" && s.N == 6 && s.Inits == 1 && s.Created == 1 && s.LastNeg, "instance globals")
check(try! c!.PublicKey().wait().description.hasPrefix("-----BEGIN PUBLIC KEY-----"), "native key survives its creating call")
for _ in 0..<3 { check(try! Inits().wait() == 1, "free functions keep fresh globals") }
check(try! c!.Self().wait() === c, "identity")
let child = try! c!.Child().wait()!
check(try! child.Bump().wait() == 7 && (try! child.Parent().wait()) === c && (try! c!.Snap().wait()).N == 7, "derived handle")
try! child.close()
check(kind(child.Bump()) == "closed", "closed derived handle")
check(try! c!.Add(1).wait() == 8, "closing a derived handle keeps the instance")
check(try! Nil().wait() == nil, "nil handle")
try! c!.close()
// Handle parameters.
let a = make("a"), b = try! a.Sibling(GoString("b")).wait()!, other = make("other")
_ = try! a.Add(2).wait(); _ = try! b.Add(3).wait()
check(try! Sum(a, b).wait() == 5, "handles of one instance")
s = try! b.Snap().wait(); check(s.Created == 2 && s.Inits == 1, "sibling joins the instance")
check(kind(Sum(a, other)) == "instance_mismatch", "mixed instances")
check(try! Sum(nil, nil).wait() == -1, "nil handle arguments")
try! a.close(); try! b.close(); try! other.close()
// Value receivers run on a copy of the handle's object.
c = make("a")
check(try! c!.Name().wait().description == "a", "value receiver on a handle with private state")
try! c!.close()
let p = try! NewCoord(1, 2).wait()!
check((try! p.Total().wait()) == 3 && (try! p.Shift(5).wait()) == 6 && (try! p.Total().wait()) == 3, "value receiver must not change the handle")
let m = try! p.Moved(10).wait()
check(m.X == 11 && m.Y == 2 && (try! p.Total().wait()) == 3, "value result is a copy")
check((try! p.Less(m).wait()) && !(try! p.Less(try! p.Moved(-1).wait()).wait()), "value parameter of the handle type")
check((try! p.Move(1).wait()) == 2 && (try! p.Total().wait()) == 4, "pointer receiver changes the handle")
try! p.close()
do { try NewCoord(-1, 0).wait()!.close(); check(false, "missing Close error") } catch let f as GoalchemyFailure { check(f.kind == "source", "value receiver Close") }
// Calls from several host threads are serialized; instances are independent.
let x = make("x"), y = make("y")
DispatchQueue.concurrentPerform(iterations: 8) { i in
  let h = i % 2 == 0 ? x : y
  for _ in 0..<50 { _ = try! h.Add(1).wait() }
}
let sx = try! x.Snap().wait(), sy = try! y.Snap().wait()
check(sx.N == 200 && sy.N == 200 && sx.Created == 1 && sy.Created == 1, "serialized calls")
try! x.close(); try! y.close()
// Close.
c = make("a"); let sib = try! c!.Sibling(GoString("bad-close")).wait()!
try! c!.close(); try! c!.close()
check(kind(c!.Add(1)) == "closed", "call after close")
check(try! sib.Snap().wait().Closes == 1, "source Close ran once")
do { try sib.close(); check(false, "missing Close error") } catch let f as GoalchemyFailure { check(f.kind == "source", "source Close error") }
check(kind(sib.Snap()) == "closed", "failed Close still releases")
// Cancellation and Close while pending.
c = make("a")
let op = c!.Wait(100000)
DispatchQueue.global().asyncAfter(deadline: .now() + 0.1) { op.cancel() }
check(kind(op) == "canceled", "active cancellation")
check(try! c!.Add(1).wait() == 1, "usable after cancellation")
let group = DispatchGroup()
var waited = "", queued = ""
let target = c!
DispatchQueue.global().async(group: group) { waited = kind(target.Wait(100000)) }
Thread.sleep(forTimeInterval: 0.1)
DispatchQueue.global().async(group: group) { queued = kind(target.Add(1)) }
Thread.sleep(forTimeInterval: 0.05)
try! c!.close()
group.wait()
check(waited == "canceled", "active call canceled by close: " + waited)
check(queued == "closed", "queued call failed by close: " + queued)
// Poisoning.
c = make("a")
check(kind(c!.Boom()) == "panic", "panic")
check(kind(c!.Add(1)) == "poisoned", "poisoned instance")
try! c!.close()
check(kind(c!.Add(1)) == "closed", "closed poisoned")
// Warnings: abandoned goroutines and unclosed handles.
var warnings: [String] = []
setGoalchemyWarningHandler { warnings.append($0) }
c = make("a")
_ = try! c!.Leak().wait()
check(warnings.contains { $0.contains("goroutine") }, "abandoned goroutine warning")
check(try! c!.Add(1).wait() == 1, "usable after abandoning goroutines")
c = nil
check(warnings.contains { $0.contains("Counter handle was not closed") }, "deinit warning")
print("PASS handles")
`
