package main

var retained [][]byte

func main() {
	retained = make([][]byte, 128)
	sum := 0
	for i := 0; i < 200000; i++ {
		b := make([]byte, 1024)
		b[i%1024] = byte(i)
		retained[i%len(retained)] = b
		sum += int(retained[i%len(retained)][i%1024])
	}
	println("alloc", sum, len(retained))
}
