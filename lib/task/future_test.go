package task

import "testing"

func TestFutureResults(t *testing.T) {
	f := Start(func() (int, string, error) { return 7, "value", nil })
	for i := 0; i < 2; i++ {
		n, s, err := Await(f)()
		if n != 7 || s != "value" || err != nil {
			t.Fatalf("got %d, %q, %v", n, s, err)
		}
	}
	CheckUnawaited()
}

func TestFuturePanicRepeated(t *testing.T) {
	f := Start(func() int { panic("future panic") })
	for i := 0; i < 2; i++ {
		func() {
			defer func() {
				if p := recover(); p != "future panic" {
					t.Fatalf("panic = %v", p)
				}
			}()
			_ = Await(f)()
			t.Fatal("missing panic")
		}()
	}
	CheckUnawaited()
}

func TestFutureFinished(t *testing.T) {
	f := Start(func() int { return 42 })
	<-f.state.futureState().done
	if n := Await(f)(); n != 42 {
		t.Fatalf("got %d", n)
	}
}
