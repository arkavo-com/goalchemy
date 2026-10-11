package main

var cache = map[string]string{}

var calls int

func fetch(key string) string {
	calls++
	cache[key] = key
	return cache[key]
}

func main() {
	println(fetch("a"), calls)
}
