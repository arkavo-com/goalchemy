package main

// goalchemy:gate cooperative

import "github.com/eugenioenko/goalchemy/lib/task"

type failure struct{ code int }

func (f *failure) Error() string { return "future failed" }
func catch(f task.Future[func() int], expected *failure) {
	defer func() { println("recovered", recover() == expected) }()
	println(task.Await(f))
}
func main() {
	expected := &failure{17}
	f := task.Start(func() int { panic(expected) })
	catch(f, expected)
	catch(f, expected)
	task.All(func() { catch(f, expected) }, func() { catch(f, expected) })
	gate := make(chan int)
	pending := task.Start(func() int { <-gate; panic(expected) })
	task.All(func() { catch(pending, expected) }, func() { catch(pending, expected) }, func() { close(gate) })
	var fn func() int
	nilFuture := task.Start(fn)
	func() { defer func() { println("nil", recover() != nil) }(); task.Await(nilFuture) }()
	println("done")
}
