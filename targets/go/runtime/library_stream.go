package rt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strconv"
	"sync"
)

// streamCallers marks goroutines running a host stream method, so a
// library call made from inside one fails fast instead of deadlocking.
var streamCallers sync.Map

func goroutineID() uint64 {
	var buf [64]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		b = b[:i]
	}
	id, _ := strconv.ParseUint(string(b), 10, 64)
	return id
}

func reentrantStream() bool {
	_, ok := streamCallers.Load(goroutineID())
	return ok
}

// pendingStreams carries the streams of one call until its instance is known.
type pendingStreams struct {
	streams []Callback
	ids     []int64
}

// registerStreams assigns instance stream ids and returns the registry the
// call sees: the call's callbacks plus every stream of the instance.
func registerStreams(inst *Instance, registry Callbacks, pending *pendingStreams) Callbacks {
	if inst.streams == nil && pending == nil {
		return registry
	}
	if pending != nil {
		if inst.streams == nil {
			inst.streams = Callbacks{}
		}
		for i, cb := range pending.streams {
			if cb == nil {
				continue
			}
			inst.nextStream++
			pending.ids[i] = inst.nextStream
			inst.streams["goalchemy.stream."+strconv.FormatInt(inst.nextStream, 10)] = cb
		}
	}
	out := make(Callbacks, len(registry)+len(inst.streams))
	for k, v := range registry {
		out[k] = v
	}
	for k, v := range inst.streams {
		out[k] = v
	}
	return out
}

func streamInt(b []byte) int64 {
	var v int64
	for _, c := range b[:8] {
		v = v<<8 | int64(c)
	}
	return v
}

func streamBytes(v int64) []byte {
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return b
}

var errStreamRequest = errors.New("hostio: malformed stream request")

func serveStream(stream any, req []byte) ([]byte, error) {
	if len(req) == 0 {
		return nil, errStreamRequest
	}
	switch req[0] {
	case 'r':
		r, ok := stream.(io.Reader)
		if !ok || len(req) != 9 {
			return nil, errStreamRequest
		}
		n := streamInt(req[1:])
		if n < 0 || n >= 128<<10 {
			return nil, errStreamRequest
		}
		buf := make([]byte, 1+n)
		k, err := r.Read(buf[1:])
		buf[0] = 0
		if k < 0 || k > int(n) {
			return nil, errors.New("hostio: invalid read count")
		}
		if err == io.EOF {
			buf[0] = 1
		} else if err != nil {
			return nil, err
		}
		return buf[:1+k], nil
	case 'w':
		w, ok := stream.(io.Writer)
		if !ok {
			return nil, errStreamRequest
		}
		data := append([]byte(nil), req[1:]...)
		k, err := w.Write(data)
		if err != nil {
			return nil, err
		}
		return append([]byte{0}, streamBytes(int64(k))...), nil
	case 's':
		s, ok := stream.(io.Seeker)
		if !ok || len(req) != 10 {
			return nil, errStreamRequest
		}
		pos, err := s.Seek(streamInt(req[1:]), int(req[9]))
		if err != nil {
			return nil, err
		}
		return append([]byte{0}, streamBytes(pos)...), nil
	case 'c':
		c, ok := stream.(io.Closer)
		if !ok {
			return nil, errStreamRequest
		}
		return []byte{0}, c.Close()
	}
	return nil, errStreamRequest
}

// HostStream serves hostio requests on a host stream. Buffers are copied in
// both directions; host io.EOF is reported to the source as io.EOF.
func HostStream(stream any) Callback {
	if stream == nil {
		return nil
	}
	return func(_ context.Context, req []byte, settle func([]byte, error)) func() {
		id := goroutineID()
		streamCallers.Store(id, true)
		defer streamCallers.Delete(id)
		reply, err := func() (reply []byte, err error) {
			defer func() {
				if p := recover(); p != nil {
					reply, err = nil, errors.New("hostio: host stream panicked")
				}
			}()
			return serveStream(stream, req)
		}()
		if err != nil {
			reply = append([]byte{2}, err.Error()...)
		}
		settle(reply, nil)
		return func() {}
	}
}

// StreamOps runs the source std/io operations behind a host view of a
// handle. Each operation is a library call on the handle.
type StreamOps struct {
	Read  func(h *Handle, n int) ([]byte, bool, error)
	Write func(h *Handle, p []byte) (int, error)
	Seek  func(h *Handle, offset int64, whence int) (int64, error)
	Close func(h *Handle) error
}

// StreamObj holds a source std/io value returned to the host, so interface
// values of any dynamic type can key the instance table.
type StreamObj struct{ V any }

// Stream is a host io view of a source reader or writer. Its methods are
// safe for concurrent use; calls are serialized with the instance's others.
type Stream struct {
	h   *Handle
	ops *StreamOps
}

// NewStream returns the view of h through ops.
func NewStream(h *Handle, ops *StreamOps) *Stream { return &Stream{h: h, ops: ops} }

var errUnsupported = &LibraryError{Kind: "unsupported"}

func (s *Stream) Read(p []byte) (int, error) {
	if s.ops.Read == nil {
		return 0, errUnsupported
	}
	if len(p) == 0 {
		return 0, nil
	}
	data, eof, err := s.ops.Read(s.h, len(p))
	n := copy(p, data)
	if err == nil && eof {
		err = io.EOF
	}
	return n, err
}

func (s *Stream) Write(p []byte) (int, error) {
	if s.ops.Write == nil {
		return 0, errUnsupported
	}
	written := 0
	for written < len(p) {
		chunk := p[written:]
		if len(chunk) > 1<<20 {
			chunk = chunk[:1<<20]
		}
		n, err := s.ops.Write(s.h, append([]byte(nil), chunk...))
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (s *Stream) Seek(offset int64, whence int) (int64, error) {
	if s.ops.Seek == nil {
		return 0, errUnsupported
	}
	return s.ops.Seek(s.h, offset, whence)
}

// Close runs the source Close, if any, and releases the handle.
func (s *Stream) Close() error { return s.ops.Close(s.h) }

// WriteTo copies the stream to w in large chunks.
func (s *Stream) WriteTo(w io.Writer) (int64, error) {
	var total int64
	for {
		data, eof, err := s.ops.Read(s.h, 1<<20)
		if len(data) > 0 {
			n, werr := w.Write(data)
			total += int64(n)
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			return total, err
		}
		if eof {
			return total, nil
		}
	}
}

// As returns s with the methods of the std/io interface kind. Close is
// always present: it releases the handle.
func (s *Stream) As(kind string) any {
	switch kind {
	case "Reader", "ReadCloser":
		return struct {
			io.Reader
			io.WriterTo
			io.Closer
		}{s, s, s}
	case "Writer", "WriteCloser":
		return struct {
			io.Writer
			io.Closer
		}{s, s}
	case "ReadSeeker", "ReadSeekCloser":
		return struct {
			io.Reader
			io.WriterTo
			io.Seeker
			io.Closer
		}{s, s, s, s}
	case "WriteSeeker":
		return struct {
			io.Writer
			io.Seeker
			io.Closer
		}{s, s, s}
	case "ReadWriter":
		return struct {
			io.Reader
			io.WriterTo
			io.Writer
			io.Closer
		}{s, s, s, s}
	}
	return s
}
