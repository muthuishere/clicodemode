package js

import (
	"errors"
	"sync"
)

// pmapDefaultConcurrency is pmap's concurrency when none is given.
const pmapDefaultConcurrency = 4

// pmapBuiltin implements pmap(items, fn, {concurrency}): fn(item, index) runs
// for every item with at most `concurrency` calls in flight; the promise
// resolves to the results in input order. The first rejection rejects the
// whole and no further items are started (in-flight calls finish, ignored).
// Callbacks run under the interpreter lock via callLocked, which releases it
// while a returned promise is awaited, so async callbacks overlap.
func pmapBuiltin(r *Runner, _ Value, a []Value) (Value, error) {
	items, err := r.iterate(arg(a, 0), nil)
	if err != nil {
		return nil, err
	}
	fn := arg(a, 1)
	switch fn.(type) {
	case *Function, *Builtin:
	default:
		return nil, r.it.typeError("pmap(items, fn, {concurrency}) needs a function")
	}
	conc := pmapDefaultConcurrency
	switch o := arg(a, 2).(type) {
	case undefinedT, nil:
	case *Object:
		if cv, ok := o.Get("concurrency"); ok && cv != Undefined {
			n, isNum := cv.(float64)
			if !isNum || n < 1 || n != float64(int(n)) {
				return nil, r.it.typeError("pmap: concurrency must be a positive integer")
			}
			conc = int(n)
		}
	default:
		return nil, r.it.typeError("pmap: options must be an object like {concurrency: 4}")
	}

	it := r.it
	out := it.newPromise()
	results := make([]Value, len(items))
	it.tasks.Add(1)
	go func() {
		defer it.tasks.Done()
		sem := make(chan struct{}, conc)
		stop := make(chan struct{})
		var once sync.Once
		var wg sync.WaitGroup
		reject := func(v Value) {
			once.Do(func() {
				out.settle(2, v)
				close(stop)
			})
		}
	schedule:
		for i, item := range items {
			select {
			case <-stop:
				break schedule
			default:
			}
			select {
			case sem <- struct{}{}:
			case <-stop:
				break schedule
			case <-it.ctx.Done():
				break schedule
			}
			select { // a rejection may have landed while we waited for a slot
			case <-stop:
				<-sem
				break schedule
			default:
			}
			wg.Add(1)
			it.tasks.Add(1)
			go func(i int, item Value) {
				defer it.tasks.Done()
				defer wg.Done()
				defer func() { <-sem }()
				v, err := it.callLocked(fn, item, float64(i))
				if err != nil {
					var t *Throw
					if errors.As(err, &t) {
						reject(t.Value)
					} else {
						reject(makeError("Error", err.Error()))
					}
					return
				}
				results[i] = v
			}(i, item)
		}
		wg.Wait()
		if it.ctx.Err() != nil {
			reject(makeError("Error", "cancelled"))
			return
		}
		out.settle(1, &Array{Elems: results}) // no-op if already rejected
	}()
	return out, nil
}
