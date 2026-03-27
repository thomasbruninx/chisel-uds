package chserver

import (
	"context"
	"testing"
	"time"

	"github.com/thomasbruninx/chisel-uds/share/tunnel"
)

func TestOTELPublisherSessionAndEndpointDurationsTracked(t *testing.T) {
	p := &otelPublisher{}
	start := time.Now().Add(-2 * time.Second)
	p.onSessionStatus(context.Background(), "s1", SessionStatusPending, "", "127.0.0.1:1", start)
	p.onSessionStatus(context.Background(), "s1", SessionStatusConnected, "", "127.0.0.1:1", start)
	p.onEndpointStatus(context.Background(), "s1", "R:18080=>8080", EndpointStatusPending, "")
	p.onEndpointStatus(context.Background(), "s1", "R:18080=>8080", EndpointStatusActive, "")
	p.onSessionStatus(context.Background(), "s1", SessionStatusDisconnected, "", "", start)
	if _, ok := p.sessionStartedAt["s1"]; ok {
		t.Fatalf("expected session start tracking removed after disconnect")
	}
}

func TestOTELPublisherStreamEventTracking(t *testing.T) {
	p := &otelPublisher{}
	t0 := time.Now()
	open := tunnel.StreamEvent{
		SessionID:  "s1",
		Descriptor: "R:18080=>8080",
		Protocol:   "tcp",
		ConnID:     1,
		State:      tunnel.StreamStateOpen,
		Time:       t0,
	}
	closeEv := open
	closeEv.State = tunnel.StreamStateClosed
	closeEv.Time = t0.Add(500 * time.Millisecond)

	p.onStreamEvent(context.Background(), open)
	if len(p.streamOpenAt) != 1 {
		t.Fatalf("expected stream open tracking")
	}
	p.onStreamEvent(context.Background(), closeEv)
	if len(p.streamOpenAt) != 0 {
		t.Fatalf("expected stream open tracking removed on close")
	}
}
