package main

// goalchemy:gate cooperative

import "github.com/eugenioenko/goalchemy/std/errors"

var trace = make(chan string, 100)

func note(s string) { trace <- s }

func drain() {
	for len(trace) > 0 {
		println(<-trace)
	}
}

func pausingDefers(ch chan int) (n int) {
	defer func() {
		ch <- 1
		n += <-ch
		note("defer 1")
	}()
	defer func() {
		note("defer 2")
		if r := recover(); r != nil {
			note("recovered " + r.(string))
			n = 10
		}
	}()
	ch <- 2
	<-ch
	panic("first")
}

func rethrow(ch chan int) {
	defer func() {
		r := recover()
		ch <- 0
		<-ch
		panic(r.(string) + " again")
	}()
	panic("inner")
}

func catchRethrow(ch chan int) (msg string) {
	defer func() {
		msg = recover().(string)
	}()
	rethrow(ch)
	return "unreachable"
}

func depth(ch chan int, n int) int {
	if n == 0 {
		return 0
	}
	ch <- n
	return <-ch%7 + depth(ch, n-1)
}

func worker(id int, in chan int, out chan int) {
	defer func() { out <- -id }()
	for v := range in {
		if v < 0 {
			panic(errors.New("negative"))
		}
		out <- v * id
	}
}

func guarded(id int, in chan int, out chan int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()
	worker(id, in, out)
	return nil
}

func main() {
	ch := make(chan int, 1)
	println("pausing defers", pausingDefers(ch))
	drain()
	println("rethrow", catchRethrow(ch))
	println("depth", depth(ch, 5000))
	in, out := make(chan int), make(chan int, 10)
	done := make(chan error)
	go func() { done <- guarded(3, in, out) }()
	in <- 1
	in <- 2
	in <- -1
	println("worker", (<-done).Error(), <-out, <-out, <-out)
	blocked := make(chan int)
	for i := 0; i < 3; i++ {
		go func() {
			defer println("never printed")
			<-blocked
		}()
	}
	sel := make(chan int)
	go func() {
		defer func() { recover(); sel <- 9 }()
		var nilMap map[string]int
		nilMap["x"] = 1
	}()
	select {
	case v := <-sel:
		println("select", v)
	case <-blocked:
		println("unreachable")
	}
	defer println("main deferred")
	defer func() {
		ch <- 5
		println("main pausing defer", <-ch)
		panic("second")
	}()
	panic("final")
}
