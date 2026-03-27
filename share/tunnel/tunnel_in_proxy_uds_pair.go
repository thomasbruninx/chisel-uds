package tunnel

import (
	"context"
	"io"
	"sync"

	"github.com/jpillora/sizestr"
)

type udsPairRegistry struct {
	mu      sync.Mutex
	waiters map[string][]*udsPairWaiter
}

type udsPairWaiter struct {
	proxy   *Proxy
	matched chan udsPairSession
}

type udsPairSession struct {
	peer *Proxy
	done chan struct{}
}

var defaultUDSPairRegistry = &udsPairRegistry{
	waiters: map[string][]*udsPairWaiter{},
}

func (r *udsPairRegistry) run(ctx context.Context, p *Proxy) error {
	path := p.remote.UDSSocketPath
	self := &udsPairWaiter{
		proxy:   p,
		matched: make(chan udsPairSession, 1),
	}

	r.mu.Lock()
	queue := r.waiters[path]
	if len(queue) > 0 {
		peer := queue[0]
		r.waiters[path] = queue[1:]
		done := make(chan struct{})
		peer.matched <- udsPairSession{peer: p, done: done}
		r.mu.Unlock()
		defer close(done)
		return relayUDSPair(ctx, peer.proxy, p)
	}
	r.waiters[path] = append(queue, self)
	r.mu.Unlock()

	select {
	case session := <-self.matched:
		select {
		case <-ctx.Done():
			return nil
		case <-session.done:
			return nil
		}
	case <-ctx.Done():
		r.removeWaiter(path, self)
		return nil
	}
}

func (r *udsPairRegistry) removeWaiter(path string, waiter *udsPairWaiter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	queue := r.waiters[path]
	for i, w := range queue {
		if w == waiter {
			r.waiters[path] = append(queue[:i], queue[i+1:]...)
			if len(r.waiters[path]) == 0 {
				delete(r.waiters, path)
			}
			return
		}
	}
}

func relayUDSPair(ctx context.Context, a, b *Proxy) error {
	aStream, err := a.openRemoteStream(ctx)
	if err != nil {
		a.Infof("Pair stream open error: %s", err)
		return err
	}
	defer aStream.Close()
	bStream, err := b.openRemoteStream(ctx)
	if err != nil {
		b.Infof("Pair stream open error: %s", err)
		return err
	}
	defer bStream.Close()

	l := a.Logger.Fork("pair#%s", b.remote.UDSSocketPath)
	l.Debugf("Open")
	sent, recv := pipeBidirectional(aStream, bStream)
	l.Debugf("Close (sent %s received %s)", sizestr.ToString(sent), sizestr.ToString(recv))
	return nil
}

func pipeBidirectional(left io.ReadWriteCloser, right io.ReadWriteCloser) (int64, int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	var aToB int64
	var bToA int64
	go func() {
		defer wg.Done()
		n, _ := io.Copy(left, right)
		aToB = n
		_ = left.Close()
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(right, left)
		bToA = n
		_ = right.Close()
	}()
	wg.Wait()
	return aToB, bToA
}
