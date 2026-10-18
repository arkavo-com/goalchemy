package os

import (
	"github.com/eugenioenko/goalchemy/lib/os"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/io"
)

// Flags to OpenFile, combined with bitwise or. Exactly one of O_RDONLY,
// O_WRONLY and O_RDWR selects the access mode.
const (
	O_RDONLY int = os.RDONLY
	O_WRONLY int = os.WRONLY
	O_RDWR   int = os.RDWR
	O_APPEND int = os.APPEND
	O_CREATE int = os.CREATE
	O_EXCL   int = os.EXCL
	O_TRUNC  int = os.TRUNC
)

// ErrClosed is the error of an operation on a closed File, as in Go.
var ErrClosed = errors.New("file already closed")

var errNegativeOffset = errors.New("negative offset")
var errPatternHasSeparator = errors.New("pattern contains path separator")

// ModeDir marks a directory in a FileMode; ModePerm selects its permission
// bits. Other Go mode bits are not reported.
const (
	ModeDir  FileMode = 1 << 31
	ModePerm FileMode = 0o777
)

// IsDir reports whether m describes a directory.
func (m FileMode) IsDir() bool { return m&ModeDir != 0 }

// IsRegular reports whether m describes a regular file.
func (m FileMode) IsRegular() bool { return m&ModeDir == 0 }

// Perm returns the permission bits of m.
func (m FileMode) Perm() FileMode { return m & ModePerm }

// A FileInfo describes a file, as returned by Stat. Modification times are
// not reported.
type FileInfo interface {
	Name() string
	Size() int64
	Mode() FileMode
	IsDir() bool
}

type fileStat struct {
	name string
	size int64
	mode FileMode
}

func (s *fileStat) Name() string   { return s.name }
func (s *fileStat) Size() int64    { return s.size }
func (s *fileStat) Mode() FileMode { return s.mode }
func (s *fileStat) IsDir() bool    { return s.mode.IsDir() }

// LinkError records an error during a Rename and the paths that caused it.
type LinkError struct {
	Op  string
	Old string
	New string
	Err error
}

func (e *LinkError) Error() string {
	return e.Op + " " + e.Old + " " + e.New + ": " + e.Err.Error()
}

func (e *LinkError) Unwrap() error { return e.Err }

