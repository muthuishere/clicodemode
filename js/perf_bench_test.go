package js

import (
	"context"
	"testing"
)

func benchProg(b *testing.B, src string) {
	prog, err := Parse(src, "bench.js")
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		it := New(context.Background(), Config{Limits: Limits{MaxSteps: 1 << 40}})
		if _, err := it.Run(prog, "bench.js"); err != nil {
			b.Fatal(err)
		}
		it.Close()
	}
}

// The loop from the CodeMode-vs-Node comparison (Node: ~24 ms incl. startup).
func BenchmarkLoopSum(b *testing.B) {
	benchProg(b, "let s = 0\nfor (let i = 0; i < 1000000; i++) s += i % 7\ns")
}

func BenchmarkFuncCalls(b *testing.B) {
	benchProg(b, "function add(a, b) { return a + b }\nlet s = 0\nfor (let i = 0; i < 300000; i++) s = add(s, i)\ns")
}

func BenchmarkObjectsArrays(b *testing.B) {
	benchProg(b, "const out = []\nfor (let i = 0; i < 100000; i++) out.push({id: i, even: i % 2 === 0})\nout.filter(o => o.even).map(o => o.id).reduce((a, b) => a + b, 0)")
}
