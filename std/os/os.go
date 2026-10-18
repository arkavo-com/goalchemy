// Package os reads and writes whole files on the host file system. Errors are
// *PathError values whose Err matches ErrNotExist, ErrExist, ErrPermission or
// errors.ErrUnsupported through errors.Is, as in Go. Hosts without a file
// system, such as browsers, fail every call with an error matching
// errors.ErrUnsupported.
package os

import (
	"github.com/eugenioenko/goalchemy/lib/os"
	"github.com/eugenioenko/goalchemy/std/errors"
)

// MaxFileBytes is the largest file ReadFile returns and WriteFile accepts.
const MaxFileBytes = os.MaxFileBytes

// Portable analogs of the io/fs errors.
var (
	ErrInvalid    = errors.New("invalid argument")
	ErrPermission = errors.New("permission denied")
	ErrExist      = errors.New("file already exists")
	ErrNotExist   = errors.New("file does not exist")
)

// A FileMode holds permission bits; only the low nine bits are applied.
type FileMode uint32

// PathError records an error and the operation and file path that caused it.
type PathError struct {
	Op   string
	Path string
	Err  error
}

func (e *PathError) Error() string { return e.Op + " " + e.Path + ": " + e.Err.Error() }

func (e *PathError) Unwrap() error { return e.Err }

type sysError int

func (e sysError) Error() string {
	switch e {
	case os.NotExist:
		return "no such file or directory"
	case os.Exist:
		return "file exists"
	case os.Permission:
		return "permission denied"
	case os.IsDir:
		return "is a directory"
	case os.NotDir:
		return "not a directory"
	case os.TooLarge:
		return "file too large"
	case os.Unsupported:
		return "operation not supported"
	case os.Invalid:
		return "invalid argument"
	}
	return "input/output error"
}

func (e sysError) Is(target error) bool {
	switch e {
	case os.NotExist:
		return target == ErrNotExist
	case os.Exist:
		return target == ErrExist
	case os.Permission:
		return target == ErrPermission
	case os.Unsupported:
		return target == errors.ErrUnsupported
	}
	return false
}

func sysErr(status int) error {
	if status < os.NotExist || status > os.IO {
		status = os.IO
	}
	return sysError(status)
}

func pathError(op, name string, status int) error {
	return &PathError{Op: op, Path: name, Err: sysErr(status)}
}

// ReadFile reads the named file and returns its contents. A directory fails
// with op "read", as on Linux; other failures use op "open".
func ReadFile(name string) ([]byte, error) {
	data, status := os.ReadFile(name)
	switch status {
	case os.OK:
		return data, nil
	case os.IsDir, os.TooLarge:
		return nil, pathError("read", name, status)
	}
	return nil, pathError("open", name, status)
}

// WriteFile writes data to the named file, creating it if necessary. If the
// file does not exist, WriteFile creates it with permissions perm (before the
// umask); otherwise WriteFile truncates it before writing, without changing
// permissions.
func WriteFile(name string, data []byte, perm FileMode) error {
	switch status := os.WriteFile(name, data, uint32(perm)); status {
	case os.OK:
		return nil
	case os.TooLarge:
		return pathError("write", name, status)
	default:
		return pathError("open", name, status)
	}
}
