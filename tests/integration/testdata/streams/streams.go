// Package streams exercises host std/io streams passed to library exports.
package streams

import (
	"github.com/eugenioenko/goalchemy/std/io"
)

// Copy copies src to dst.
func Copy(dst io.Writer, src io.Reader) (int64, error) { return io.Copy(dst, src) }

// Sum adds every byte of r.
func Sum(r io.Reader) (int, error) {
	data, err := io.ReadAll(r)
	total := 0
	for _, b := range data {
		total += int(b)
	}
	return total, err
}

// Section seeks to off and reads n bytes.
func Section(r io.ReadSeeker, off int64, n int) ([]byte, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	k, err := io.ReadFull(r, buf)
	return buf[:k], err
}

// Drain reads r to the end and closes it.
func Drain(r io.ReadCloser) (int, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	return len(data), r.Close()
}

// AtEOF reports whether the first read of r returns io.EOF.
func AtEOF(r io.Reader) (bool, error) {
	_, err := r.Read(make([]byte, 4))
	return err == io.EOF, nil
}

// FirstError returns the message of the first read error of r.
func FirstError(r io.Reader) (string, error) {
	_, err := r.Read(make([]byte, 4))
	if err == nil {
		return "", nil
	}
	return err.Error(), nil
}

// Nil reports whether r is nil.
func Nil(r io.Reader) bool { return r == nil }

// Doc keeps a host stream across calls.
type Doc struct {
	r    io.ReadSeeker
	size int64
}

// Open measures r and keeps it.
func Open(r io.ReadSeeker) (*Doc, error) {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	return &Doc{r: r, size: size}, nil
}

func (d *Doc) Size() int64 { return d.size }

// ReadAt reads n bytes at off from the stream kept by Open.
func (d *Doc) ReadAt(off int64, n int) ([]byte, error) { return Section(d.r, off, n) }

// WriteTo copies the whole document to w.
func (d *Doc) WriteTo(w io.Writer) (int64, error) {
	if _, err := d.r.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	return io.Copy(w, d.r)
}
