package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// received owns one complete text-message allocation. Neither admission nor
// dequeue allocates a protocol sequence or acknowledges a device event.
type received struct {
	raw        []byte
	receivedAt time.Time
	reserved   bool
}

// ingressQueue has one producer and one serial consumer. Reservations include
// queued messages AND the consumer's in-flight message until done. Its byte
// bound counts backing capacity, not just payload length or total process heap.
type ingressQueue struct {
	mu       sync.Mutex
	messages chan received
	stopped  bool
	count    int
	bytes    int
}

func newIngressQueue() *ingressQueue {
	return &ingressQueue{messages: make(chan received, MaxQueue)}
}

func (q *ingressQueue) offer(ctx context.Context, v received) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped || ctx.Err() != nil || q.count >= MaxQueue || cap(v.raw) > MaxQueueBytes-q.bytes {
		return false
	}
	v.reserved = true
	q.count++
	q.bytes += cap(v.raw)
	select {
	case q.messages <- v:
		return true
	default:
		// Never block the reader behind a processor or a full channel.
		q.releaseLocked(&v)
		return false
	}
}

func (q *ingressQueue) next(ctx context.Context) (received, bool) {
	select {
	case <-ctx.Done():
		return received{}, false
	case v, ok := <-q.messages:
		if !ok {
			return received{}, false
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		// A ready channel may win the select even when cancellation is ready.
		if q.stopped || ctx.Err() != nil {
			q.releaseLocked(&v)
			return received{}, false
		}
		return v, true
	}
}

func (q *ingressQueue) done(v *received) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseLocked(v)
}

func (q *ingressQueue) releaseLocked(v *received) {
	if !v.reserved {
		return
	}
	n := cap(v.raw)
	*v = received{}
	q.count--
	q.bytes -= n
}

func (q *ingressQueue) stop() {
	q.mu.Lock()
	q.stopped = true
	q.mu.Unlock()
}

// drain is called only after all workers have joined and handler processing has
// returned. An upgrade that failed before reader startup leaves an open, empty
// channel; do not wait for a producer that was never started.
func (q *ingressQueue) drain() {
	for {
		select {
		case v, ok := <-q.messages:
			if !ok {
				return
			}
			q.done(&v)
		default:
			return
		}
	}
}

type ingressReader interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
}

func (x *connection) cancelIngressRead() {
	if x.readCancel != nil {
		x.readCancel()
	}
}

func (x *connection) startIngress(r ingressReader) {
	x.work.Add(1)
	go func() { defer x.work.Done(); x.readIngress(r) }()
}

// readIngress is the only WebSocket reader. Read completion is the observation
// boundary, not packet arrival. No PG, decoding, processor wait or queue wait
// occurs here. Coder consumes control frames and returns whole data messages.
func (x *connection) readIngress(r ingressReader) {
	defer close(x.ingress.messages) // Only the producer closes this channel.
	b := newBudget(x.parent.budgetNow())
	first := true
	for {
		if x.ctx.Err() != nil {
			x.stop(1000, "closed")
			return
		}
		ctx := x.readCtx
		cancel := func() {}
		if first {
			ctx, cancel = context.WithTimeout(x.readCtx, 5*time.Second)
		}
		kind, raw, err := r.Read(ctx)
		var at time.Time
		if err == nil {
			at = x.parent.budgetNow()
		}
		cancel()
		if err != nil {
			raw = nil
			if first {
				x.stop(1002, "HELLO_TIMEOUT")
			} else {
				x.stop(1000, "closed")
			}
			return
		}
		if x.ctx.Err() != nil {
			raw = nil
			x.stop(1000, "closed")
			return
		}
		if kind != websocket.MessageText {
			raw = nil
			x.stop(1002, "PROTOCOL_ERROR")
			return
		}
		if !b.take(at, len(raw)) || !x.ingress.offer(x.ctx, received{raw: raw, receivedAt: at}) {
			raw = nil
			x.stop(1008, "RESOURCE_LIMIT")
			return
		}
		raw = nil // The queue now owns the backing allocation.
		first = false
	}
}
