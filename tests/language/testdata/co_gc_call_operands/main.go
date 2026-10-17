package main

// goalchemy:gate cooperative

type value struct {
	a, b, c, d, e int
}

type limits struct {
	a, b, c, d int
}

func defaults() limits {
	return limits{1, 2, 3, 4}
}

func encode(v value, l limits) int {
	return v.e + l.d
}

func forward(v value) int {
	return encode(v, defaults())
}

func main() {
	println(forward(value{1, 2, 3, 4, 5}))
}
