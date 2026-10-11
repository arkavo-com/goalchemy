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
	"github.com/eugenioenko/goalchemy/internal/subset"
)

func buildSourceStreams(t *testing.T) *driver.Result {
	t.Helper()
	fixture, err := filepath.Abs("testdata/sourcestreams")
	if err != nil {
		t.Fatal(err)
	}
	res, ds := driver.Build(context.Background(), driver.Options{Dir: fixture, Gate: subset.Cooperative})
	if diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	return res
}

// throughputMiB is the size of the host → handle → host copy; set
// GOALCHEMY_THROUGHPUT_MIB to override it.
func throughputMiB(def int) int {
	var n int
	if _, err := fmt.Sscan(os.Getenv("GOALCHEMY_THROUGHPUT_MIB"), &n); err == nil && n > 0 {
		return n
	}
	return def
}

func TestGeneratedGoSourceStreams(t *testing.T) {
	res := buildSourceStreams(t)
	out, consumer := t.TempDir(), t.TempDir()
	if ds := driver.Emit("go", res, out); diagnostics.HasErrors(ds) {
		t.Fatal(ds)
	}
	mod := fmt.Sprintf("module independent\n\ngo 1.25\nrequire goalchemyout v0.0.0\nreplace goalchemyout => %s\n", out)
	for name, data := range map[string]string{"go.mod": mod, "consumer_test.go": goSourceStreamsConsumer} {
		if err := os.WriteFile(filepath.Join(consumer, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "-v", "./...")
	cmd.Dir, cmd.Env = consumer, append(driver.ToolEnv(), "GOTOOLCHAIN=go1.25.14", "GOFLAGS=", fmt.Sprintf("THROUGHPUT_MIB=%d", throughputMiB(256)))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source streams consumer: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}

const goSourceStreamsConsumer = `package independent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	g "goalchemyout"
)

var bg = context.Background()

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func kind(err error) string {
	var le *g.LibraryError
	if errors.As(err, &le) {
		return le.Kind
	}
	return ""
}

type patternReader struct{ i, n int64 }

func (p *patternReader) Read(b []byte) (int, error) {
	if p.i >= p.n {
		return 0, io.EOF
	}
	if int64(len(b)) > p.n-p.i {
		b = b[:p.n-p.i]
	}
	for k := range b {
		b[k] = byte((p.i + int64(k)) % 251)
	}
	p.i += int64(len(b))
	return len(b), nil
}

type fnv struct {
	h    uint32
	n    int64
	peak uint64
}

func (f *fnv) Write(p []byte) (int, error) {
	for _, b := range p {
		f.h = (f.h ^ uint32(b)) * 16777619
	}
	before := f.n
	f.n += int64(len(p))
	if before>>24 != f.n>>24 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.HeapInuse > f.peak {
			f.peak = m.HeapInuse
		}
	}
	return len(p), nil
}

func TestReturnedReaders(t *testing.T) {
	r, err := g.Pattern(bg, 3<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(io.Writer); ok {
		t.Fatal("io.Reader result exposes Write")
	}
	defer r.(io.Closer).Close()
	data, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(data, pattern(3<<20)) {
		t.Fatal("pattern", len(data), err)
	}
	r, _ = g.Pattern(bg, 1000)
	var buf bytes.Buffer
	if n, err := io.Copy(&buf, r); err != nil || n != 1000 || !bytes.Equal(buf.Bytes(), pattern(1000)) {
		t.Fatal("WriteTo", n, err)
	}

	rs, err := g.Text(bg, "hello, world")
	if err != nil {
		t.Fatal(err)
	}
	if pos, err := rs.Seek(7, io.SeekStart); err != nil || pos != 7 {
		t.Fatal("seek", pos, err)
	}
	if rest, err := io.ReadAll(rs); err != nil || string(rest) != "world" {
		t.Fatal("read after seek", string(rest), err)
	}

	f, _ := g.Failing(bg, 10)
	got, err := io.ReadAll(f)
	if len(got) != 10 || err == nil || err.Error() != "source failed" {
		t.Fatal("source error", len(got), err)
	}

	if n, err := g.Nil(bg); err != nil || n != nil {
		t.Fatal("nil reader", n, err)
	}

	host := pattern(3 << 20)
	rc, err := g.Through(bg, bytes.NewReader(host))
	if err != nil {
		t.Fatal(err)
	}
	if back, err := io.ReadAll(rc); err != nil || !bytes.Equal(back, host) {
		t.Fatal("host → source → host", len(back), err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Read(make([]byte, 4)); kind(err) != "closed" {
		t.Fatal("read after close", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal("close is idempotent", err)
	}
}

func TestReturnedWriterAndHandleViews(t *testing.T) {
	sink, err := g.NewSink(bg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	w, err := sink.Writer(bg)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := io.Copy(w, bytes.NewReader(pattern(5<<20))); err != nil || n != 5<<20 {
		t.Fatal("copy into writer", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if c, _ := sink.Closes(bg); c != 1 {
		t.Fatal("writer Close ran the source Close", c)
	}
	if _, err := w.Write([]byte("x")); kind(err) != "closed" {
		t.Fatal("write after close", err)
	}
	if _, err := sink.IO().Write([]byte("tail")); err != nil {
		t.Fatal("handle view write", err)
	}
	count, _ := sink.Count(bg)
	if count != 5<<20+4 {
		t.Fatal("count", count)
	}
	contents, err := sink.Contents(bg)
	if err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(contents)
	if err != nil || !bytes.Equal(all, append(pattern(5<<20), "tail"...)) {
		t.Fatal("contents", len(all), err)
	}
	contents.(io.Closer).Close()

	data := []byte("persistent document")
	doc, err := g.Open(bg, data)
	if err != nil {
		t.Fatal(err)
	}
	view := doc.IO()
	if pos, err := view.Seek(11, io.SeekStart); err != nil || pos != 11 {
		t.Fatal("view seek", pos, err)
	}
	if rest, err := io.ReadAll(view); err != nil || string(rest) != "document" {
		t.Fatal("view read", string(rest), err)
	}
	if reads, _ := doc.Reads(bg); reads < 2 {
		t.Fatal("reads", reads)
	}
	if err := view.Close(); err != nil {
		t.Fatal("view close runs Doc.Close once", err)
	}
	if err := doc.Close(); err != nil {
		t.Fatal("handle close after view close", err)
	}
	if _, err := doc.Reads(bg); kind(err) != "closed" {
		t.Fatal("handle closed by its view", err)
	}
}

func TestConcurrentReads(t *testing.T) {
	const n = 1 << 20
	r, _ := g.Pattern(bg, n)
	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 4096)
			for {
				k, err := r.Read(buf)
				mu.Lock()
				total += k
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	if total != n {
		t.Fatal("concurrent reads", total)
	}
}

func TestThroughput(t *testing.T) {
	mib, _ := strconv.Atoi(os.Getenv("THROUGHPUT_MIB"))
	size := int64(mib) << 20
	want, err := g.Hash(bg, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.Through(bg, &patternReader{n: size})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	sink := &fnv{h: 2166136261}
	start := time.Now()
	n, err := io.Copy(sink, r)
	elapsed := time.Since(start)
	if err != nil || n != size {
		t.Fatal("throughput copy", n, err)
	}
	check := &fnv{h: 2166136261}
	io.Copy(check, &patternReader{n: 1 << 20})
	if int64(check.h) != want {
		t.Fatal("source hash", check.h, want)
	}
	if sink.peak > 256<<20 {
		t.Fatal("peak heap", sink.peak)
	}
	t.Logf("throughput: %d MiB in %v (%.1f MiB/s), peak heap %d MiB", mib, elapsed, float64(mib)/elapsed.Seconds(), sink.peak>>20)
}
`

func TestSourceStreamResultsRejectedOnPendingTargets(t *testing.T) {
	res := buildSourceStreams(t)
	for _, target := range []string{"java", "csharp", "rust", "c", "swift"} {
		ds := driver.Emit(target, res, t.TempDir())
		if len(ds) == 0 || ds[0].Code != "GCE007" {
			t.Errorf("%s: want GCE007, got %v", target, ds)
		}
	}
}
