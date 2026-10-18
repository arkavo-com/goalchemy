// SPDX-License-Identifier: Apache-2.0

// Package os reads and writes whole host files and opens files as handles. Results carry a status code
// instead of an error so every host reports the same failure classes;
// std/os turns them into *PathError values.
package os

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// MaxFileBytes is the largest file ReadFile returns and WriteFile accepts.
const MaxFileBytes = 1 << 30

// Status codes. Hosts without a file system report Unsupported.
const (
	OK          = 0
	NotExist    = 1
	Exist       = 2
	Permission  = 3
	IsDir       = 4
	NotDir      = 5
	TooLarge    = 6
	Unsupported = 7
	Invalid     = 8
	IO          = 9
)

// ReadFile returns the contents of the named file.
func ReadFile(name string) ([]byte, int) {
	if strings.IndexByte(name, 0) >= 0 {
		return nil, Invalid
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, status(err)
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > MaxFileBytes {
		return nil, TooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, status(err)
	}
	if len(data) > MaxFileBytes {
		return nil, TooLarge
	}
	return data, OK
}

// WriteFile writes data to the named file, creating it with perm (before the
// process umask) if necessary and truncating it otherwise.
func WriteFile(name string, data []byte, perm uint32) int {
	if strings.IndexByte(name, 0) >= 0 {
		return Invalid
	}
	if len(data) > MaxFileBytes {
		return TooLarge
	}
	if err := os.WriteFile(name, data, fs.FileMode(perm&0o777)); err != nil {
		return status(err)
	}
	return OK
}

func status(err error) int {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return NotExist
	case errors.Is(err, fs.ErrExist):
		return Exist
	case errors.Is(err, fs.ErrPermission):
		return Permission
	case errors.Is(err, syscall.EISDIR):
		return IsDir
	case errors.Is(err, syscall.ENOTDIR):
		return NotDir
	case errors.Is(err, syscall.EFBIG):
		return TooLarge
	case errors.Is(err, errors.ErrUnsupported):
		return Unsupported
	case errors.Is(err, fs.ErrInvalid), errors.Is(err, syscall.EINVAL):
		return Invalid
	}
	return IO
}

// Flags for Open, combined with bitwise or. Exactly one of RDONLY, WRONLY and
// RDWR selects the access mode; the others modify it.
const (
	RDONLY = 0
	WRONLY = 1
	RDWR   = 2
	APPEND = 4
	CREATE = 8
	EXCL   = 16
	TRUNC  = 32
)

// MaxChunk is the most bytes one Read, ReadAt or Write moves (1 MiB).
const MaxChunk = 1 << 20

var (
	mu    sync.Mutex
	files = map[int64]*os.File{}
	last  int64
)

func lookup(file int64) (*os.File, int) {
	mu.Lock()
	defer mu.Unlock()
	f, ok := files[file]
	if !ok {
		return nil, Invalid
	}
	return f, OK
}

func goFlag(flag int) (int, bool) {
	if flag&^(WRONLY|RDWR|APPEND|CREATE|EXCL|TRUNC) != 0 || flag&(WRONLY|RDWR) == WRONLY|RDWR {
		return 0, false
	}
	out := 0
	for _, m := range []struct{ ours, gos int }{
		{WRONLY, os.O_WRONLY}, {RDWR, os.O_RDWR}, {APPEND, os.O_APPEND},
		{CREATE, os.O_CREATE}, {EXCL, os.O_EXCL}, {TRUNC, os.O_TRUNC},
	} {
		if flag&m.ours != 0 {
			out |= m.gos
		}
	}
	return out, true
}

func register(f *os.File) int64 {
	mu.Lock()
	defer mu.Unlock()
	last++
	files[last] = f
	return last
}

// Open opens the named file with the access and modifier flags above and,
// when CREATE applies, the low nine bits of perm before the process umask.
// It returns a handle for the other file functions; Close releases it.
func Open(name string, flag int, perm uint32) (int64, int) {
	if strings.IndexByte(name, 0) >= 0 {
		return 0, Invalid
	}
	gf, ok := goFlag(flag)
	if !ok {
		return 0, Invalid
	}
	f, err := os.OpenFile(name, gf, os.FileMode(perm&0o777))
	if err != nil {
		return 0, status(err)
	}
	return register(f), OK
}

