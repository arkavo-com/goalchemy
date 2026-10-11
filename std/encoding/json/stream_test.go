package json

import (
	gobytes "bytes"
	stdjson "encoding/json"
	"errors"
	goio "io"
	"math/rand"
	"reflect"
	"strconv"
	"testing"

	"github.com/eugenioenko/goalchemy/std/io"
)

type chunkReader struct {
	data    []byte
	sizes   []int
	native  bool
	failAt  int
	eofWith bool
}

var errBroken = errors.New("broken pipe")

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.failAt > 0 && len(c.data) <= c.failAt {
		return 0, errBroken
	}
	if len(c.data) == 0 {
		if c.native {
			return 0, goio.EOF
		}
		return 0, io.EOF
	}
	n := 1
	if len(c.sizes) > 0 {
		n, c.sizes = c.sizes[0], c.sizes[1:]
	}
	n = min(n, len(p), len(c.data))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	if c.eofWith && len(c.data) == 0 {
		if c.native {
			return n, goio.EOF
		}
		return n, io.EOF
	}
	return n, nil
}

func tokenText(v any) string {
	switch x := v.(type) {
	case Delim:
		return "D" + x.String()
	case stdjson.Delim:
		return "D" + x.String()
	case Number:
		return "N" + string(x)
	case stdjson.Number:
		return "N" + string(x)
	case string:
		return "S" + strconv.Quote(x)
	case float64:
		return "F" + strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return "B" + strconv.FormatBool(x)
	case nil:
		return "nil"
	}
	return "?"
}

func streamDocument(r *rand.Rand) string {
	s := ""
	for n := r.Intn(4); n >= 0; n-- {
		s += []string{"", " ", "\n", "\t "}[r.Intn(4)] + randomDocument(r, 0)
	}
	if r.Intn(3) == 0 {
		s = mutate(r, s)
	}
	return s
}

func TestDecoderMatchesEncodingJSON(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	for i := 0; i < 100000; i++ {
		doc := streamDocument(r)
		sizes := make([]int, 256)
		limit := []int{3, 40, 700}[r.Intn(3)]
		for j := range sizes {
			sizes[j] = 1 + r.Intn(limit)
		}
		failAt, eofWith := 0, r.Intn(2) == 0
		if r.Intn(8) == 0 {
			failAt = r.Intn(len(doc) + 1)
		}
		useNumber := r.Intn(2) == 0
		ops := make([]int, 24)
		for j := range ops {
			ops[j] = r.Intn(10)
		}
		got := NewDecoder(&chunkReader{data: []byte(doc), sizes: sizes, failAt: failAt, eofWith: eofWith})
		want := stdjson.NewDecoder(&chunkReader{data: []byte(doc), sizes: sizes, native: true, failAt: failAt, eofWith: eofWith})
		if useNumber {
			got.UseNumber()
			want.UseNumber()
		}
		var trace []string
		for _, op := range ops {
			var g, w string
			switch {
			case op < 5:
				gt, gerr := got.Token()
				wt, werr := want.Token()
				g, w = tokenText(gt)+" "+describeError(gerr), tokenText(wt)+" "+describeError(werr)
			case op < 7:
				var gv, wv any
				var gerr error
				if op == 5 {
					gerr = decoderAny(got, &gv)
				} else {
					gerr = got.Decode(&gv)
				}
				werr := want.Decode(&wv)
				g, w = describeError(gerr), describeError(werr)
				if !reflect.DeepEqual(normalize(gv), normalize(wv)) {
					g += " value differs"
				}
			case op < 8:
				g, w = strconv.FormatBool(got.More()), strconv.FormatBool(want.More())
			case op < 9:
				g, w = strconv.FormatInt(got.InputOffset(), 10), strconv.FormatInt(want.InputOffset(), 10)
			default:
				gb, _ := io.ReadAll(got.Buffered())
				wb, _ := goio.ReadAll(want.Buffered())
				g, w = string(gb), string(wb)
			}
			trace = append(trace, w)
			if g != w {
				t.Fatalf("stream %q sizes %v ops %v useNumber %v:\n trace %q\n got %s\nwant %s", doc, sizes[:4], ops, useNumber, trace, g, w)
			}
		}
	}
}

func normalize(v any) any {
	switch x := v.(type) {
	case stdjson.Number:
		return Number(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = normalize(e)
		}
		return out
	}
	return v
}

