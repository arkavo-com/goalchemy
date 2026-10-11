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

func TestGeneratedSwiftSourceStreams(t *testing.T) {
	res := buildSourceStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("swift", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	manifest := fmt.Sprintf(`// swift-tools-version: 6.0
import PackageDescription
let package = Package(name:"SourceStreamsConsumer",dependencies:[.package(name:"GoalchemyGenerated",path:%q)],targets:[.executableTarget(name:"SourceStreamsConsumer",dependencies:[.product(name:"GoalchemyGenerated",package:"GoalchemyGenerated")],path:".",sources:["main.swift"])],swiftLanguageModes:[.v5])
`, out)
	for file, source := range map[string]string{"Package.swift": manifest, "main.swift": swiftSourceStreamsConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, file), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "swift run -c release")
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), fmt.Sprintf("THROUGHPUT_MIB=%d", throughputMiB(256)))
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source streams consumer: %v\n%s", err, data)
	}
	t.Logf("%s", data)
}

const swiftSourceStreamsConsumer = `import Foundation
import GoalchemyGenerated
func check(_ ok: Bool, _ message: String) { if !ok { fatalError(message) } }
func kind(_ body: () throws -> Void) -> String {
  do { try body(); return "" } catch let f as GoalchemyFailure { return f.kind } catch { return "other" }
}
func pattern(_ n: Int, _ from: Int = 0) -> [UInt8] { (0..<n).map { UInt8((from + $0) % 251) } }
func readAll(_ s: GoalchemySourceStream) throws -> [UInt8] {
  var out: [UInt8] = []
  while let chunk = try s.read(1 << 20) { out += chunk }
  return out
}
func rssMiB() -> Int {
  let s = (try? String(contentsOfFile: "/proc/self/status", encoding: .utf8)) ?? ""
  for line in s.split(separator: "\n") where line.hasPrefix("VmRSS:") {
    return (Int(line.split(separator: " ").dropFirst().first ?? "0") ?? 0) >> 10
  }
  return 0
}

let r = try! Pattern(3 << 20).wait()!
check(r.canRead && !r.canWrite && !r.canSeek, "kind")
check(kind { try r.write([1]) } == "unsupported", "write on a reader")
check(try! readAll(r) == pattern(3 << 20), "read all")
check(try! r.read() == nil, "EOF")
try! r.close()

let small = try! Pattern(1000).wait()!
var chunks: [UInt8] = []
let done = DispatchSemaphore(value: 0)
Task {
  for try await chunk in small { chunks += chunk }
  done.signal()
}
done.wait()
check(chunks == pattern(1000), "AsyncSequence")
try! small.close()

let rs = try! Text(GoString("hello, world")).wait()!
check(try! rs.seek(7) == 7, "seek")
check(try! readAll(rs) == Array("world".utf8), "read after seek")
check(try! rs.seek(-5, 2) == 7, "seek from end")
var buffer = [UInt8](repeating: 0, count: 3)
check(try! buffer.withUnsafeMutableBufferPointer { try rs.read(into: $0) } == 3 && buffer == Array("wor".utf8), "read(into:)")
try! rs.close()

let f = try! Failing(10).wait()!
check(try! f.read(64)?.count == 10, "data before the error")
check(kind { _ = try f.read(64) } == "source", "source error")
try! f.close()

check(try! Nil().wait() == nil, "nil reader")

let big = pattern(3 << 20)
let rc = try! Through(GoalchemyStream(bytes: big)).wait()!
check(try! readAll(rc) == big, "host to source to host")
try! rc.close()
check(kind { _ = try rc.read() } == "closed", "read after close")
try! rc.close()

let sink = try! NewSink(true).wait()!
let w = try! sink.Writer().wait()!
check(w.canWrite && !w.canRead, "writer kind")
let data = pattern(5 << 20)
try! w.write(data)
try! w.close()
check(try! sink.Closes().wait() == 1, "writer close runs the source Close")
check(kind { try w.write([1]) } == "closed", "write after close")
try! sink.io().write(Array("tail".utf8))
check(try! sink.Count().wait() == Int64(data.count + 4), "count")
let contents = try! sink.Contents().wait()!
check(try! readAll(contents) == data + Array("tail".utf8), "contents")
try! contents.close()
try! sink.close()

let doc = try! Open(Array("persistent document".utf8)).wait()!
let view = doc.io()
check(try! view.seek(11) == 11, "view seek")
check(try! readAll(view) == Array("document".utf8), "view read")
check(try! doc.Reads().wait() >= 2, "reads")
try! view.close()
try! doc.close()
check(kind { _ = try doc.Reads().wait() } == "closed", "handle closed by its view")

let n = 1 << 20
let shared = try! Pattern(Int64(n)).wait()!
let lock = NSLock()
var total = 0
DispatchQueue.concurrentPerform(iterations: 8) { _ in
  while let c = try! shared.read(4096) { lock.lock(); total += c.count; lock.unlock() }
}
check(total == n, "concurrent reads \(total)")
try! shared.close()

let mib = Int(ProcessInfo.processInfo.environment["THROUGHPUT_MIB"] ?? "256") ?? 256
let size = mib << 20
let block = pattern(251 * 1024)
func source() -> GoalchemyStream {
  var left = size, at = 0
  return GoalchemyStream(read: { n in
    let k = min(n, left, 128 << 10)
    let off = at % 251
    left -= k; at += k
    return Array(block[off..<off + k])
  })
}
let start = Date()
let through = try! Through(source()).wait()!
let base = rssMiB()
var peak = 0, count = 0
var h: UInt32 = 2166136261
while let c = try! through.read(1 << 20) {
  for x in c { h = (h ^ UInt32(x)) &* 16777619 }
  if count >> 24 != (count + c.count) >> 24 { peak = max(peak, rssMiB()) }
  count += c.count
}
try! through.close()
let seconds = Date().timeIntervalSince(start)
var want: UInt32 = 2166136261
for i in 0..<size { want = (want ^ UInt32(i % 251)) &* 16777619 }
check(count == size && h == want, "throughput copy \(count)")
check(peak < base + 256, "rss grew from \(base) to \(peak) MiB")
print(String(format: "throughput: %d MiB in %.1fs (%.1f MiB/s), rss %d MiB at start, peak %d MiB", mib, seconds, Double(mib) / seconds, base, peak))
print("PASS source streams")
`
