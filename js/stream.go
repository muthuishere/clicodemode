package js

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Source produces raw records (usually lines as strings). It must stop and
// close out when ctx is cancelled, and send with a select on ctx.Done() so a
// slow consumer applies backpressure instead of buffering without bound.
// The returned wait func reports the source's final error after out closes.
type Source func(ctx context.Context) (out <-chan Value, wait func() error)

// Stream is a lazy pipeline: transform methods return a new Stream; terminal
// methods (toArray, reduce, forEach, count, first, writeJSONL, writeLines,
// json) run it and return a Promise. Nothing runs until a terminal is called.
type Stream struct {
	src    Source
	stages []stage
}

type stage struct {
	kind string // map filter take jsonl csv yaml
	fn   Value
	n    int
}

// NewStream wraps a source.
func NewStream(src Source) *Stream { return &Stream{src: src} }

// StreamBuffer is the channel size between a source and the pipeline.
const StreamBuffer = 64

// FromValues is a source over a fixed list.
func FromValues(vals []Value) Source {
	return func(ctx context.Context) (<-chan Value, func() error) {
		ch := make(chan Value)
		go func() {
			defer close(ch)
			for _, v := range vals {
				select {
				case ch <- v:
				case <-ctx.Done():
					return
				}
			}
		}()
		return ch, func() error { return nil }
	}
}

// FromReader is a source of lines read from r (closed when done/cancelled).
func FromReader(r io.ReadCloser) Source {
	return func(ctx context.Context) (<-chan Value, func() error) {
		ch := make(chan Value, StreamBuffer)
		errc := make(chan error, 1)
		go func() {
			defer close(ch)
			defer r.Close()
			err := scanLines(ctx, r, ch)
			errc <- err
		}()
		return ch, func() error { return <-errc }
	}
}

