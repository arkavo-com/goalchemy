package main

func main() {
	m := make(map[int]int)
	for i := 0; i < 32; i++ {
		m[i] = i * i
	}
	total := 0
	for n := 0; n < 8; n++ {
		for i := 0; i < 32; i++ {
			total += m[i]
		}
	}
	println(total, len(m))
}
