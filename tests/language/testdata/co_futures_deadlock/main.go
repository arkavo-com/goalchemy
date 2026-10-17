package main

// goalchemy:gate cooperative

import "github.com/eugenioenko/goalchemy/lib/task"

func main() {
	f := task.Start(func() int { block := make(chan int); return <-block })
	println("before")
	println(task.Await(f))
}
