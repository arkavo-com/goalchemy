package main

func main() {
	x := uint(1)
	for i := 0; i < 200000; i++ {
		x = (x*1103515245 + 12345) ^ (x >> 13)
	}
	println(x)
}
