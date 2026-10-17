package main

// goalchemy:gate cooperative

import "github.com/eugenioenko/goalchemy/lib/task"

func main() {
	var f task.Future[func() int]
	println("before")
	println(task.Await(f))
}
