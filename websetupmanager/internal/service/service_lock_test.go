package service

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestKeyedLocksSerializeAndReleaseRegistryEntries(t *testing.T) {
	s := &Service{locks: make(map[string]*keyedMutex)}
	var active atomic.Int32
	var maximum atomic.Int32
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < 32; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if err := s.withSetupLock("same-setup", func() error {
				current := active.Add(1)
				for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
				}
				active.Add(-1)
				return nil
			}); err != nil {
				t.Errorf("lock operation: %v", err)
			}
		}()
	}
	close(start)
	workers.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent holders = %d", maximum.Load())
	}

	for index := 0; index < 10_000; index++ {
		if err := s.withSetupLock(fmt.Sprintf("deleted-%d", index), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	if len(s.locks) != 0 {
		t.Fatalf("keyed lock registry retained %d idle entries", len(s.locks))
	}
}
