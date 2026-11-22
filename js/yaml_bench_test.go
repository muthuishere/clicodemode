package js

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// Run with: CODEMODE_YAML_BENCH=docs.yaml go test ./js -run '^$' -bench YAML -benchtime 5x
func benchFile(b *testing.B) []byte {
	p := os.Getenv("CODEMODE_YAML_BENCH")
	if p == "" {
		b.Skip("set CODEMODE_YAML_BENCH to a multi-document YAML file")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	return data
}

// Raw Go: yaml.v3 decoding every document to map[string]any.
func BenchmarkYAMLRawGo(b *testing.B) {
	data := benchFile(b)
	for b.Loop() {
		d := yaml.NewDecoder(bytes.NewReader(data))
		n := 0
		for {
			var v any
			if err := d.Decode(&v); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				b.Fatal(err)
			}
			n++
		}
		if n != 20000 {
			b.Fatalf("docs %d", n)
		}
	}
}

// CodeMode end to end: a program streams the file's lines through .yaml()
// into ordered JS objects and counts them (what `execStream([...]).yaml()` does).
func BenchmarkYAMLCodeModeStream(b *testing.B) {
	data := benchFile(b)
	prog, err := Parse(`await src().yaml().count()`, "bench.js")
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		it := New(context.Background(), Config{})
		it.Define("src", bi("src", func(*Runner, Value, []Value) (Value, error) {
			return NewStream(FromReader(io.NopCloser(bytes.NewReader(data)))), nil
		}))
		v, err := it.Run(prog, "bench.js")
		if err != nil {
			b.Fatal(err)
		}
		if v != 20000.0 {
			b.Fatalf("count %v", v)
		}
		it.Close()
	}
}
