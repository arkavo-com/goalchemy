package http

import (
	"context"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/std/io"
	gstrings "github.com/eugenioenko/goalchemy/std/strings"
)

type closer struct {
	*gstrings.Reader
	closed bool
}

func (c *closer) Close() error { c.closed = true; return nil }

func TestDo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := stdio.ReadAll(r.Body)
		w.Header().Set("X-Length", r.Header.Get("Content-Length"))
		w.Write([]byte(r.Method + " " + string(b)))
	}))
	defer server.Close()
	body := &closer{Reader: gstrings.NewReader(strings.Repeat("x", 300000))}
	resp, err := Do(context.Background(), &Request{Method: "POST", URL: server.URL, Body: body, ContentLength: 300000, TimeoutMillis: 5000})
	if err != nil || resp.Status != 200 || !body.closed {
		t.Fatal(resp, err, body.closed)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != "POST "+strings.Repeat("x", 300000) {
		t.Fatal(len(got), err)
	}
	resp.Body.Close()
	if _, err := resp.Body.Read(make([]byte, 1)); err != errClosed {
		t.Fatal(err)
	}
	resp, err = Do(context.Background(), &Request{Method: "GET", URL: server.URL, TimeoutMillis: 5000})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "GET " {
		t.Fatal(string(got))
	}
	if _, err := Do(context.Background(), nil); err != errNilRequest {
		t.Fatal(err)
	}
	if _, err := Do(context.Background(), &Request{Method: "PUT", URL: server.URL, TimeoutMillis: 5000}); err == nil {
		t.Fatal("PUT accepted")
	}
}
