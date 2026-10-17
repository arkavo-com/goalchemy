package main

// goalchemy:gate cooperative

import "github.com/eugenioenko/goalchemy/lib/task"

func total(n int) int {
	sum := 0
	for i := 1; i <= n; i++ {
		if i%2 == 0 {
			sum += i
		}
	}
	return sum
}
func main() {
	a := task.Start(func() int { return total(10) })
	b := task.Start(func() int { return total(20) })
	println(task.Await(a), task.Await(b))
}
