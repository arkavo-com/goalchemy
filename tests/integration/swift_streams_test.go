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

func TestGeneratedSwiftStreams(t *testing.T) {
	res := buildStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("swift", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	manifest := fmt.Sprintf(`// swift-tools-version: 6.0
import PackageDescription
let package = Package(name:"StreamsConsumer",dependencies:[.package(name:"GoalchemyGenerated",path:%q)],targets:[.executableTarget(name:"StreamsConsumer",dependencies:[.product(name:"GoalchemyGenerated",package:"GoalchemyGenerated")],path:".",sources:["main.swift"])],swiftLanguageModes:[.v5])
`, out)
	for file, source := range map[string]string{"Package.swift": manifest, "main.swift": swiftStreamsConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, file), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "swift run -c release -Xswiftc -Onone")
	cmd.Dir, cmd.Env = consumer, driver.ToolEnv()
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streams consumer: %v\n%s", err, data)
	}
}

const swiftStreamsConsumer = `import Foundation
import GoalchemyGenerated
func check(_ ok: Bool, _ message: String) { if !ok { fatalError(message) } }
func kind<T>(_ op: GoalchemyGenerated.Operation<T>) -> String {
  do { _ = try op.wait(); return "" } catch let f as GoalchemyFailure { return f.kind } catch { return "other" }
}
final class Sink { var data: [UInt8] = [] }
func writer(_ sink: Sink) -> GoalchemyStream { GoalchemyStream(write: { sink.data += $0; return $0.count }) }
let big = (0..<(3 << 20)).map { UInt8($0 % 251) }
let sink = Sink()
check(try! Copy(writer(sink), GoalchemyStream(bytes: big)).wait() == Int64(big.count) && sink.data == big, "chunked copy")
check(try! Sum(GoalchemyStream(bytes: [1, 2, 3])).wait() == 6, "bytes reader")
check(try! Section(GoalchemyStream(bytes: Array("hello, world".utf8)), 7, 5).wait() == Array("world".utf8), "seek")
check(kind(Section(GoalchemyStream(bytes: Array("short".utf8)), 3, 5)) == "source", "io.ErrUnexpectedEOF")
var closes = 0
var abc = GoalchemyStream(bytes: Array("abc".utf8))
abc.close = { closes += 1 }
check(try! Drain(abc).wait() == 3 && closes == 1, "close")
check(try! AtEOF(GoalchemyStream(bytes: [])).wait(), "host EOF is io.EOF")
struct Fire: Error, CustomStringConvertible { var description: String { "disk on fire" } }
check(try! FirstError(GoalchemyStream(read: { _ in throw Fire() })).wait().description == "disk on fire", "host error")
check(try! Nil(nil).wait(), "nil stream")
let reentrant = GoalchemyStream(read: { _ in _ = try Sum(GoalchemyStream(bytes: [1])).wait(); return [] })
check(try! FirstError(reentrant).wait().description.contains("host stream called back"), "reentrant host stream")
check(kind(Sum(writer(Sink()))) == "invalid_argument", "stream without read")
check(try! Copy(GoalchemyStream(output: OutputStream(toMemory: ())), GoalchemyStream(input: InputStream(data: Data([1, 2])))).wait() == 2, "Foundation streams")
let doc = try! Open(GoalchemyStream(bytes: Array("persistent document".utf8))).wait()!
check(try! doc.Size().wait() == 19, "size")
check(try! doc.ReadAt(11, 8).wait() == Array("document".utf8), "stream kept by the instance")
let whole = Sink()
check(try! doc.WriteTo(writer(whole)).wait() == 19 && whole.data == Array("persistent document".utf8), "WriteTo")
try! doc.close()
print("PASS streams")
`
