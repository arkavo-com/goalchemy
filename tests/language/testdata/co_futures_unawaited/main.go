package main

// goalchemy:gate cooperative

import "github.com/eugenioenko/goalchemy/lib/task"

func main() {
	task.Start(func() { panic("future failed") })
	block := make(chan int)
	<-block
}
