package main

// goalchemy:gate cooperative

func fib(ch chan int, n int) int {
	if n < 2 {
		ch <- n
		return <-ch
	}
	return fib(ch, n-1) + fib(ch, n-2)
}

func step(ch chan int, x int) int {
	ch <- x
	return (<-ch*31 + 7) % 1000003
}

func main() {
	ch := make(chan int, 1)
	acc := 0
	for i := 0; i < 2000000; i++ {
		acc = step(ch, acc+i)
	}
	println("step", acc)
	println("fib", fib(ch, 27))
}
