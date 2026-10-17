package main

// goalchemy:gate cooperative

import (
	"github.com/eugenioenko/goalchemy/lib/context"
	"github.com/eugenioenko/goalchemy/lib/errors"
	"github.com/eugenioenko/goalchemy/lib/runtime"
	"github.com/eugenioenko/goalchemy/lib/task"
)

type counter struct{ N int }

func (c counter) value() int { return c.N }
func main() {
	var futures []task.Future[func() int]
	for i := 0; i < 32; i++ {
		futures = append(futures, task.Start(counter{i}.value))
	}
	sum := 0
	for _, f := range futures {
		sum += task.Await(f)
	}
	println("sum", sum)
	finished := task.Start(func() int { return 42 })
	runtime.Gosched()
	println("finished", task.Await(finished), task.Await(finished))
	ch := make(chan int)
	f := task.Start(func() int { return <-ch })
	go func() { ch <- 9 }()
	values := make([]int, 2)
	task.All(func() { values[0] = task.Await(f) }, func() { values[1] = task.Await(f) })
	println("shared", values[0], values[1])
	ctx, cancel := context.WithCancel(context.Background())
	canceled := task.Start(func() error { <-ctx.Done(); return ctx.Err() })
	cancel()
	println("context", task.Await(canceled).Error())
	errorResult := task.Start(func() (string, error) { return "result", errors.New("future error") })
	text, err := task.Await(errorResult)
	println(text, err.Error())
}
