package main

// goalchemy:gate cooperative

// Values computed before a pause and used after it must stay reachable while
// other tasks run and the collector runs (GOALCHEMY_GC_THRESHOLD=1).

type pair struct {
	a, b int
}

func roundTrip(ch chan int, v int) int {
	ch <- v
	return <-ch
}

func echo(ch chan int) {
	for {
		v, ok := <-ch
		if !ok {
			return
		}
		junk := make([]pair, 8)
		junk[0].a = v
		ch <- junk[0].a + 1
	}
}

func describe(x any, n int) int {
	return x.(pair).a + n
}

func measure(s []pair, n int) int {
	return s[len(s)-1].b + n
}

func lookup(m map[string]pair, n int) int {
	return m["k"].a + n
}

func main() {
	ch := make(chan int)
	go echo(ch)
	total := 0
	for i := 0; i < 4; i++ {
		total += describe(pair{i, 1}, roundTrip(ch, i))
		total += measure(append([]pair{{0, i}}, pair{1, 2}), roundTrip(ch, i))
		total += lookup(map[string]pair{"k": {i * 10, 0}}, roundTrip(ch, i))
		p := &pair{i, i}
		q := roundTrip(ch, p.a) + p.b
		total += q
	}
	println(total)
	close(ch)
}
