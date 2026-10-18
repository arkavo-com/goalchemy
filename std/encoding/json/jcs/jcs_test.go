package jcs

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/eugenioenko/goalchemy/std/encoding/jsonvalue"
)

func TestVectors(t *testing.T) {
	inputs, err := filepath.Glob("testdata/*.input.json")
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no test vectors: %v", err)
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".input.json")
		t.Run(name, func(t *testing.T) {
			input, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(strings.TrimSuffix(in, ".input.json") + ".output.json")
			if err != nil {
				t.Fatal(err)
			}
			got, err := Transform(input)
			if err != nil {
				t.Fatalf("Transform: %v", err)
			}
			if string(got) != strings.TrimRight(string(want), "\n") {
				t.Fatalf("got  %s\nwant %s", got, want)
			}
			again, err := Transform(got)
			if err != nil || string(again) != string(got) {
				t.Fatalf("not idempotent: %s %v", again, err)
			}
		})
	}
}

func TestNumbers(t *testing.T) {
	cases := []struct{ bits, want string }{
		{"4340000000000001", "9007199254740994"},
		{"4340000000000002", "9007199254740996"},
		{"444b1ae4d6e2ef50", "1e+21"},
		{"3eb0c6f7a0b5ed8d", "0.000001"},
		{"3eb0c6f7a0b5ed8c", "9.999999999999997e-7"},
		{"8000000000000000", "0"},
		{"0000000000000000", "0"},
		{"3ff0000000000000", "1"},
		{"bff0000000000000", "-1"},
		{"3fb999999999999a", "0.1"},
		{"7fefffffffffffff", "1.7976931348623157e+308"},
		{"0000000000000001", "5e-324"},
		{"c1e0000000000000", "-2147483648"},
		{"4415af1d78b58c40", "100000000000000000000"},
		{"3fe8000000000000", "0.75"},
		{"c00c000000000000", "-3.5"},
	}
	for _, c := range cases {
		bits, err := strconv.ParseUint(c.bits, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		got := string(appendES6(nil, math.Float64frombits(bits)))
		if got != c.want {
			t.Errorf("%s: got %s want %s", c.bits, got, c.want)
		}
	}
	for _, text := range []string{"333333333.33333329", "1E30", "4.50", "2e-3", "0.000000000000000000000000001", "1e21", "1e-7", "123456789012345678901234567890", "-0", "-0.0", "1.0e+2"} {
		got, err := Transform([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		f, _ := strconv.ParseFloat(text, 64)
		want := strconv.FormatFloat(f, 'g', -1, 64)
		if f == 0 {
			want = "0"
		}
		round, err := strconv.ParseFloat(string(got), 64)
		if err != nil || round != f {
			t.Errorf("%s: %s does not round-trip (%v)", text, got, err)
		}
		if strings.ContainsAny(string(got), "E") || strings.Contains(string(got), ".0e") {
			t.Errorf("%s: %s is not ES6 form (%s)", text, got, want)
		}
	}
	for _, text := range []string{"1e400", "-1e400", "1e999999999"} {
		if _, err := Transform([]byte(text)); err == nil {
			t.Errorf("%s: expected a NumberError", text)
		} else if _, ok := err.(*NumberError); !ok {
			t.Errorf("%s: got %T %v", text, err, err)
		}
	}
}

func TestKeyOrder(t *testing.T) {
	got, err := Transform([]byte(`{"€":1,"😂":2,"דּ":3,"a":4,"A":5,"":6,"ab":7,"b":8,"ö":9}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"\":6,\"A\":5,\"a\":4,\"ab\":7,\"b\":8,\"ö\":9,\"€\":1,\"😂\":2,\"\ufb33\":3}"
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// A supplementary character sorts by its surrogate pair, below U+FB33.
	got, err = Transform([]byte(`{"\ufb33":1,"\ud83d\ude02":2,"\ue000":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\"\U0001F602\":2,\"\ue000\":3,\"\ufb33\":1}" {
		t.Fatalf("surrogate order: %s", got)
	}
}

func TestStrings(t *testing.T) {
	got, err := Transform([]byte(`"\u0000\u001f\u007f\u0080 </>&'\/\b\t\n\f\r\"\\"`))
	if err != nil {
		t.Fatal(err)
	}
	want := "\"\\u0000\\u001f\u007f\u0080 </>&'/\\b\\t\\n\\f\\r\\\"\\\\\""
	if string(got) != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestErrors(t *testing.T) {
	for _, text := range []string{``, `{`, `{"a":1,"a":2}`, `[1,]`, `nul`, `"\ud800`} {
		if _, err := Transform([]byte(text)); err == nil {
			t.Errorf("%q: expected an error", text)
		}
	}
	if _, err := Encode(jsonvalue.Value{}); err == nil {
		t.Error("invalid Value: expected an error")
	}
	if _, err := Encode(jsonvalue.Number("1e400")); err == nil {
		t.Error("overflowing Number: expected an error")
	}
}

func TestEncodeValue(t *testing.T) {
	v := jsonvalue.Object("z", jsonvalue.Int(1), "a", jsonvalue.Array(jsonvalue.Float(2.5), jsonvalue.Null()), "m", jsonvalue.String("x\ny"))
	got, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":[2.5,null],"m":"x\ny","z":1}` {
		t.Fatalf("got %s", got)
	}
}