// Read returns up to max (1..MaxChunk) bytes from the file's current offset
// and advances it. eof reports the end of the file; data is then empty.
func Read(file int64, max int) ([]byte, bool, int) {
	f, s := lookup(file)
	if s != OK {
		return nil, false, s
	}
	if max < 1 || max > MaxChunk {
		return nil, false, Invalid
	}
	buf := make([]byte, max)
	n, err := f.Read(buf)
	if n > 0 {
		return buf[:n], false, OK
	}
	if err == io.EOF {
		return []byte{}, true, OK
	}
	if err != nil {
		return nil, false, status(err)
	}
	return []byte{}, false, OK
}

// ReadAt returns up to max (1..MaxChunk) bytes starting at offset without
// moving the file's offset. eof reports that offset is at or past the end.
func ReadAt(file int64, offset int64, max int) ([]byte, bool, int) {
	f, s := lookup(file)
	if s != OK {
		return nil, false, s
	}
	if max < 1 || max > MaxChunk || offset < 0 {
		return nil, false, Invalid
	}
	buf := make([]byte, max)
	n, err := f.ReadAt(buf, offset)
	if n > 0 {
		return buf[:n], false, OK
	}
	if err == io.EOF {
		return []byte{}, true, OK
	}
	if err != nil {
		return nil, false, status(err)
	}
	return []byte{}, false, OK
}

// Write writes data (at most MaxChunk bytes) at the file's current offset,
// or at its end when it was opened with APPEND, and returns the bytes
// written before any failure.
func Write(file int64, data []byte) (int, int) {
	f, s := lookup(file)
	if s != OK {
		return 0, s
	}
	if len(data) > MaxChunk {
		return 0, Invalid
	}
	n, err := f.Write(data)
	if err != nil {
		return n, status(err)
	}
	return n, OK
}

// Seek sets the file's offset to offset relative to the start (whence 0),
// the current offset (1) or the end (2) and returns the new offset.
func Seek(file int64, offset int64, whence int) (int64, int) {
	f, s := lookup(file)
	if s != OK {
		return 0, s
	}
	if whence < 0 || whence > 2 {
		return 0, Invalid
	}
	pos, err := f.Seek(offset, whence)
	if err != nil {
		return 0, status(err)
	}
	return pos, OK
}

// Sync flushes the file's written data to the storage device.
func Sync(file int64) int {
	f, s := lookup(file)
	if s != OK {
		return s
	}
	if err := f.Sync(); err != nil {
		return status(err)
	}
	return OK
}

// Close closes the file and releases its handle. An unknown or already
// released handle reports Invalid.
func Close(file int64) int {
	mu.Lock()
	f, ok := files[file]
	delete(files, file)
	mu.Unlock()
	if !ok {
		return Invalid
	}
	if err := f.Close(); err != nil {
		return status(err)
	}
	return OK
}

// Stat describes the named file: its size in bytes, its permission bits and
// whether it is a directory.
func Stat(name string) (int64, int, bool, int) {
	if strings.IndexByte(name, 0) >= 0 {
		return 0, 0, false, Invalid
	}
	st, err := os.Stat(name)
	if err != nil {
		return 0, 0, false, status(err)
	}
	return st.Size(), int(st.Mode().Perm()), st.IsDir(), OK
}

// CreateTemp creates a new file in dir, or in the host's temporary directory
// when dir is empty, whose name is prefix, a random string and suffix, opened
// for reading and writing with permission 0600. It returns the handle and the
// file's path; on failure the path is the last name tried, or empty.
func CreateTemp(dir string, prefix string, suffix string) (int64, string, int) {
	if strings.IndexByte(dir+prefix+suffix, 0) >= 0 || strings.ContainsRune(prefix+suffix, filepath.Separator) {
		return 0, "", Invalid
	}
	f, err := os.CreateTemp(dir, prefix+"*"+suffix)
	if err != nil {
		name := ""
		if pe, ok := err.(*os.PathError); ok {
			name = pe.Path
		}
		return 0, name, status(err)
	}
	return register(f), f.Name(), OK
}

// Remove removes the named file or empty directory.
func Remove(name string) int {
	if strings.IndexByte(name, 0) >= 0 {
		return Invalid
	}
	if err := os.Remove(name); err != nil {
		return status(err)
	}
	return OK
}

// Rename renames oldpath to newpath, replacing a file at newpath.
func Rename(oldpath string, newpath string) int {
	if strings.IndexByte(oldpath+newpath, 0) >= 0 {
		return Invalid
	}
	if err := os.Rename(oldpath, newpath); err != nil {
		return status(err)
	}
	return OK
}
