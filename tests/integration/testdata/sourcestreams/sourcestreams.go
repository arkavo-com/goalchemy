// Package sourcestreams exercises std/io readers and writers returned to the
// host, and handles that are readers or writers themselves.
package sourcestreams

import (
	"github.com/eugenioenko/goalchemy/std/bytes"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/io"
)

type pattern struct{ i int64 }

func (p *pattern) Read(b []byte) (int, error) {
	for k := range b {
		b[k] = byte(p.i % 251)
		p.i++
	}
	return len(b), nil
}

// Pattern returns a reader of n bytes where byte i is i%251.
func Pattern(n int64) io.Reader { return io.LimitReader(&pattern{}, n) }

// Text returns a seekable reader over s.
func Text(s string) io.ReadSeeker { return bytes.NewReader([]byte(s)) }

type failing struct{ left int }

func (f *failing) Read(b []byte) (int, error) {
	if f.left == 0 {
		return 0, errors.New("source failed")
	}
	n := copy(b, make([]byte, f.left))
	f.left -= n
	return n, nil
}

// Failing returns a reader that fails after n zero bytes.
func Failing(n int) io.Reader { return &failing{left: n} }

// Nil returns a nil reader.
func Nil() io.Reader { return nil }

// Through returns a reader over a host stream: host, then source, then host.
func Through(r io.Reader) io.ReadCloser { return io.NopCloser(r) }

// Sink is a handle that is a writer: it hashes and counts what it receives.
type Sink struct {
	count  int64
	hash   uint32
	closes int
	data   bytes.Buffer
	keep   bool
}

// NewSink returns a sink; keep makes it retain the bytes written.
func NewSink(keep bool) *Sink { return &Sink{hash: 2166136261, keep: keep} }

func (s *Sink) Write(p []byte) (int, error) {
	for _, b := range p {
		s.hash = (s.hash ^ uint32(b)) * 16777619
	}
	s.count += int64(len(p))
	if s.keep {
		s.data.Write(p)
	}
	return len(p), nil
}

// Count returns the number of bytes written.
func (s *Sink) Count() int64 { return s.count }

// Hash returns the FNV-1a hash of the bytes written.
func (s *Sink) Hash() int64 { return int64(s.hash) }

// Closes reports how many writers of the sink were closed.
func (s *Sink) Closes() int { return s.closes }

// Writer returns a writer into the sink that counts its Close.
func (s *Sink) Writer() io.WriteCloser { return &sinkWriter{s} }

// Contents returns a reader over the retained bytes, in the sink's instance.
func (s *Sink) Contents() io.Reader { return bytes.NewReader(s.data.Bytes()) }

type sinkWriter struct{ s *Sink }

func (w *sinkWriter) Write(p []byte) (int, error) { return w.s.Write(p) }
func (w *sinkWriter) Close() error {
	w.s.closes++
	return nil
}

// Hash returns the FNV-1a hash of the n-byte pattern, for checking copies.
func Hash(n int64) int64 {
	s := NewSink(false)
	io.Copy(s, Pattern(n))
	return s.Hash()
}

// Doc is a handle that is a seekable reader with a Close.
type Doc struct {
	r      *bytes.Reader
	reads  int
	closed bool
}

// Open returns a document over data.
func Open(data []byte) *Doc { return &Doc{r: bytes.NewReader(data)} }

func (d *Doc) Read(p []byte) (int, error) {
	d.reads++
	return d.r.Read(p)
}

func (d *Doc) Seek(offset int64, whence int) (int64, error) { return d.r.Seek(offset, whence) }

// Reads returns how many reads the document served.
func (d *Doc) Reads() int { return d.reads }

func (d *Doc) Close() error {
	if d.closed {
		return errors.New("closed twice")
	}
	d.closed = true
	return nil
}
