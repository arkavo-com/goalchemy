package os

import (
	stdos "os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "f")
	f, s := Open(name, RDWR|CREATE|TRUNC, 0o640)
	if s != OK {
		t.Fatalf("open %d", s)
	}
	if n, s := Write(f, []byte("hello world")); n != 11 || s != OK {
		t.Fatalf("write %d %d", n, s)
	}
	if pos, s := Seek(f, 6, 0); pos != 6 || s != OK {
		t.Fatalf("seek %d %d", pos, s)
	}
	data, eof, s := Read(f, MaxChunk)
	if string(data) != "world" || eof || s != OK {
		t.Fatalf("read %q %v %d", data, eof, s)
	}
	data, eof, s = Read(f, 1)
	if len(data) != 0 || !eof || s != OK {
		t.Fatalf("read at end %q %v %d", data, eof, s)
	}
	data, eof, s = ReadAt(f, 0, 5)
	if string(data) != "hello" || eof || s != OK {
		t.Fatalf("readat %q %v %d", data, eof, s)
	}
	if data, eof, s := ReadAt(f, 11, 5); len(data) != 0 || !eof || s != OK {
		t.Fatalf("readat end %q %v %d", data, eof, s)
	}
	if pos, s := Seek(f, -5, 2); pos != 6 || s != OK {
		t.Fatalf("seek end %d %d", pos, s)
	}
	if s := Sync(f); s != OK {
		t.Fatalf("sync %d", s)
	}
	if size, mode, isDir, s := Stat(name); size != 11 || mode != 0o640&^umask(t) || isDir || s != OK {
		t.Fatalf("stat %d %o %v %d", size, mode, isDir, s)
	}
	if _, _, isDir, s := Stat(dir); !isDir || s != OK {
		t.Fatalf("stat dir %v %d", isDir, s)
	}
	if s := Close(f); s != OK {
		t.Fatalf("close %d", s)
	}
	if s := Close(f); s != Invalid {
		t.Fatalf("double close %d", s)
	}
	if _, _, s := Read(f, 1); s != Invalid {
		t.Fatalf("read closed %d", s)
	}
	a, s := Open(name, WRONLY|APPEND, 0)
	if s != OK {
		t.Fatal(s)
	}
	if n, s := Write(a, []byte("!")); n != 1 || s != OK {
		t.Fatal(n, s)
	}
	Close(a)
	if data, s := ReadFile(name); s != OK || string(data) != "hello world!" {
		t.Fatalf("%q %d", data, s)
	}
}

func umask(t *testing.T) int {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "mask")
	if s := WriteFile(probe, nil, 0o777); s != OK {
		t.Fatal(s)
	}
	st, err := stdos.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	return 0o777 &^ int(st.Mode().Perm())
}