func scanLines(ctx context.Context, r io.Reader, ch chan<- Value) error {
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	emit := func(line string) bool {
		select {
		case ch <- strings.TrimSuffix(line, "\r"):
			return true
		case <-ctx.Done():
			return false
		}
	}
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		for {
			i := indexByte(buf, '\n')
			if i < 0 {
				break
			}
			if !emit(string(buf[:i])) {
				return nil
			}
			buf = buf[i+1:]
		}
		if len(buf) > 16<<20 {
			return errors.New("line longer than 16 MiB")
		}
		if err == io.EOF {
			if len(buf) > 0 {
				emit(string(buf))
			}
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func (s *Stream) with(st stage) *Stream {
	return &Stream{src: s.src, stages: append(append([]stage{}, s.stages...), st)}
}

// callLocked calls a JS function from a pipeline goroutine, holding the
// interpreter lock only for the call; a returned promise is awaited unlocked.
func (it *Interp) callLocked(fn Value, args ...Value) (Value, error) {
	it.gil.Lock()
	if it.ctx.Err() != nil {
		it.gil.Unlock()
		return nil, it.stopErr(nil, "")
	}
	r := &Runner{it: it}
	v, err := r.Call(fn, args...)
	it.gil.Unlock()
	if err != nil {
		return nil, err
	}
	if p, ok := v.(*Promise); ok {
		p.mu.Lock()
		p.handled = true
		p.mu.Unlock()
		select {
		case <-p.done:
		case <-it.ctx.Done():
			return nil, it.stopErr(nil, "")
		}
		if p.state == 2 {
			return nil, &Throw{Value: p.value}
		}
		return p.value, nil
	}
	return v, nil
}

// run drives the pipeline on a goroutine and calls sink for each output
// value; sink returns stop=true to end early (which cancels the source).
func (s *Stream) run(it *Interp, sink func(v Value) (stop bool, err error), done func() (Value, error)) *Promise {
	p := it.newPromise()
	it.tasks.Add(1)
	go func() {
		defer it.tasks.Done()
		ctx, cancel := context.WithCancel(it.ctx)
		defer cancel()
		in, wait := s.src(ctx)
		taken := make([]int, len(s.stages))
		yamlBuf := make([][]string, len(s.stages))
		yamlPars := make([]*yamlPar, len(s.stages))
		for i, st := range s.stages {
			if st.kind == "yaml" {
				yamlPars[i] = newYAMLPar()
			}
		}
		var csvHeader []string
		var fail error
		early := false
		// push runs vals through stages[from:] and into the sink; it reports
		// whether the pipeline must stop (fail or early set).
		push := func(vals []Value, from int) bool {
			for si := from; si < len(s.stages); si++ {
				st := s.stages[si]
				var next []Value
				for _, v := range vals {
					switch st.kind {
					case "map":
						m, err := it.callLocked(st.fn, v)
						if err != nil {
							fail = err
							return true
						}
						next = append(next, m)
					case "filter":
						keep, err := it.callLocked(st.fn, v)
						if err != nil {
							fail = err
							return true
						}
						if truthy(keep) {
							next = append(next, v)
						}
					case "jsonl":
						line := strings.TrimSpace(toString(v))
						if line == "" {
							continue
						}
						j, err := jsonParse(line)
						if err != nil {
							fail = &Throw{Value: makeError("SyntaxError", fmt.Sprintf("jsonl: %v in line %q", err, clip(line)))}
							return true
						}
						next = append(next, j)
					case "yaml":
						line := toString(v)
						rest, isSep := yamlSeparator(line)
						if !isSep {
							yamlBuf[si] = append(yamlBuf[si], line)
							continue
						}
						yamlPars[si].submit(yamlBuf[si])
						yamlBuf[si] = nil
						if rest != "" {
							yamlBuf[si] = append(yamlBuf[si], rest)
						}
						docs, err := yamlPars[si].drain(false)
						next = append(next, docs...)
						if err != nil {
							fail = err
							return true
						}
					case "csv":
						rec, err := csv.NewReader(strings.NewReader(toString(v))).Read()
						if err == io.EOF {
							continue
						}
						if err != nil {
							fail = &Throw{Value: makeError("SyntaxError", fmt.Sprintf("csv: %v", err))}
							return true
						}
						if csvHeader == nil {
							csvHeader = rec
							continue
						}
						o := NewObject()
						for i, h := range csvHeader {
							if i < len(rec) {
								o.Set(h, rec[i])
							} else {
								o.Set(h, "")
							}
						}
						next = append(next, o)
					case "take":
						if taken[si] < st.n {
							taken[si]++
							next = append(next, v)
						}
					}
				}
				vals = next
				if st.kind == "take" && taken[si] >= st.n && len(vals) == 0 {
					early = true
					return true
				}
			}
			for _, v := range vals {
				stop, err := sink(v)
				if err != nil {
					fail = err
					return true
				}
				if stop {
					early = true
					return true
				}
			}
			// a take stage that just reached its limit ends the stream
			for si, st := range s.stages {
				if st.kind == "take" && taken[si] >= st.n {
					early = true
					return true
				}
			}
			return false
		}
		stopped := false
		for raw := range in {
			if push([]Value{raw}, 0) {
				stopped = true
				break
			}
		}
		// end of input: flush buffered YAML documents through later stages
		if !stopped && it.ctx.Err() == nil {
			for si, st := range s.stages {
				if st.kind != "yaml" {
					continue
				}
				yamlPars[si].submit(yamlBuf[si])
				yamlBuf[si] = nil
				docs, err := yamlPars[si].drain(true)
				if len(docs) > 0 && push(docs, si+1) {
					break
				}
				if err != nil {
					fail = err
					break
				}
			}
		}
		if early || fail != nil {
			cancel() // stops and kills the source (closes processes)
		}
		for range in {
		}
		werr := wait()
		cancel()
		if fail == nil && !early && werr != nil && it.ctx.Err() == nil {
			fail = werr
		}
		if fail == nil && it.ctx.Err() != nil {
			p.settle(2, makeError("Error", "cancelled"))
			return
		}
		if fail != nil {
			var t *Throw
			if errors.As(fail, &t) {
				p.settle(2, t.Value)
			} else {
				p.settle(2, makeError("Error", fail.Error()))
			}
			return
		}
		v, err := done()
		if err != nil {
			p.settle(2, makeError("Error", err.Error()))
			return
		}
		p.settle(1, v)
	}()
	return p
}

func clip(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func streamMember(s *Stream, key string) Value {
	transform := func(kind string) Value {
		return bi(key, func(r *Runner, _ Value, a []Value) (Value, error) {
			st := stage{kind: kind}
			switch kind {
			case "map", "filter":
				st.fn = arg(a, 0)
				switch st.fn.(type) {
				case *Function, *Builtin:
				default:
					return nil, r.it.typeError(key + "() needs a function")
				}
			case "take":
				st.n = int(toNumber(arg(a, 0)))
				if st.n < 0 {
					return nil, r.it.typeError("take() needs a non-negative number")
				}
			}
			return s.with(st), nil
		})
	}
	switch key {
	case "map", "filter", "take", "jsonl", "csv", "yaml":
		return transform(key)
	case "lines":
		return bi(key, func(_ *Runner, _ Value, _ []Value) (Value, error) { return s, nil })
	case "toArray":
		return bi(key, func(r *Runner, _ Value, _ []Value) (Value, error) {
			out := &Array{}
			return s.run(r.it, func(v Value) (bool, error) {
				out.Elems = append(out.Elems, v)
				return false, nil
			}, func() (Value, error) { return out, nil }), nil
		})
	case "count":
		return bi(key, func(r *Runner, _ Value, _ []Value) (Value, error) {
			n := 0
			return s.run(r.it, func(Value) (bool, error) { n++; return false, nil },
				func() (Value, error) { return float64(n), nil }), nil
		})
	case "first":
		return bi(key, func(r *Runner, _ Value, _ []Value) (Value, error) {
			var got Value = Undefined
			return s.run(r.it, func(v Value) (bool, error) { got = v; return true, nil },
				func() (Value, error) { return got, nil }), nil
		})
	case "forEach":
		return bi(key, func(r *Runner, _ Value, a []Value) (Value, error) {
			fn := arg(a, 0)
			return s.run(r.it, func(v Value) (bool, error) {
				_, err := r.it.callLocked(fn, v)
				return false, err
			}, func() (Value, error) { return Undefined, nil }), nil
		})
	case "reduce":
		return bi(key, func(r *Runner, _ Value, a []Value) (Value, error) {
			fn, acc := arg(a, 0), arg(a, 1)
			return s.run(r.it, func(v Value) (bool, error) {
				var err error
				acc, err = r.it.callLocked(fn, acc, v)
				return false, err
			}, func() (Value, error) { return acc, nil }), nil
		})
	case "json":
		return bi(key, func(r *Runner, _ Value, _ []Value) (Value, error) {
			var b strings.Builder
			return s.run(r.it, func(v Value) (bool, error) {
				b.WriteString(toString(v))
				b.WriteByte('\n')
				return false, nil
			}, func() (Value, error) {
				v, err := jsonParse(b.String())
				if err != nil {
					return nil, fmt.Errorf("json: %v", err)
				}
				return v, nil
			}), nil
		})
	case "writeJSONL", "writeLines":
		return bi(key, func(r *Runner, _ Value, _ []Value) (Value, error) {
			n := 0
			w := r.it.cfg.Stdout
			return s.run(r.it, func(v Value) (bool, error) {
				line := toString(v)
				if key == "writeJSONL" {
					j, err := jsonStringify(r.it, v, "")
					if err != nil {
						return false, err
					}
					line = toString(j)
				}
				if _, err := io.WriteString(w, line+"\n"); err != nil {
					return false, err
				}
				n++
				return false, nil
			}, func() (Value, error) { return float64(n), nil }), nil
		})
	}
	return Undefined
}
