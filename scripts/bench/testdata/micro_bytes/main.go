package main

func main() {
	b := make([]byte, 2048)
	for i := range b {
		b[i] = byte(i)
	}
	c := make([]byte, len(b))
	total := 0
	for n := 0; n < 100; n++ {
		copy(c, b)
		for _, v := range c {
			total += int(v)
		}
	}
	println(total, len(c))
}
