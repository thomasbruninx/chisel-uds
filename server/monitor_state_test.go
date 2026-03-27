package chserver

import (
	"fmt"
	"sync"
	"testing"

	"github.com/jpillora/chisel/share/tunnel"
)

func TestMonitorStateSessionLifecycle(t *testing.T) {
	m := newMonitorState()
	m.markSessionPending("1", "10.0.0.1:5000")
	m.markSessionConnected("1")
	m.markSessionDisconnected("1")

	snap := m.snapshot()
	if len(snap.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(snap.Sessions))
	}
	if snap.Sessions[0].Status != SessionStatusDisconnected {
		t.Fatalf("expected disconnected status, got %s", snap.Sessions[0].Status)
	}
	if snap.Counters.DisconnectedSessions != 1 {
		t.Fatalf("expected disconnected counter 1, got %d", snap.Counters.DisconnectedSessions)
	}
}

func TestMonitorStateEndpointLifecycleAndCloseOnDisconnect(t *testing.T) {
	s, err := NewServer(&Config{})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	s.markSessionPending("7", "127.0.0.1:5555")
	s.markSessionConnected("7")
	s.onEndpointStateChange(tunnel.EndpointStateChange{
		SessionID:  "7",
		Descriptor: "R:18080=>8080",
		State:      tunnel.EndpointStatePending,
	})
	s.onEndpointStateChange(tunnel.EndpointStateChange{
		SessionID:  "7",
		Descriptor: "R:18080=>8080",
		State:      tunnel.EndpointStateActive,
	})
	s.markSessionDisconnected("7")

	snap := s.MonitorSnapshot()
	if len(snap.Endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(snap.Endpoints))
	}
	if snap.Endpoints[0].Status != EndpointStatusClosed {
		t.Fatalf("expected closed endpoint status, got %s", snap.Endpoints[0].Status)
	}
}

func TestMonitorStateSnapshotConcurrency(t *testing.T) {
	s, err := NewServer(&Config{})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("%d", i)
			s.markSessionPending(id, "127.0.0.1:1")
			s.markSessionConnected(id)
			s.onEndpointStateChange(tunnel.EndpointStateChange{
				SessionID:  id,
				Descriptor: "R:1=>1",
				State:      tunnel.EndpointStateActive,
			})
			_ = s.MonitorSnapshot()
			s.markSessionDisconnected(id)
		}(i)
	}
	wg.Wait()
	snap := s.MonitorSnapshot()
	if len(snap.Sessions) != 20 {
		t.Fatalf("expected 20 sessions, got %d", len(snap.Sessions))
	}
}
