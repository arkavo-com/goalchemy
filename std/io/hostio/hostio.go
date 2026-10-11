// Package hostio adapts host streams passed to library exports as std/io
// interfaces. The compiler includes it in libraries whose exports take
// std/io parameters; programs do not import it directly.
//
// Each operation is a host callback named "goalchemy.stream.<id>" carrying a
// one-byte opcode: 'r' with an 8-byte count, 'w' with data, 's' with an
// 8-byte offset and 1-byte whence, or 'c'. Every reply starts with a status
// byte: 0 for success, 1 for a read that reached end of stream, or 2 for a
// host error followed by its message. Read data, or the 8-byte big-endian
// write count or seek position, follows a success status.
package hostio

import (
	"github.com/eugenioenko/goalchemy/lib/callback"
	"github.com/eugenioenko/goalchemy/lib/context"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/io"
	"github.com/eugenioenko/goalchemy/std/strconv"
)

// MaxChunk bounds the bytes moved by one host operation, within every
// target's callback reply limit.
const MaxChunk = 128<<10 - 16

var errReply = errors.New("hostio: malformed host reply")

// Stream is a host stream identified by a per-call registry number.
type Stream struct {
	name string
}

func newStream(id int64) *Stream {
	return &Stream{name: "goalchemy.stream." + strconv.FormatInt(id, 10)}
}

// call returns the reply after its status byte and whether the host
// reached end of stream.
func (s *Stream) call(request []byte) ([]byte, bool, error) {
	reply, err := callback.Request(context.Background(), s.name, request)
	if err != nil {
		return nil, false, err
	}
	if len(reply) == 0 {
		return nil, false, errReply
	}
	switch reply[0] {
	case 0:
		return reply[1:], false, nil
	case 1:
		return reply[1:], true, nil
	case 2:
		return nil, false, errors.New(string(reply[1:]))
	}
	return nil, false, errReply
}

func putInt(b []byte, v int64) {
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}

func getInt(b []byte) int64 {
	var v int64
	for i := 0; i < 8; i++ {
		v = v<<8 | int64(b[i])
	}
	return v
}

func (s *Stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := len(p)
	if n > MaxChunk-1 {
		n = MaxChunk - 1
	}
	req := make([]byte, 9)
	req[0] = 'r'
	putInt(req[1:], int64(n))
	reply, eof, err := s.call(req)
	if err != nil {
		return 0, err
	}
	if len(reply) > n {
		return 0, errReply
	}
	copied := copy(p, reply)
	if eof {
		return copied, io.EOF
	}
	return copied, nil
}

func (s *Stream) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) || len(p) == 0 {
		chunk := p[written:]
		if len(chunk) > MaxChunk-1 {
			chunk = chunk[:MaxChunk-1]
		}
		req := make([]byte, 1+len(chunk))
		req[0] = 'w'
		copy(req[1:], chunk)
		reply, _, err := s.call(req)
		if err != nil {
			return written, err
		}
		if len(reply) != 8 {
			return written, errReply
		}
		n := getInt(reply)
		if n < 0 || n > int64(len(chunk)) {
			return written, errReply
		}
		written += int(n)
		if int(n) < len(chunk) {
			return written, io.ErrShortWrite
		}
		if len(p) == 0 {
			break
		}
	}
	return written, nil
}

func (s *Stream) Seek(offset int64, whence int) (int64, error) {
	req := make([]byte, 10)
	req[0] = 's'
	putInt(req[1:], offset)
	req[9] = byte(whence)
	reply, _, err := s.call(req)
	if err != nil {
		return 0, err
	}
	if len(reply) != 8 {
		return 0, errReply
	}
	return getInt(reply), nil
}

func (s *Stream) Close() error {
	_, _, err := s.call([]byte{'c'})
	return err
}

// Reader returns the host stream id as an io.Reader.
func Reader(id int64) io.Reader { return newStream(id) }

// Writer returns the host stream id as an io.Writer.
func Writer(id int64) io.Writer { return newStream(id) }

// ReadCloser returns the host stream id as an io.ReadCloser.
func ReadCloser(id int64) io.ReadCloser { return newStream(id) }

// WriteCloser returns the host stream id as an io.WriteCloser.
func WriteCloser(id int64) io.WriteCloser { return newStream(id) }

// ReadSeeker returns the host stream id as an io.ReadSeeker.
func ReadSeeker(id int64) io.ReadSeeker { return newStream(id) }

// ReadSeekCloser returns the host stream id as an io.ReadSeekCloser.
func ReadSeekCloser(id int64) io.ReadSeekCloser { return newStream(id) }

// WriteSeeker returns the host stream id as an io.WriteSeeker.
func WriteSeeker(id int64) io.WriteSeeker { return newStream(id) }

// ReadWriter returns the host stream id as an io.ReadWriter.
func ReadWriter(id int64) io.ReadWriter { return newStream(id) }
