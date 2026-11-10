package codex

import (
	"sync"
	"testing"

	"mcode/internal/harness"
)

func TestTurnEmitterSerializesCallbacks(t *testing.T) {
	var events []harness.Event
	emitter := &turnEmitter{fn: func(event harness.Event) {
		events = append(events, event)
	}}

	const workers = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			emitter.emit(harness.Event{Type: harness.EventSession})
			emitter.emitAccepted()
		}()
	}
	close(start)
	wg.Wait()

	if len(events) != workers+1 {
		t.Fatalf("expected %d events, got %d", workers+1, len(events))
	}
	accepted := 0
	for _, event := range events {
		if event.Type == harness.EventTurnAccepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("expected one EventTurnAccepted, got %d", accepted)
	}
}
