package dialogue

import (
	"context"
	"sync"
)

// patientLocks serializes the final check-and-write for each patient. References
// include waiting callers, so idle keys can be removed without splitting a lock.
type patientLocks struct {
	mu      sync.Mutex
	entries map[int]*patientLock
}
type patientLock struct {
	token chan struct{}
	refs  int
}

func (l *patientLocks) acquire(ctx context.Context, id int) (func(), error) {
	l.mu.Lock()
	if l.entries == nil {
		l.entries = map[int]*patientLock{}
	}
	entry := l.entries[id]
	if entry == nil {
		entry = &patientLock{token: make(chan struct{}, 1)}
		l.entries[id] = entry
	}
	entry.refs++
	l.mu.Unlock()
	drop := func() {
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.entries, id)
		}
		l.mu.Unlock()
	}
	select {
	case entry.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-entry.token
			drop()
			return nil, err
		}
		return func() { <-entry.token; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
