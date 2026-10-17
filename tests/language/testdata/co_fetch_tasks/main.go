package main

// goalchemy:gate cooperative

import "github.com/eugenioenko/goalchemy/lib/errors"

type Task struct {
	ID    int
	Title string
	Done  bool
}

func produce(out chan<- Task, n int) {
	for i := 1; i <= n; i++ {
		out <- Task{ID: i, Title: "task", Done: i%3 == 0}
	}
	close(out)
}

func fetchTasks(n int) ([]Task, error) {
	if n < 0 {
		return nil, errors.New("negative count")
	}
	ch := make(chan Task)
	go produce(ch, n)
	var tasks []Task
	for t := range ch {
		if t.Done {
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func main() {
	tasks, err := fetchTasks(7)
	if err != nil {
		println("error:", err.Error())
		return
	}
	for _, t := range tasks {
		println(t.ID, t.Title)
	}
	_, err = fetchTasks(-1)
	println(err.Error())
}
