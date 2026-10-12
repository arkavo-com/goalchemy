// goalchemy:gate cooperative
package main

type key struct {
	url, kid string
}

type entry struct {
	pem  string
	time int64
}

func lookup(ticks chan int, cache map[key]entry, url string) (entry, bool) {
	for k, v := range cache {
		<-ticks
		if k.url == url {
			return v, true
		}
	}
	return entry{}, false
}

func main() {
	ticks := make(chan int, 16)
	for i := 0; i < 16; i++ {
		ticks <- 1
	}
	empty := map[key]entry{}
	v, ok := lookup(ticks, empty, "a")
	println(ok, v.pem, v.time)
	full := map[key]entry{{"a", "k1"}: {"pem-a", 7}}
	v, ok = lookup(ticks, full, "a")
	println(ok, v.pem, v.time)
	v, ok = lookup(ticks, full, "b")
	println(ok, v.pem, v.time)
}
