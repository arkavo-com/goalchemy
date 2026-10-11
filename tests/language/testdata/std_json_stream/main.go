package main

import (
	"github.com/eugenioenko/goalchemy/std/bytes"
	"github.com/eugenioenko/goalchemy/std/encoding/json"
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/io"
	"github.com/eugenioenko/goalchemy/std/strconv"
	"github.com/eugenioenko/goalchemy/std/strings"
)

type Item struct {
	ID    int               `json:"id"`
	Name  string            `json:"name,omitempty"`
	Tags  []string          `json:"tags"`
	Attrs map[string]any    `json:"attrs,omitempty"`
	Next  *Item             `json:"next,omitempty"`
	Raw   json.RawMessage   `json:"raw,omitempty"`
	Score float64           `json:"score,string"`
	Extra map[string]string `json:"-"`
}

type Level int

func (l Level) MarshalJSON() ([]byte, error) {
	if l < 0 {
		return nil, errors.New("negative level")
	}
	return []byte(`{"level":` + strconv.Itoa(int(l)) + `}`), nil
}

type Note struct {
	Text  string `json:"text"`
	Level Level  `json:"level"`
}

type chunked struct {
	data string
	n    int
}

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := min(c.n, len(p), len(c.data))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

type failing struct{ calls int }

