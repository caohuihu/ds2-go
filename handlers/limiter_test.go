package handlers

import (
	"context"
	"testing"
	"time"
)

// TestWaitSlotCancelledContext: a waiter whose context is cancelled before
// it gets a permit must return immediately with a no-op release — it
// neither parks a slot nor frees one it never held.
func TestWaitSlotCancelledContext(t *testing.T) {
	l := NewAccountLimiter(6)
	hold := l.WaitSlot(context.Background(), "dev", 6, 1) // the only permit of a cap-1 bucket

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan func(), 1)
	go func() {
		rel := l.WaitSlot(ctx, "dev", 6, 1)
		got <- rel
	}()
	cancel()
	var rel func()
	select {
	case rel = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitSlot did not return when the context was cancelled")
	}
	rel() // no-op: nothing was acquired, must be safe to call

	// The only permit is still held by `hold`, so a fresh waiter blocks.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	acquired := make(chan struct{})
	go func() {
		rel2 := l.WaitSlot(ctx2, "dev", 6, 1)
		rel2()
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("acquired a concurrency slot that is still held")
	case <-time.After(300 * time.Millisecond):
	}

	hold() // release the permit
	select {
	case <-acquired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("slot did not become available after release")
	}
}

// TestWaitSlotAcquiresAndReleases: a free bucket hands the permit out and
// the release puts it back for the next waiter.
func TestWaitSlotAcquiresAndReleases(t *testing.T) {
	l := NewAccountLimiter(6)
	ctx := context.Background()

	release := l.WaitSlot(ctx, "dev", 6, 1)
	release()

	done := make(chan func(), 1)
	go func() {
		done <- l.WaitSlot(ctx, "dev", 6, 1)
	}()
	select {
	case rel := <-done:
		rel()
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire the released slot")
	}
}

// TestWaitWaitQPMWaitSlot: WaitQPM consumes a rate token (its release is a
// no-op bookend), WaitSlot takes the concurrency permit (its release frees
// it). The two composed must reproduce the legacy Wait, which takes both.
func TestWaitWaitQPMWaitSlot(t *testing.T) {
	l := NewAccountLimiter(6)
	ctx := context.Background()

	holdSlot := l.WaitSlot(ctx, "dev", 6, 1) // grab the only permit
	tok := l.WaitQPM("dev", 6, 1)
	holdSlot()
	tok() // no-op release; the bucket stays usable

	// A legacy Wait must hand out a working release pair and leave the
	// bucket healthy for the next request.
	rel := l.Wait("dev", 6, 1)
	rel()
	rel2 := l.Wait("dev", 6, 1)
	rel2()
}
