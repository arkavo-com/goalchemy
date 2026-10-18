package os

import (
	"errors"
	stdio "io"
	stdos "os"
	"path/filepath"
	"strings"
	"testing"

	goerrors "github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/io"
)

func TestFileMatchesGo(t *testing.T) {
	dir := t.TempDir()
	mine, gos := filepath.Join(dir, "mine"), filepath.Join(dir, "go")
	f, err := Create(mine)
	g, gerr := stdos.Create(gos)
	if err != nil || gerr != nil {
		t.Fatal(err, gerr)
	}
	payload := strings.Repeat("0123456789", 300000)
	n, err := f.WriteString(payload)
	gn, gerr := g.WriteString(payload)
	if n != gn || (err == nil) != (gerr == nil) {
		t.Fatalf("write %d %v, Go %d %v", n, err, gn, gerr)
	}
	for _, c := range []struct {
		off    int64
		whence int
	}{{0, io.SeekStart}, {-10, io.SeekEnd}, {-5, io.SeekCurrent}, {1 << 40, io.SeekStart}} {
		pos, err := f.Seek(c.off, c.whence)
		gpos, gerr := g.Seek(c.off, c.whence)
		if pos != gpos || (err == nil) != (gerr == nil) {
			t.Fatalf("seek %v: %d %v, Go %d %v", c, pos, err, gpos, gerr)
		}
	}
	f.Seek(0, io.SeekStart)
	g.Seek(0, io.SeekStart)
	got, err := io.ReadAll(f)
	want, gerr := stdio.ReadAll(g)
	if string(got) != string(want) || err != nil || gerr != nil {
		t.Fatalf("readall %d %v, Go %d %v", len(got), err, len(want), gerr)
	}
	buf, gbuf := make([]byte, 4), make([]byte, 4)
	n, err = f.Read(buf)
	gn, gerr = g.Read(gbuf)
	if n != gn || err != io.EOF || gerr != stdio.EOF {
		t.Fatalf("read at end %d %v, Go %d %v", n, err, gn, gerr)
	}
	for _, off := range []int64{0, 7, int64(len(payload)) - 2, int64(len(payload)), -1} {
		n, err := f.ReadAt(buf, off)
		gn, gerr := g.ReadAt(gbuf, off)
		if n != gn || string(buf[:n]) != string(gbuf[:gn]) || (err == nil) != (gerr == nil) || (err == io.EOF) != (gerr == stdio.EOF) {
			t.Fatalf("readat %d: %d %q %v, Go %d %q %v", off, n, buf[:n], err, gn, gbuf[:gn], gerr)
		}
		if err != nil && err != io.EOF && err.Error() != strings.Replace(gerr.Error(), gos, mine, 1) {
			t.Fatalf("readat %d text %q, Go %q", off, err, gerr)
		}
	}
	big := make([]byte, 3<<20)
	n, err = f.ReadAt(big, 0)
	gn, gerr = g.ReadAt(make([]byte, 3<<20), 0)
	if n != gn || err != io.EOF || gerr != stdio.EOF || string(big[:n]) != payload {
		t.Fatalf("large readat %d %v, Go %d %v", n, err, gn, gerr)
	}
	st, err := f.Stat()
	gst, gerr := g.Stat()
	if err != nil || gerr != nil || st.Name() != "mine" || st.Size() != gst.Size() || st.Mode().Perm() != FileMode(gst.Mode().Perm()) || st.IsDir() || st.Mode().IsDir() || !st.Mode().IsRegular() {
		t.Fatalf("stat %v %v, Go %v %v", st, err, gst, gerr)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil || g.Close() != nil {
		t.Fatal(err)
	}
	err, gerr = f.Close(), g.Close()
	if err.Error() != strings.Replace(gerr.Error(), gos, mine, 1) || !goerrors.Is(err, ErrClosed) || !errors.Is(gerr, stdos.ErrClosed) {
		t.Fatalf("double close %v, Go %v", err, gerr)
	}
	if _, err := f.Read(buf); !goerrors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	var nilFile *File
	if _, err := nilFile.Read(buf); err != ErrInvalid || nilFile.Name() != "<nil>" {
		t.Fatal(err)
	}
	dst, err := Stat(dir)
	if err != nil || !dst.IsDir() || !dst.Mode().IsDir() || dst.Name() != filepath.Base(dir) {
		t.Fatalf("stat dir %v %v", dst, err)
	}
	a, err := OpenFile(mine, O_WRONLY|O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(buf); err == nil {
		t.Fatal("read from write-only file")
	}
	a.Close()
	data, _ := ReadFile(mine)
	if !strings.HasSuffix(string(data), "9tail") || len(data) != len(payload)+4 {
		t.Fatalf("append %d", len(data))
	}
}

func TestFileErrorsMatchGo(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(dir, "missing"), dir + "/", filepath.Join(file, "x"), "a\x00b"} {
		_, got := Open(name)
		_, want := stdos.Open(name)
		if (got == nil) != (want == nil) {
			t.Fatalf("Open(%q) = %v, Go %v", name, got, want)
		}
		if got != nil {
			compare(t, "Open", name, got, want)
		}
		_, got = Stat(name)
		_, want = stdos.Stat(name)
		if (got == nil) != (want == nil) {
			t.Fatalf("Stat(%q) = %v, Go %v", name, got, want)
		}
		if got != nil {
			compare(t, "Stat", name, got, want)
		}
	}
	_, got := OpenFile(file, O_WRONLY|O_CREATE|O_EXCL, 0o600)
	_, want := stdos.OpenFile(file, stdos.O_WRONLY|stdos.O_CREATE|stdos.O_EXCL, 0o600)
	compare(t, "OpenFile", file, got, want)
	d, _ := Open(dir)
	gd, _ := stdos.Open(dir)
	_, got = d.Read(make([]byte, 1))
	_, want = gd.Read(make([]byte, 1))
	compare(t, "Read", dir, got, want)
	d.Close()
	gd.Close()
	missing := filepath.Join(dir, "missing")
	compare(t, "Remove", missing, Remove(missing), stdos.Remove(missing))
	got, want = Rename(missing, file), stdos.Rename(missing, file)
	if got.Error() != want.Error() || !goerrors.Is(got, ErrNotExist) {
		t.Fatalf("Rename %v, Go %v", got, want)
	}
	var le *LinkError
	if !goerrors.As(got, &le) || le.Op != "rename" || le.Old != missing || le.New != file {
		t.Fatalf("LinkError %v", got)
	}
	_, got = CreateTemp(dir, "a/b")
	_, want = stdos.CreateTemp(dir, "a/b")
	compare(t, "CreateTemp", "a/b", got, want)
	_, got = CreateTemp(missing, "x")
	_, want = stdos.CreateTemp(missing, "x")
	if !strings.HasPrefix(got.Error(), "open "+missing+"/x") || !strings.HasSuffix(got.Error(), ": no such file or directory") || !goerrors.Is(got, ErrNotExist) || !errors.Is(want, stdos.ErrNotExist) {
		t.Fatalf("CreateTemp %v, Go %v", got, want)
	}
}

func TestCreateTempRename(t *testing.T) {
	dir := t.TempDir()
	f, err := CreateTemp(dir, ".decrypt-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(f.Name())
	if filepath.Dir(f.Name()) != dir || !strings.HasPrefix(base, ".decrypt-") || !strings.HasSuffix(base, ".tmp") || len(base) <= len(".decrypt-.tmp") {
		t.Fatalf("name %q", f.Name())
	}
	st, err := f.Stat()
	if err != nil || st.Mode().Perm()&0o077 != 0 || st.Name() != base {
		t.Fatalf("stat %v %v", st, err)
	}
	if _, err := f.Write([]byte("plain")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := WriteFile(out, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Rename(f.Name(), out); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile(out); err != nil || string(data) != "plain" {
		t.Fatalf("%q %v", data, err)
	}
	if _, err := Stat(f.Name()); !goerrors.Is(err, ErrNotExist) {
		t.Fatal(err)
	}
	g, err := CreateTemp(dir, "nosuffix")
	if err != nil || !strings.HasPrefix(filepath.Base(g.Name()), "nosuffix") || filepath.Base(g.Name()) == "nosuffix" {
		t.Fatalf("%v %v", g, err)
	}
	g.Close()
	if err := Remove(g.Name()); err != nil {
		t.Fatal(err)
	}
	if err := Remove(g.Name()); !goerrors.Is(err, ErrNotExist) || err.Error() != "remove "+g.Name()+": no such file or directory" {
		t.Fatal(err)
	}
	h, err := CreateTemp("", "goalchemy-")
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	if filepath.Dir(h.Name()) != stdos.TempDir() {
		t.Errorf("default dir %q", h.Name())
	}
	Remove(h.Name())
}

func TestBasename(t *testing.T) {
	for _, c := range []struct{ in, want string }{{"", "."}, {"/", "/"}, {"a", "a"}, {"a/b", "b"}, {"a/b/", "b"}, {"/a", "a"}, {"a//b//", "b"}} {
		if got := basename(c.in); got != c.want || got != filepath.Base(c.in) {
			t.Errorf("basename(%q) = %q, want %q (Go %q)", c.in, got, c.want, filepath.Base(c.in))
		}
	}
}
