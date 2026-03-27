package tunnel

import (
	"context"
	"testing"
	"time"

	"github.com/jpillora/chisel/share/cio"
	"github.com/jpillora/chisel/share/settings"
)

func TestProxyEmitsPendingFailedOnListenError(t *testing.T) {
	var events []EndpointStateChange
	remote := &settings.Remote{
		Reverse:   true,
		LocalHost: "127.0.0.1",
		LocalPort: "1",
	}
	_, _ = NewProxy(cio.NewLogger("test"), nil, 0, remote, "s1", func(change EndpointStateChange) {
		events = append(events, change)
	}, nil)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 endpoint events, got %d", len(events))
	}
	if events[0].State != EndpointStatePending {
		t.Fatalf("expected first event pending, got %s", events[0].State)
	}
	if events[1].State != EndpointStateFailed {
		t.Fatalf("expected second event failed, got %s", events[1].State)
	}
}

func TestProxyEmitsActiveClosedOnRunShutdown(t *testing.T) {
	var events []EndpointStateChange
	var streamEvents []StreamEvent
	remote := &settings.Remote{
		Reverse:    true,
		LocalHost:  "127.0.0.1",
		LocalPort:  "0",
		LocalProto: "tcp",
		RemoteHost: "127.0.0.1",
		RemotePort: "1",
	}
	p, err := NewProxy(cio.NewLogger("test"), nil, 0, remote, "s1", func(change EndpointStateChange) {
		events = append(events, change)
	}, func(change StreamEvent) {
		streamEvents = append(streamEvents, change)
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	time.Sleep(40 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}
	if events[0].State != EndpointStatePending {
		t.Fatalf("expected pending event first, got %s", events[0].State)
	}
	if events[1].State != EndpointStateActive {
		t.Fatalf("expected active event second, got %s", events[1].State)
	}
	if events[len(events)-1].State != EndpointStateClosed {
		t.Fatalf("expected last event closed, got %s", events[len(events)-1].State)
	}
	_ = streamEvents
}
