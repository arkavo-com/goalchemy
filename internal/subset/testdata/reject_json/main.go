package main

import (
	"github.com/eugenioenko/goalchemy/std/encoding/json"
	"github.com/eugenioenko/goalchemy/std/encoding/jsonvalue"
)

type Fn struct {
	F func()
}

type Ignored struct {
	F func() `json:"-"`
	f func()
}

type Keys struct {
	M map[float64]int
}

type Zero struct {
	N int `json:"n,omitzero"`
}

type BadName struct {
	N int `json:"'quoted'"`
}

type Inline struct {
	N int `json:",inline"`
}

type Twice struct {
	N int `json:",omitempty,omitempty"`
}

type Hidden struct {
	n int `json:"n"`
}

type NumberString struct {
	N json.Number `json:",string"`
}

type Point struct{ X, Y int }

type Text int

func (*Text) MarshalText() ([]byte, error) { return nil, nil }

type Value int

type hiddenBase struct{ X int }

type HiddenPointer struct {
	*hiddenBase
}

type FloatKeys struct {
	M map[float64]string
}

type TextKey int

func (k *TextKey) UnmarshalText([]byte) error { return nil }

type TextKeys struct {
	M map[TextKey]int
}

func (Value) MarshalJSON() ([]byte, error) { return nil, nil }

func main() {
	_, _ = json.Marshal(Fn{}) // want GCS006
	_, _ = json.Marshal(Ignored{})
	_, _ = json.Marshal(Keys{})                        // want GCS006
	_, _ = json.Marshal(Zero{})                        // want GCS006
	_, _ = json.Marshal(BadName{})                     // want GCS006
	_, _ = json.Marshal(Inline{})                      // want GCS006
	_, _ = json.Marshal(Twice{})                       // want GCS006
	_, _ = json.Marshal(Hidden{})                      // want GCS006
	_, _ = json.Marshal(NumberString{})                // want GCS006
	_, _ = json.Marshal([]any{Point{}})                // want GCS006
	_, _ = json.Marshal(map[string]any{"p": &Point{}}) // want GCS006
	_, _ = json.Marshal(any(Text(1)))                  // want GCS006
	_, _ = json.Marshal(struct{ A any }{A: Point{}})   // want GCS006
	_, _ = json.Marshal([]any{1, "s", nil, []byte("b"), map[string]any{"v": Value(1)}, jsonvalue.Null(), json.Number("1")})
	_, _ = json.MarshalIndent(Point{}, "", " ")
	f := json.Marshal // want GCS006
	_ = f
	defer json.Marshal(1) // want GCS006

	var p Point
	data := []byte(`{}`)
	_ = json.Unmarshal(data, &p)
	_ = json.Unmarshal(data, p)                // want GCS006
	_ = json.Unmarshal(data, &Fn{})            // want GCS006
	_ = json.Unmarshal(data, &FloatKeys{})     // want GCS006
	_ = json.Unmarshal(data, &HiddenPointer{}) // want GCS006
	_ = json.Unmarshal(data, &Zero{})          // want GCS006
	_ = json.Unmarshal(data, any(&p))          // want GCS006
	_ = json.Unmarshal(data, &TextKeys{})
	_ = json.Unmarshal(data, any(new(map[string]any)))
	_ = json.Unmarshal(data, nil)
	var v jsonvalue.Value
	_ = json.Unmarshal(data, &v)
	u := json.Unmarshal // want GCS006
	_ = u

	enc := json.NewEncoder(nil)
	_ = enc.Encode(Point{})
	_ = enc.Encode(Fn{})           // want GCS006
	_ = enc.Encode([]any{Point{}}) // want GCS006
	encode := enc.Encode           // want GCS006
	_ = encode
	defer enc.Encode(1) // want GCS006
	dec := json.NewDecoder(nil)
	_ = dec.Decode(&p)
	_ = dec.Decode(p)       // want GCS006
	_ = dec.Decode(&Fn{})   // want GCS006
	_ = dec.Decode(any(&p)) // want GCS006
	_ = dec.Decode(any(new([]any)))
	decode := dec.Decode // want GCS006
	_ = decode
	var s struct{ *json.Decoder }
	_ = s.Decode(&p)
	_ = s.Decode(p) // want GCS006
}
