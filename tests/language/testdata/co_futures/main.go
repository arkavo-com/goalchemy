package main

// goalchemy:gate cooperative

import "github.com/eugenioenko/goalchemy/lib/task"

type result struct{ N int }
type holder struct {
	Future task.Future[func() (result, error)]
}

func compute() (result, error) { return result{7}, nil }

func main() {
	f := task.Start(compute)
	first, err := task.Await(f)
	println("first", first.N, err == nil)
	first.N = 99
	second, err := task.Await(f)
	println("again", second.N, err == nil)
	h := holder{f}
	s := []task.Future[func() (result, error)]{h.Future}
	m := map[string]task.Future[func() (result, error)]{"value": s[0]}
	ch := make(chan task.Future[func() (result, error)], 1)
	ch <- m["value"]
	last, err := task.Await(<-ch)
	println("stored", last.N, err == nil)
	void := task.Start(func() {})
	task.Await(void)
	println("done")
}
