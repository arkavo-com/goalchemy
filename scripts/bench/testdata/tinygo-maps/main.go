package main

func main() {
	m := make(map[int64]int64)
	sum := int64(0)
	for i := int64(0); i < 1000000; i++ {
		m[i%1024] = i
		sum += m[(i+1)%1024]
		if i%3 == 0 {
			delete(m, (i+2)%1024)
		}
	}
	println("maps", sum, len(m))
}
