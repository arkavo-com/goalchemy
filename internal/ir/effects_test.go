package ir

import "testing"

func TestFutureEffects(t *testing.T) {
	start := &Call{Kind: CallExtern, Extern: &Extern{Contract: "lib.task.start"}}
	await := &Call{Kind: CallExtern, Extern: &Extern{Contract: "lib.task.await", MaySuspend: true}}
	for _, tc := range []struct {
		name     string
		call     *Call
		suspends bool
	}{
		{"start", start, false}, {"await", await, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &Func{Name: tc.name}
			b := f.NewBlock("")
			b.Instrs = []Instr{tc.call}
			p := &Program{Types: NewTypes(), Funcs: []*Func{f}}
			ComputeEffects(p)
			if !p.Cooperative || f.MaySuspend != tc.suspends || tc.call.Suspends != tc.suspends {
				t.Fatalf("cooperative=%v function=%v call=%v", p.Cooperative, f.MaySuspend, tc.call.Suspends)
			}
		})
	}
}