func (f *failing) Write(p []byte) (int, error) {
	f.calls++
	if f.calls > 1 {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

type brokenReader struct{ data string }

func (b *brokenReader) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, errors.New("connection reset")
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func describe(err error) string {
	if err == nil {
		return "<nil>"
	}
	s := err.Error()
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		s += " | " + ute.Value + " | " + ute.Type + " | " + strconv.FormatInt(ute.Offset, 10) + " | " + ute.Struct + " | " + ute.Field
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		s += " @" + strconv.FormatInt(se.Offset, 10)
	}
	var ie *json.InvalidUnmarshalError
	if errors.As(err, &ie) {
		s += " invalid(" + ie.Type + ")"
	}
	if err == io.EOF {
		s += " (io.EOF)"
	}
	if err == io.ErrUnexpectedEOF {
		s += " (io.ErrUnexpectedEOF)"
	}
	return s
}

func token(t json.Token) string {
	switch v := t.(type) {
	case nil:
		return "null"
	case json.Delim:
		return "delim " + v.String()
	case bool:
		return "bool " + strconv.FormatBool(v)
	case float64:
		return "float " + strconv.FormatFloat(v, 'g', -1, 64)
	case json.Number:
		return "number " + v.String()
	case string:
		return "string " + strconv.Quote(v)
	}
	return "?"
}

func encoded(b []byte, err error) string {
	if err != nil {
		return "marshal: " + err.Error()
	}
	return string(b)
}

func show(v any) string { return encoded(json.Marshal(v)) }

func showItem(it Item) string { return encoded(json.Marshal(it)) }

func showInts(sl []int) string { return encoded(json.Marshal(sl)) }

func numberKinds(v any) string {
	switch x := v.(type) {
	case json.Number:
		return "Number(" + x.String() + ")"
	case float64:
		return "float64"
	case []any:
		s := "["
		for _, e := range x {
			s += numberKinds(e) + " "
		}
		return s + "]"
	case map[string]any:
		return "map(" + numberKinds(x["n"]) + ")"
	}
	return "other"
}

func encoding() {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	next := &Item{ID: 2, Tags: nil, Score: 0.5}
	println("encode item", describe(enc.Encode(Item{ID: 1, Name: "<a&b>", Tags: []string{"x", "y"}, Attrs: map[string]any{"k": []any{1.5, "v", nil, true}}, Next: next, Raw: json.RawMessage(`{"r" : [1, 2]}`), Score: 12.25})))
	println("encode slice", describe(enc.Encode([]int{1, 2, 3})))
	println("encode number", describe(enc.Encode(42)))
	var dynamic any = map[string]any{"html": "<tag>", "list": []any{"a&b"}}
	println("encode any", describe(enc.Encode(dynamic)))
	println("encode marshaler", describe(enc.Encode(Note{Text: "n", Level: 3})))
	println("encode marshaler error", describe(enc.Encode(Note{Text: "n", Level: -1})))
	println("encode nil", describe(enc.Encode(nil)))
	enc.SetEscapeHTML(false)
	println("encode raw html", describe(enc.Encode(map[string]string{"<k>": "a&b<c>"})))
	println("encode raw html any", describe(enc.Encode(dynamic)))
	println("encode raw html marshaler", describe(enc.Encode(json.RawMessage(`"<x>"`))))
	enc.SetIndent(">", "  ")
	println("encode indent", describe(enc.Encode(Item{ID: 3, Tags: []string{}, Attrs: map[string]any{"a": map[string]any{}}})))
	enc.SetIndent("", "")
	println("encode compact", describe(enc.Encode(Note{Text: "&"})))
	print(buf.String())

	f := &failing{}
	fenc := json.NewEncoder(f)
	println("write 1", describe(fenc.Encode(1)))
	println("write 2", describe(fenc.Encode(2)))
	println("write 3", describe(fenc.Encode(3)), f.calls)

	type encoder interface{ Encode(v any) error }
	var sb strings.Builder
	var e encoder = json.NewEncoder(&sb)
	println("interface encode", describe(e.Encode(map[string]any{"z": 1, "a": []any{"<"}})), sb.String() == "{\"a\":[\"\\u003c\"],\"z\":1}\n")
}

func decoding() {
	stream := `{"id":1,"name":"a","tags":["x"],"score":"1.5"} {"id":2,"tags":null,"next":{"id":3,"score":"2"},"raw":[1, 2],"score":"0"}
	[1,2] {"id":"bad","score":"3"} {"id":4,"score":"nope"} 7 "tail"`
	dec := json.NewDecoder(&chunked{data: stream, n: 5})
	for i := 0; i < 3; i++ {
		var it Item
		err := dec.Decode(&it)
		println("item", i, describe(err), showItem(it), dec.InputOffset())
	}
	var sl []int
	println("slice", describe(dec.Decode(&sl)), showInts(sl))
	var bad Item
	println("bad", describe(dec.Decode(&bad)), showItem(bad))
	var n int
	println("n", describe(dec.Decode(&n)), n, dec.More())
	var s string
	println("s", describe(dec.Decode(&s)), s, dec.InputOffset())
	println("eof", describe(dec.Decode(&s)))
	println("eof again", describe(dec.Decode(&s)))

	dec = json.NewDecoder(strings.NewReader(`{"id":1,"bogus":2,"name":"x"} {"id":2}`))
	dec.DisallowUnknownFields()
	var it Item
	println("strict", describe(dec.Decode(&it)), showItem(it))
	println("strict next", describe(dec.Decode(&it)), showItem(it))

	dec = json.NewDecoder(strings.NewReader(`[1, 2.50, {"n": 1e400}, 12345678901234567890] {"n":-0}`))
	dec.UseNumber()
	var a any
	println("number", describe(dec.Decode(&a)), numberKinds(a), show(a))
	var m map[string]any
	println("number map", describe(dec.Decode(&m)), numberKinds(m))
	dec = json.NewDecoder(strings.NewReader(`[1, 1e400]`))
	println("float", describe(dec.Decode(&a)), numberKinds(a))

	dec = json.NewDecoder(strings.NewReader(`{"a": 1} {"b": [true, null]} x`))
	var holder any = &m
	println("holder", describe(dec.Decode(holder)), show(m))
	println("holder 2", describe(dec.Decode(holder)), show(m))
	println("holder 3", describe(dec.Decode(holder)))
	println("sticky", describe(dec.Decode(holder)))

	type decoder interface{ Decode(v any) error }
	var di decoder = json.NewDecoder(strings.NewReader(`{"i": [1, "x"]} 5`))
	var im map[string]any
	println("interface decode", describe(di.Decode(&im)), show(im), describe(di.Decode(&im)))

	dec = json.NewDecoder(strings.NewReader(`1`))
	println("nil target", describe(dec.Decode(nil)))
	var np *Item
	dec = json.NewDecoder(strings.NewReader(`{} 2`))
	println("nil pointer", describe(dec.Decode(np)))
	println("after nil pointer", describe(dec.Decode(&n)), n)

	for _, doc := range []string{`[1,2`, `{"a" 1}`, `[1,]`, `"abc`, `tru`, `1 2 x`, `]`, `{"a":1,}`, "\"\\x\"", `[1}`, ` `, `01`} {
		dec = json.NewDecoder(&chunked{data: doc, n: 2})
		out := strconv.Quote(doc) + ":"
		for i := 0; i < 3; i++ {
			var v any
			err := dec.Decode(&v)
			out += " " + show(v) + " " + describe(err) + ";"
			if err != nil {
				break
			}
		}
		println(out)
	}

	dec = json.NewDecoder(&brokenReader{data: `{"a":`})
	println("broken", describe(dec.Decode(&a)))
	dec = json.NewDecoder(&brokenReader{data: `{"a":1}`})
	println("broken ok", describe(dec.Decode(&a)), show(a), describe(dec.Decode(&a)))
}

func tokens() {
	doc := ` {"items": [ {"id": 1, "tags": ["a"], "score": "1"}, {"id": 2, "score": "2"} ], "n": 3.5, "ok": true, "nil": null, "s": "x\u00e9", "big": 1e400} [] `
	dec := json.NewDecoder(&chunked{data: doc, n: 7})
	for i := 0; i < 3; i++ {
		t, err := dec.Token()
		println("tok", token(t), describe(err), dec.InputOffset(), dec.More())
	}
	for dec.More() {
		var it Item
		err := dec.Decode(&it)
		println("elem", describe(err), showItem(it), dec.InputOffset())
	}
	for {
		t, err := dec.Token()
		rest, _ := io.ReadAll(dec.Buffered())
		println("tok", token(t), describe(err), dec.InputOffset(), strconv.Quote(string(rest)))
		if err != nil {
			break
		}
	}

	dec = json.NewDecoder(strings.NewReader(`{"a": [1, 2.5, "x"]} 7`))
	dec.UseNumber()
	for {
		t, err := dec.Token()
		println("num tok", token(t), describe(err), dec.InputOffset())
		if err != nil {
			break
		}
	}

	for _, doc := range []string{`[1,x]`, `{"a":1 "b"}`, `[1`, `{"a" 1}`, `[1}`, `{"a":1,}`, `[1,]`, `-`, `{1:2}`, `{"a"}`, `]`, `[1,2]]`, `{"a":1}:`} {
		dec = json.NewDecoder(&chunked{data: doc, n: 3})
		out := strconv.Quote(doc) + ":"
		for i := 0; i < 8; i++ {
			t, err := dec.Token()
			out += " " + token(t) + "|" + describe(err) + "|" + strconv.FormatInt(dec.InputOffset(), 10)
			if err != nil {
				out += " more=" + strconv.FormatBool(dec.More())
				break
			}
		}
		println(out)
	}

	dec = json.NewDecoder(strings.NewReader(`[1, 2] [3`))
	dec.Token()
	println("mixed", dec.More())
	var n int
	println("mixed decode", describe(dec.Decode(&n)), n)
	t, err := dec.Token()
	println("mixed tok", token(t), describe(err))
	println("mixed decode end", describe(dec.Decode(&n)))
	t, err = dec.Token()
	println("mixed close", token(t), describe(err))
	var sl []int
	println("mixed last", describe(dec.Decode(&sl)), showInts(sl))
	println("mixed more", dec.More(), describe(dec.Decode(&sl)))
}

func main() {
	encoding()
	decoding()
	tokens()
}
