package js

import (
	"strings"
	"testing"
	"time"
)

func TestPmapOrderAndBound(t *testing.T) {
	src := `
let inFlight = 0, peak = 0
const out = await pmap([50, 10, 40, 5, 30, 1, 20], async (ms, i) => {
  inFlight++
  if (inFlight > peak) peak = inFlight
  await sleep(ms)
  inFlight--
  return ms * 10 + i
}, {concurrency: 3})
;[out, peak]`
	got, _ := run(t, src)
	if got != `[[500,101,402,53,304,15,206],3]` {
		t.Fatalf("got %s", got)
	}
	got, _ = run(t, `let n = 0, peak = 0; await pmap([1,2,3,4,5,6,7,8,9,10], async x => { n++; if (n > peak) peak = n; await sleep(10); n--; return x }); peak`)
	if got != "4" {
		t.Fatalf("default concurrency: peak %s", got)
	}
	got, _ = run(t, `[await pmap([], x => x), await pmap([1,2], x => x * 2, {concurrency: 1})]`)
	if got != `[[],[2,4]]` {
		t.Fatalf("got %s", got)
	}
}

func TestPmapParallelWallClock(t *testing.T) {
	start := time.Now()
	run(t, `await pmap([1,2,3,4,5,6,7,8], async () => { await sleep(50) }, {concurrency: 8})`)
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("8x50ms at concurrency 8 took %v; not parallel", d)
	}
}

func TestPmapRejection(t *testing.T) {
	got, _ := run(t, `
let started = []
let msg
try {
  await pmap([1,2,3,4,5,6], async x => {
    started.push(x)
    await sleep(5)
    if (x === 2) throw Error("boom " + x)
    return x
  }, {concurrency: 2})
} catch (e) { msg = e.message }
await sleep(30)
;[msg, started.length < 6]`)
	if got != `["boom 2",true]` {
		t.Fatalf("got %s", got)
	}
	for _, src := range []string{`await pmap([1], 5)`, `await pmap([1], x => x, {concurrency: 0})`, `await pmap([1], x => x, 3)`} {
		if err := runErr(t, src, Config{}); err == nil || !strings.Contains(err.Error(), "pmap") {
			t.Fatalf("%s: got %v", src, err)
		}
	}
}
