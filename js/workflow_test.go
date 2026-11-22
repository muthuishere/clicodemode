package js

import (
	"context"
	"testing"
	"time"
)

// Run returns as soon as its context ends, so this checks the background
// goroutines too: a backoff that ignored cancellation would keep tasks alive.
func TestWorkflowBackoffStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	it := New(ctx, Config{})
	InstallWorkflow(it, WorkflowConfig{})
	prog, err := Parse(`export default await workflow("r", [step("x", () => { throw Error("no") }, {retry: {attempts: 5, backoff: "10s"}})]).run()`, "main.js")
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{})
	go func() { _, _ = it.Run(prog, "main.js"); close(ran) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-ran
	drained := make(chan struct{})
	go func() { it.tasks.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("workflow goroutines still running after cancel (backoff ignored cancellation)")
	}
}
