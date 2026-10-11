package http

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestExchangeStreams(t *testing.T) {
	const size = 96 << 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload":
			h := sha256.New()
			n, _ := io.Copy(h, r.Body)
			w.Header().Set("X-Length", strconv.FormatInt(n, 10))
			w.Header().Set("X-Chunked", strconv.FormatBool(len(r.TransferEncoding) > 0))
			w.Write(h.Sum(nil))
		case "/download":
			buf := make([]byte, 1<<16)
			for i := range buf {
				buf[i] = byte(i)
			}
			for sent := 0; sent < size; sent += len(buf) {
				w.Write(buf)
			}
		case "/slow":
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	chunk := make([]byte, MaxChunk)
	for i := range chunk {
		chunk[i] = byte(i * 7)
	}
	for _, length := range []int64{-1, size} {
		e, err := Open(context.Background(), "POST", server.URL+"/upload", []string{"Content-Type", "application/octet-stream"}, length, 60000, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := sha256.New()
		for sent := 0; sent < size; sent += len(chunk) {
			if err := e.Write(chunk); err != nil {
				t.Fatal(err)
			}
			want.Write(chunk)
		}
		status, headers, err := e.Response()
		if err != nil || status != 200 {
			t.Fatal(status, err)
		}
		var got []byte
		for {
			p, eof, err := e.Read(MaxChunk)
			if err != nil {
				t.Fatal(err)
			}
			if eof {
				break
			}
			got = append(got, p...)
		}
		if string(got) != string(want.Sum(nil)) || !hasHeader(headers, "X-Length", strconv.Itoa(size)) || !hasHeader(headers, "X-Chunked", strconv.FormatBool(length < 0)) {
			t.Fatal(length, headers)
		}
		e.Close()
		e.Close()
		if err := e.Write(nil); err != errClosed {
			t.Fatal(err)
		}
	}
	e, err := Open(context.Background(), "GET", server.URL+"/download", nil, 0, 60000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Read(10); err != errState {
		t.Fatal("read before response", err)
	}
	if status, _, err := e.Response(); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	total := 0
	for {
		p, eof, err := e.Read(MaxChunk)
		if err != nil {
			t.Fatal(err)
		}
		if eof {
			break
		}
		if len(p) > MaxChunk {
			t.Fatal(len(p))
		}
		total += len(p)
	}
	e.Close()
	if total != size {
		t.Fatal(total)
	}
	e, _ = Open(context.Background(), "POST", server.URL+"/upload", nil, 10, 5000, nil)
	if err := e.Write(make([]byte, 11)); err != errState {
		t.Fatal("overlong write", err)
	}
	e.Write(make([]byte, 5))
	if _, _, err := e.Response(); err != errState {
		t.Fatal("short body", err)
	}
	e.Close()
	e, _ = Open(context.Background(), "GET", server.URL+"/slow", nil, 0, 100, nil)
	if status, _, err := e.Response(); err != nil || status != 200 {
		t.Fatal(status, err)
	}
	start := time.Now()
	if _, _, err := e.Read(10); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("whole-exchange timeout", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout delayed")
	}
	e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	e, _ = Open(ctx, "GET", server.URL+"/slow", nil, 0, 60000, nil)
	e.Response()
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, _, err := e.Read(10); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel", err)
	}
	e.Close()
	var nilExchange *Exchange
	nilExchange.Close()
	for _, bad := range []func() (*Exchange, error){
		func() (*Exchange, error) { return Open(context.Background(), "GET", server.URL, nil, 5, 1000, nil) },
		func() (*Exchange, error) { return Open(context.Background(), "POST", server.URL, nil, -2, 1000, nil) },
		func() (*Exchange, error) { return Open(context.Background(), "PUT", server.URL, nil, 0, 1000, nil) },
		func() (*Exchange, error) {
			return Open(context.Background(), "GET", server.URL, nil, 0, MaxExchangeMillis+1, nil)
		},
		func() (*Exchange, error) {
			return Open(context.Background(), "GET", server.URL, []string{"Host", "x"}, 0, 1000, nil)
		},
		func() (*Exchange, error) {
			return Open(context.Background(), "GET", server.URL, nil, 0, 1000, []string{RootCAs})
		},
	} {
		if e, err := bad(); err == nil || e != nil {
			t.Fatal("invalid open accepted")
		}
	}
}

func hasHeader(headers []string, name, value string) bool {
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == name && headers[i+1] == value {
			return true
		}
	}
	return false
}
