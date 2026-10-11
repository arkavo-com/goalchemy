package cache

type entry struct{ hits int }

var (
	cache    = map[string]string{}
	calls    int
	list     []string
	last     = &entry{}
	limits   = [4]int{1, 2, 3, 4}
	greeting = "hello"
	table    = map[string]int{"a": 1}
	shared   = map[string]int{}
	counter  Counter
	loaded   int
)

func init() { loaded++ }

func Fetch(key string) string {
	if v, ok := cache[key]; ok {
		return v
	}
	v := key + "!"
	cache[key] = v // want GCW001
	return v
}

func Count() int {
	bump()
	return calls
}

func bump() { calls++ } // want GCW001

func Append(s string) int {
	list = append(list, s) // want GCW001
	return len(list)
}

func Touch() int {
	last.hits = 1 // want GCW001
	return last.hits
}

func Set(i int) int {
	limits[i] = 0 // want GCW001
	return limits[0]
}

func Greet(name string) string { return greeting + " " + name + loadedText() }

func Lookup(k string) int { return table[k] }

func loadedText() string {
	if loaded > 0 {
		return "."
	}
	return ""
}

func Tick() int { return counter.Add(1) } // want GCW001

type Counter struct{ n int }

func (c *Counter) Add(d int) int { c.n += d; return c.n }

func (c *Counter) Remember(k string) int {
	shared[k]++
	return shared[k]
}

func New() *Counter {
	shared["new"] = 1
	return &Counter{}
}

func Use(c *Counter) int {
	shared["use"]++
	return c.n
}