// basename returns the last element of a slash-separated path, as
// filepath.Base does on Unix hosts.
func basename(path string) string {
	if path == "" {
		return "."
	}
	for len(path) > 0 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	if path == "" {
		return "/"
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// File is an open host file. Its methods implement io.Reader, io.ReaderAt,
// io.Writer, io.Seeker, io.StringWriter and io.Closer. A nil *File fails
// every method with ErrInvalid; a closed one with ErrClosed.
type File struct {
	handle int64
	name   string
	closed bool
}

// Open opens the named file for reading.
func Open(name string) (*File, error) { return OpenFile(name, O_RDONLY, 0) }

// Create creates or truncates the named file for reading and writing with
// mode 0o666 before the umask.
func Create(name string) (*File, error) {
	return OpenFile(name, O_RDWR|O_CREATE|O_TRUNC, 0o666)
}

// OpenFile opens the named file with the given flags and, when it creates
// the file, perm before the umask. Failures are *PathError values with op
// "open".
func OpenFile(name string, flag int, perm FileMode) (*File, error) {
	handle, status := os.Open(name, flag, uint32(perm))
	if status != os.OK {
		return nil, pathError("open", name, status)
	}
	return &File{handle: handle, name: name}, nil
}

// CreateTemp creates a new file in dir, or in the host's temporary
// directory when dir is empty, opened for reading and writing with mode
// 0o600. Its base name is pattern with the last "*" replaced by a random
// string, or pattern followed by one. The caller removes the file.
func CreateTemp(dir, pattern string) (*File, error) {
	prefix, suffix := pattern, ""
	for i := len(pattern) - 1; i >= 0; i-- {
		if pattern[i] == '*' {
			prefix, suffix = pattern[:i], pattern[i+1:]
			break
		}
	}
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '/' {
			return nil, &PathError{Op: "createtemp", Path: pattern, Err: errPatternHasSeparator}
		}
	}
	handle, name, status := os.CreateTemp(dir, prefix, suffix)
	if status != os.OK {
		if name == "" {
			name = dir + "/" + prefix + "*" + suffix
		}
		return nil, pathError("open", name, status)
	}
	return &File{handle: handle, name: name}, nil
}

// Stat describes the named file. Failures are *PathError values with op
// "stat".
func Stat(name string) (FileInfo, error) {
	size, mode, isDir, status := os.Stat(name)
	if status != os.OK {
		return nil, pathError("stat", name, status)
	}
	m := FileMode(mode) & ModePerm
	if isDir {
		m |= ModeDir
	}
	return &fileStat{name: basename(name), size: size, mode: m}, nil
}

// Remove removes the named file or empty directory.
func Remove(name string) error {
	if status := os.Remove(name); status != os.OK {
		return pathError("remove", name, status)
	}
	return nil
}

// Rename renames oldpath to newpath, replacing a file at newpath. Failures
// are *LinkError values.
func Rename(oldpath, newpath string) error {
	if status := os.Rename(oldpath, newpath); status != os.OK {
		return &LinkError{Op: "rename", Old: oldpath, New: newpath, Err: sysErr(status)}
	}
	return nil
}

// Name returns the name the file was opened with.
func (f *File) Name() string {
	if f == nil {
		return "<nil>"
	}
	return f.name
}

func (f *File) check(op string) error {
	if f == nil {
		return ErrInvalid
	}
	if f.closed {
		return &PathError{Op: op, Path: f.name, Err: ErrClosed}
	}
	return nil
}

func (f *File) wrap(op string, status int) error {
	return pathError(op, f.name, status)
}

// Read reads up to len(b) bytes into b and returns the count. At the end of
// the file it returns 0, io.EOF.
func (f *File) Read(b []byte) (int, error) {
	if err := f.check("read"); err != nil {
		return 0, err
	}
	if len(b) == 0 {
		return 0, nil
	}
	max := len(b)
	if max > os.MaxChunk {
		max = os.MaxChunk
	}
	data, eof, status := os.Read(f.handle, max)
	if status != os.OK {
		return 0, f.wrap("read", status)
	}
	if eof {
		return 0, io.EOF
	}
	return copy(b, data), nil
}

// ReadAt reads len(b) bytes starting at offset off, without moving the
// file's offset. It returns io.EOF with the count when the file ends first.
func (f *File) ReadAt(b []byte, off int64) (int, error) {
	if err := f.check("readat"); err != nil {
		return 0, err
	}
	if off < 0 {
		return 0, &PathError{Op: "readat", Path: f.name, Err: errNegativeOffset}
	}
	n := 0
	for n < len(b) {
		max := len(b) - n
		if max > os.MaxChunk {
			max = os.MaxChunk
		}
		data, eof, status := os.ReadAt(f.handle, off+int64(n), max)
		if status != os.OK {
			return n, f.wrap("readat", status)
		}
		if eof {
			return n, io.EOF
		}
		n += copy(b[n:], data)
	}
	return n, nil
}

// Write writes b at the file's offset, or at its end when it was opened with
// O_APPEND, and returns the bytes written. A short write without a host
// failure returns io.ErrShortWrite.
func (f *File) Write(b []byte) (int, error) {
	if err := f.check("write"); err != nil {
		return 0, err
	}
	n := 0
	for n < len(b) {
		end := len(b)
		if end-n > os.MaxChunk {
			end = n + os.MaxChunk
		}
		chunk := b[n:end]
		w, status := os.Write(f.handle, chunk)
		n += w
		if status != os.OK {
			return n, f.wrap("write", status)
		}
		if w < len(chunk) {
			return n, io.ErrShortWrite
		}
	}
	return n, nil
}

// WriteString writes s.
func (f *File) WriteString(s string) (int, error) { return f.Write([]byte(s)) }

// Seek sets the offset for the next Read or Write relative to the start
// (io.SeekStart), the current offset (io.SeekCurrent) or the end
// (io.SeekEnd), and returns the new offset.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	if err := f.check("seek"); err != nil {
		return 0, err
	}
	pos, status := os.Seek(f.handle, offset, whence)
	if status != os.OK {
		return 0, f.wrap("seek", status)
	}
	return pos, nil
}

// Stat describes the file by its name.
func (f *File) Stat() (FileInfo, error) {
	if f == nil {
		return nil, ErrInvalid
	}
	if f.closed {
		return nil, &PathError{Op: "stat", Path: f.name, Err: ErrClosed}
	}
	return Stat(f.name)
}

// Sync commits the file's written data to the storage device.
func (f *File) Sync() error {
	if err := f.check("sync"); err != nil {
		return err
	}
	if status := os.Sync(f.handle); status != os.OK {
		return f.wrap("sync", status)
	}
	return nil
}

// Close closes the file. A second Close fails with ErrClosed.
func (f *File) Close() error {
	if err := f.check("close"); err != nil {
		return err
	}
	f.closed = true
	if status := os.Close(f.handle); status != os.OK {
		return f.wrap("close", status)
	}
	return nil
}
