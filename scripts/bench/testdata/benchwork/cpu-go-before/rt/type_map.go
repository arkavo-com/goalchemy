package rt

import "reflect"

type mapEntry[K comparable, V any] struct {
	key   K
	value V
	live  bool
}

type mapData[K comparable, V any] struct {
	index   map[K]*mapEntry[K, V]
	entries []*mapEntry[K, V]
}

// Map is a Go map value: a handle to shared insertion-ordered storage with
// snapshot iteration. The zero Map is the nil map.
type Map[K comparable, V any] struct {
	d *mapData[K, V]
}

func (m Map[K, V]) IsNil() bool { return m.d == nil }

var keyChecks = map[reflect.Type]bool{}

// checkKey reproduces Go's hashing panic for keys holding unhashable
// dynamic values; only key types containing interfaces can fail.
func checkKey[K comparable](k K) {
	t := reflect.TypeFor[K]()
	need, ok := keyChecks[t]
	if !ok {
		need = containsInterface(t)
		keyChecks[t] = need
	}
	if need {
		CheckKey(k)
	}
}

func containsInterface(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Interface:
		return true
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if containsInterface(t.Field(i).Type) {
				return true
			}
		}
	case reflect.Array:
		return containsInterface(t.Elem())
	}
	return false
}

func (m Map[K, V]) sourceMapTypes() (reflect.Type, reflect.Type) {
	return reflect.TypeFor[K](), reflect.TypeFor[V]()
}

// MapIter iterates a snapshot of the entries present when it was created.
type MapIter[K comparable, V any] struct {
	entries []*mapEntry[K, V]
	i       int
}

func (d *mapData[K, V]) compact() {
	if len(d.entries) > 32 && len(d.entries) > 2*len(d.index) {
		live := make([]*mapEntry[K, V], 0, len(d.index))
		for _, e := range d.entries {
			if e.live {
				live = append(live, e)
			}
		}
		d.entries = live
	}
}
