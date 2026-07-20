package runner

import (
	"context"
	"sync"

	"qda/internal/types"
)

type queueItem struct {
	target   types.Target
	attempts int
}

// workQueue is a FIFO queue with blocking Pop, requeue-to-end support and
// completion detection (all initial items processed and nothing in flight).
type workQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	items    []queueItem
	inFlight map[string]queueItem
	closed   bool
}

func newWorkQueue() *workQueue {
	q := &workQueue{inFlight: map[string]queueItem{}}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// PushBack appends an item to the end of the queue.
func (q *workQueue) PushBack(item queueItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, item)
	q.cond.Signal()
}

// Close marks the queue as fully loaded; workers exit once it drains.
func (q *workQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

// Pop removes the first item, blocking until one is available or the queue
// is closed and fully drained.
func (q *workQueue) Pop(ctx context.Context) (queueItem, bool) {
	// Wake up on context cancellation.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.cond.Broadcast()
			q.mu.Unlock()
		case <-stop:
		}
	}()

	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if len(q.items) > 0 {
			item := q.items[0]
			q.items = q.items[1:]
			q.inFlight[item.target.Domain] = item
			return item, true
		}
		if q.closed && len(q.inFlight) == 0 {
			return queueItem{}, false
		}
		if ctx.Err() != nil {
			return queueItem{}, false
		}
		q.cond.Wait()
	}
}

// Done marks an in-flight item as processed (optionally requeueing it to
// the end of the queue with an incremented attempt counter).
func (q *workQueue) Done(item queueItem, requeue bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.inFlight, item.target.Domain)
	if requeue {
		item.attempts++
		q.items = append(q.items, item)
	}
	q.cond.Broadcast()
}

// Abandon returns an in-flight item to the front of the queue (used on
// shutdown so the resume state is accurate).
func (q *workQueue) Snapshot() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := make([]string, 0, len(q.items)+len(q.inFlight))
	for domain := range q.inFlight {
		pending = append(pending, domain)
	}
	for _, item := range q.items {
		pending = append(pending, item.target.Domain)
	}
	return pending
}