type strictTarget struct {
	A int
	B any
}

func TestDecoderOptions(t *testing.T) {
	doc := `{"A":1,"C":2,"B":3} {"a":2,"B":{"x":1.50}} [1] {"A":"x","Z":1}`
	got := NewDecoder(&chunkReader{data: []byte(doc), sizes: []int{3, 5, 7}})
	want := stdjson.NewDecoder(&chunkReader{data: []byte(doc), sizes: []int{3, 5, 7}, native: true})
	got.UseNumber()
	got.DisallowUnknownFields()
	want.UseNumber()
	want.DisallowUnknownFields()
	for i := 0; i < 5; i++ {
		var g, w strictTarget
		gerr := got.Decode(&g)
		werr := want.Decode(&w)
		if describeError(gerr) != describeError(werr) || g.A != w.A || tokenText(g.B) != tokenText(w.B) && !reflect.DeepEqual(normalize(g.B), normalize(w.B)) {
			t.Fatalf("Decode %d: got %+v %s, want %+v %s", i, g, describeError(gerr), w, describeError(werr))
		}
	}
}

func TestEncoderMatchesEncodingJSON(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		v := randomValue(r, 0)
		escape := r.Intn(2) == 0
		prefix, indent := []string{"", ">"}[r.Intn(2)], []string{"", "  ", "\t"}[r.Intn(3)]
		var want gobytes.Buffer
		we := stdjson.NewEncoder(&want)
		we.SetEscapeHTML(escape)
		we.SetIndent(prefix, indent)
		werr := we.Encode(v)
		for _, compiled := range []bool{false, true} {
			var got gobytes.Buffer
			ge := NewEncoder(&got)
			ge.SetEscapeHTML(escape)
			ge.SetIndent(prefix, indent)
			var gerr error
			if compiled {
				gerr = encoderAny(ge, v)
			} else {
				gerr = ge.Encode(v)
			}
			if (gerr == nil) != (werr == nil) || got.String() != want.String() {
				t.Fatalf("Encode(%#v) compiled=%v escape=%v indent=%q/%q:\n got %q %v\nwant %q %v", v, compiled, escape, prefix, indent, got.String(), gerr, want.String(), werr)
			}
		}
	}
}

type failingWriter struct{ n int }

func (f *failingWriter) Write(p []byte) (int, error) {
	f.n++
	if f.n > 1 {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

func TestEncoderStickyWriteError(t *testing.T) {
	w := &failingWriter{}
	enc := NewEncoder(w)
	if err := enc.Encode(1); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(2); err != io.ErrShortWrite {
		t.Fatalf("second Encode: %v", err)
	}
	if err := encoderAny(enc, 3); err != io.ErrShortWrite || w.n != 2 {
		t.Fatalf("third Encode: %v after %d writes", err, w.n)
	}
}

func TestDecoderDepthMatchesEncodingJSON(t *testing.T) {
	for _, n := range []int{9999, 10000, 10001} {
		doc := gobytes.Repeat([]byte("["), n)
		doc = append(doc, gobytes.Repeat([]byte("]"), n)...)
		for _, tokens := range []int{0, 1, 2} {
			got := NewDecoder(&chunkReader{data: doc, sizes: []int{700}})
			want := stdjson.NewDecoder(&chunkReader{data: doc, sizes: []int{700}, native: true})
			for i := 0; i < tokens; i++ {
				got.Token()
				want.Token()
			}
			var gv, wv any
			gerr := decoderAny(got, &gv)
			werr := want.Decode(&wv)
			if describeError(gerr) != describeError(werr) {
				t.Fatalf("depth %d after %d tokens: got %s, want %s", n, tokens, describeError(gerr), describeError(werr))
			}
		}
		got := NewDecoder(&chunkReader{data: doc, sizes: []int{700}})
		want := stdjson.NewDecoder(&chunkReader{data: doc, sizes: []int{700}, native: true})
		for i := 0; i < 2*n+1; i++ {
			gt, gerr := got.Token()
			wt, werr := want.Token()
			if g, w := tokenText(gt)+describeError(gerr), tokenText(wt)+describeError(werr); g != w {
				t.Fatalf("depth %d token %d: got %s, want %s", n, i, g, w)
			}
			if gerr != nil {
				break
			}
		}
	}
}
