package main

// goalchemy:gate cooperative

func mix(x int) int { return (x*1103515245 + 12345) % 2147483648 }

func main() {
	ch := make(chan int, 1)
	acc := 1
	for i := 0; i < 30000000; i++ {
		acc = mix(acc ^ i)
	}
	ch <- acc
	println("cpu", <-ch)
}