func TestFileStatuses(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "f")
	if s := WriteFile(name, []byte("x"), 0o600); s != OK {
		t.Fatal(s)
	}
	if _, s := Open(filepath.Join(dir, "missing"), RDONLY, 0); s != NotExist {
		t.Errorf("missing %d", s)
	}
	if _, s := Open(name, WRONLY|CREATE|EXCL, 0o600); s != Exist {
		t.Errorf("excl %d", s)
	}
	if _, s := Open(name, RDWR|WRONLY, 0); s != Invalid {
		t.Errorf("bad access %d", s)
	}
	if _, s := Open(name, 1<<10, 0); s != Invalid {
		t.Errorf("bad flag %d", s)
	}
	if _, s := Open("a\x00b", RDONLY, 0); s != Invalid {
		t.Errorf("nul %d", s)
	}
	d, s := Open(dir, RDONLY, 0)
	if s != OK {
		t.Fatalf("open dir %d", s)
	}
	if _, _, s := Read(d, 1); s != IsDir {
		t.Errorf("read dir %d", s)
	}
	Close(d)
	f, _ := Open(name, RDONLY, 0)
	if _, _, s := Read(f, 0); s != Invalid {
		t.Errorf("zero read %d", s)
	}
	if _, _, s := Read(f, MaxChunk+1); s != Invalid {
		t.Errorf("huge read %d", s)
	}
	if _, _, s := ReadAt(f, -1, 1); s != Invalid {
		t.Errorf("negative readat %d", s)
	}
	if _, s := Seek(f, 0, 3); s != Invalid {
		t.Errorf("bad whence %d", s)
	}
	if _, s := Seek(f, -1, 0); s != Invalid {
		t.Errorf("negative seek %d", s)
	}
	if _, s := Write(f, []byte("x")); s != Permission && s != IO {
		t.Errorf("write read-only %d", s)
	}
	if _, s := Write(f, make([]byte, MaxChunk+1)); s != Invalid {
		t.Errorf("huge write %d", s)
	}
	Close(f)
	if _, _, _, s := Stat(filepath.Join(dir, "missing")); s != NotExist {
		t.Errorf("stat missing %d", s)
	}
	if _, _, _, s := Stat("a\x00b"); s != Invalid {
		t.Errorf("stat nul %d", s)
	}
	if s := Remove(filepath.Join(dir, "missing")); s != NotExist {
		t.Errorf("remove missing %d", s)
	}
	if s := Rename(filepath.Join(dir, "missing"), name); s != NotExist {
		t.Errorf("rename missing %d", s)
	}
	for _, s := range []int{Read2(99), Close(99), Sync(99)} {
		if s != Invalid {
			t.Errorf("unknown handle %d", s)
		}
	}
}

func Read2(file int64) int {
	_, _, s := Read(file, 1)
	return s
}

func TestTempRenameRemove(t *testing.T) {
	dir := t.TempDir()
	f, name, s := CreateTemp(dir, "pre-", ".tmp")
	if s != OK {
		t.Fatalf("createtemp %d", s)
	}
	base := filepath.Base(name)
	if filepath.Dir(name) != dir || !strings.HasPrefix(base, "pre-") || !strings.HasSuffix(base, ".tmp") || len(base) <= len("pre-.tmp") {
		t.Fatalf("temp name %q", name)
	}
	if _, mode, _, s := Stat(name); s != OK || mode&0o077 != 0 {
		t.Fatalf("temp mode %o %d", mode, s)
	}
	if n, s := Write(f, []byte("data")); n != 4 || s != OK {
		t.Fatal(n, s)
	}
	Close(f)
	g, other, s := CreateTemp(dir, "pre-", ".tmp")
	if s != OK || other == name {
		t.Fatalf("second temp %q %d", other, s)
	}
	Close(g)
	target := filepath.Join(dir, "final")
	if s := WriteFile(target, []byte("old"), 0o600); s != OK {
		t.Fatal(s)
	}
	if s := Rename(name, target); s != OK {
		t.Fatalf("rename %d", s)
	}
	if data, s := ReadFile(target); s != OK || string(data) != "data" {
		t.Fatalf("renamed %q %d", data, s)
	}
	if _, _, _, s := Stat(name); s != NotExist {
		t.Fatalf("old name %d", s)
	}
	if s := Remove(other); s != OK {
		t.Fatalf("remove %d", s)
	}
	if s := Remove(other); s != NotExist {
		t.Fatalf("remove again %d", s)
	}
	sub := filepath.Join(dir, "sub")
	if err := stdos.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if s := Remove(sub); s != OK {
		t.Fatalf("remove dir %d", s)
	}
	if _, _, s := CreateTemp(dir, "a/b", ""); s != Invalid {
		t.Errorf("separator %d", s)
	}
	if _, _, s := CreateTemp(filepath.Join(dir, "missing"), "x", ""); s != NotExist {
		t.Errorf("missing dir %d", s)
	}
	h, name, s := CreateTemp("", "goalchemy-", "")
	if s != OK {
		t.Fatalf("default dir %d", s)
	}
	Close(h)
	if filepath.Dir(name) != stdos.TempDir() {
		t.Errorf("default dir %q", name)
	}
	Remove(name)
}
