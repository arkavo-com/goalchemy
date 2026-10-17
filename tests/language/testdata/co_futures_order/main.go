package main

// goalchemy:gate cooperative

import (
	"github.com/eugenioenko/goalchemy/lib/task"
	"github.com/eugenioenko/goalchemy/lib/runtime"
)

func main() {
	started := false
	f := task.Start(func() int { started = true; return 7 })
	println("immediate", started)
	runtime.Gosched()
	println("ready", started)
	go func() { println("queued") }()
	println("fast", task.Await(f))
	runtime.Gosched()
	gate := make(chan int)
	pending := task.Start(func() int { return <-gate })
	go func() { println("waiter", 1, task.Await(pending)) }()
	go func() { println("waiter", 2, task.Await(pending)) }()
	runtime.Gosched()
	gate <- 9
	runtime.Gosched()
	runtime.Gosched()
	println("done")
}
