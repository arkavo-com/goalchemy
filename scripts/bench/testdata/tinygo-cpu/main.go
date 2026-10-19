package main

func mix(x int64) int64 { return (x*1103515245 + 12345) % 2147483648 }

func main() {
	acc := int64(1)
	for i := 0; i < 30000000; i++ {
		acc = mix(acc ^ int64(i))
	}
	println("cpu", acc)
}
