// Package http sends HTTP requests whose bodies stream through std/io: the
// request body is read from an io.Reader and the response body is an
// io.ReadCloser, so neither is held in memory whole. It runs on lib/http
// exchanges, so calls suspend on the host HTTP client.
package http

import (
	"github.com/eugenioenko/goalchemy/lib/context"
	lhttp "github.com/eugenioenko/goalchemy/lib/http"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/io"
)

// Request is one GET or POST request.
type Request struct {
	Method string
	URL    string
	// Headers alternate names and values, with the rules of lib/http.Do.
	Headers []string
	// Body is the request body, or nil for none. It is closed after the
	// request when it is an io.Closer.
	Body io.Reader
	// ContentLength is the exact length of Body; zero or less with a Body
	// sends it chunked.
	ContentLength int64
	// Options are lib/http.DoWith transport options, such as a client
	// certificate, extra root CAs or a Unix socket.
	Options []string
	// TimeoutMillis bounds the whole exchange, including reading the
	// response body (1..lib/http.MaxExchangeMillis).
	TimeoutMillis int64
}

// Response is the response to a Request. Body must be closed.
type Response struct {
	Status  int
	Headers []string
	Body    io.ReadCloser
}

// chunk is the size of one request body write.
const chunk = 256 << 10

// Do sends req and returns the response once its headers arrive. The
// response body is read from the server as Body is read; the exchange ends
// when Body is closed, ctx is canceled or the timeout expires. Redirects are
// not followed and compression is neither requested nor decoded.
func Do(ctx context.Context, req *Request) (*Response, error) {
	if req == nil {
		return nil, errNilRequest
	}
	length := int64(0)
	if req.Body != nil {
		length = req.ContentLength
		if length <= 0 {
			length = -1
		}
	}
	e, err := lhttp.Open(ctx, req.Method, req.URL, req.Headers, length, req.TimeoutMillis, req.Options)
	if err != nil {
		closeBody(req.Body)
		return nil, err
	}
	if req.Body != nil {
		err = send(e, req.Body)
		closeBody(req.Body)
		if err != nil {
			e.Close()
			return nil, err
		}
	}
	status, headers, err := e.Response()
	if err != nil {
		e.Close()
		return nil, err
	}
	return &Response{Status: status, Headers: headers, Body: &body{e: e}}, nil
}

func send(e *lhttp.Exchange, r io.Reader) error {
	buf := make([]byte, chunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if werr := e.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func closeBody(r io.Reader) {
	if c, ok := r.(io.Closer); ok {
		c.Close()
	}
}

type body struct {
	e      *lhttp.Exchange
	eof    bool
	closed bool
}

func (b *body) Read(p []byte) (int, error) {
	if b.closed {
		return 0, errClosed
	}
	if b.eof {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	n := len(p)
	if n > lhttp.MaxChunk {
		n = lhttp.MaxChunk
	}
	data, eof, err := b.e.Read(n)
	if err != nil {
		return 0, err
	}
	if eof {
		b.eof = true
		return 0, io.EOF
	}
	return copy(p, data), nil
}

func (b *body) Close() error {
	if !b.closed {
		b.closed = true
		b.e.Close()
	}
	return nil
}

var errClosed = errors.New("http: read on closed response body")
var errNilRequest = errors.New("http: nil request")
