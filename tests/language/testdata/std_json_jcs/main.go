package main

import (
	"github.com/eugenioenko/goalchemy/std/encoding/json/jcs"
	"github.com/eugenioenko/goalchemy/std/encoding/jsonvalue"
)

func show(label, text string) {
	out, err := jcs.Transform([]byte(text))
	if err != nil {
		println(label + ": error " + err.Error())
		return
	}
	println(label + ": " + string(out))
}

func main() {
	show("literals", ` [ null , true , false ] `)
	show("arrays", "[\n  56,\n  {\n    \"d\": true,\n    \"10\": null,\n    \"1\": [ ]\n  }\n]")
	show("structures", `{"1": {"f": {"f": "hi","F": 5} ,"\n": 56.0}, "10": { }, "": "empty", "a": { }, "111": [ {"e": "yes","E": "no" } ], "A": { }}`)
	show("french", `{"peach": "This sorting order", "péché": "is wrong according to French", "pêche": "but canonicalization MUST", "sin": "ignore locale"}`)
	show("weird", `{"\u20ac": "Euro Sign", "\r": "Carriage Return", "\u000a": "Newline", "1": "One", "\u0080": "Control\u007f", "\ud83d\ude02": "Smiley", "\u00f6": "Latin Small Letter O With Diaeresis", "\ufb33": "Hebrew Letter Dalet With Dagesh", "</script>": "Browser Challenge"}`)
	show("values", `{"numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001], "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/", "literals": [null, true, false]}`)
	show("numbers", `[0, -0, 1, -1, 0.1, 0.75, 1e21, 1e-7, 123456789012345678, 9007199254740994, 1.7976931348623157e308, 5e-324, 100000000000000000000, 0.000001, 9.999999999999997e-7, -2147483648]`)
	show("unicode", `{"Unnormalized Unicode":"A\u030a", "surrogates": "\ud83d\ude02", "escapes": "\u0000\u001f\u007f\u2028\b\t\n\f\r\"\\/", "lone": "\ud800x"}`)
	show("empty", `{}`)
	show("nested", `{"b":{"y":[{"q":1,"p":[]},{}],"x":"\u0041"},"a":[[],[[]]]}`)
	show("overflow", `[1e400]`)
	show("duplicate", `{"a":1,"a":2}`)
	show("invalid", `{"a":`)
	v := jsonvalue.Object("z", jsonvalue.Int(1), "a", jsonvalue.Array(jsonvalue.Float(2.5), jsonvalue.Null()), "m", jsonvalue.String("x\ny"))
	out, err := jcs.Encode(v)
	if err != nil {
		println("encode: error " + err.Error())
	} else {
		println("encode: " + string(out))
	}
}
