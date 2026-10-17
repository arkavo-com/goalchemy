package main

// goalchemy:gate cooperative

func main() {
	ping, pong := make(chan int), make(chan int)
	done := make(chan int)
	go func() {
		for v := range ping {
			pong <- v + 1
		}
		close(pong)
	}()
	go func() {
		sum := 0
		for v := range pong {
			sum = (sum + v) % 1000003
		}
		done <- sum
	}()
	for i := 0; i < 300000; i++ {
		ping <- i
	}
	close(ping)
	println("pingpong", <-done)
}
