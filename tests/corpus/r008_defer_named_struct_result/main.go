// goalchemy:gate cooperative
package main

type value struct {
	kind int
	text string
}

func resolve(ticks chan int, v value) (rv value) {
	defer func() {
		if r := recover(); r != nil {
			rv = value{kind: -1, text: "recovered"}
		}
	}()
	for i := 0; i < 3; i++ {
		if v.kind == 0 {
			return v
		}
		v = value{kind: v.kind - <-ticks, text: v.text + "."}
	}
	return value{kind: 9, text: "too many"}
}

func pair(ticks chan int, v value) (first value, count int) {
	defer func() { count++ }()
	first = resolve(ticks, v)
	return first, len(first.text)
}

func main() {
	ticks := make(chan int, 16)
	for i := 0; i < 16; i++ {
		ticks <- 1
	}
	a := resolve(ticks, value{kind: 2, text: "x"})
	println(a.kind, a.text)
	b := resolve(ticks, value{kind: 7, text: "y"})
	println(b.kind, b.text)
	c, n := pair(ticks, value{kind: 1, text: "z"})
	println(c.kind, c.text, n)
}
