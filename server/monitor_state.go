package chserver

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/thomasbruninx/chisel-uds/share/tunnel"
)

type SessionStatus string

const (
	SessionStatusPending      SessionStatus = "pending"
	SessionStatusConnected    SessionStatus = "connected"
	SessionStatusFailed       SessionStatus = "failed"
	SessionStatusDisconnected SessionStatus = "disconnected"
)

type EndpointStatus string

const (
	EndpointStatusPending EndpointStatus = "pending"
	EndpointStatusActive  EndpointStatus = "active"
	EndpointStatusFailed  EndpointStatus = "failed"
	EndpointStatusClosed  EndpointStatus = "closed"
)

type SessionSnapshot struct {
	SessionID   string
	RemoteAddr  string
	Status      SessionStatus
	Error       string
	StartedAt   time.Time
	ConnectedAt time.Time
	UpdatedAt   time.Time
}

type EndpointSnapshot struct {
	SessionID  string
	Descriptor string
	Status     EndpointStatus
	LastError  string
	UpdatedAt  time.Time
}

type MonitorCounters struct {
	PendingSessions      int
	ConnectedSessions    int
	FailedSessions       int
	DisconnectedSessions int
	PendingEndpoints     int
	ActiveEndpoints      int
	FailedEndpoints      int
	ClosedEndpoints      int
}

type MonitorSnapshot struct {
	Sessions  []SessionSnapshot
	Endpoints []EndpointSnapshot
	Counters  MonitorCounters
}

type monitorState struct {
	mu        sync.RWMutex
	sessions  map[string]*SessionSnapshot
	endpoints map[string]*EndpointSnapshot
}

func newMonitorState() *monitorState {
	return &monitorState{
		sessions:  map[string]*SessionSnapshot{},
		endpoints: map[string]*EndpointSnapshot{},
	}
}

func (m *monitorState) snapshot() MonitorSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := MonitorSnapshot{
		Sessions:  make([]SessionSnapshot, 0, len(m.sessions)),
		Endpoints: make([]EndpointSnapshot, 0, len(m.endpoints)),
	}
	for _, s := range m.sessions {
		session := *s
		snapshot.Sessions = append(snapshot.Sessions, session)
		switch s.Status {
		case SessionStatusPending:
			snapshot.Counters.PendingSessions++
		case SessionStatusConnected:
			snapshot.Counters.ConnectedSessions++
		case SessionStatusFailed:
			snapshot.Counters.FailedSessions++
		case SessionStatusDisconnected:
			snapshot.Counters.DisconnectedSessions++
		}
	}
	for _, e := range m.endpoints {
		endpoint := *e
		snapshot.Endpoints = append(snapshot.Endpoints, endpoint)
		switch e.Status {
		case EndpointStatusPending:
			snapshot.Counters.PendingEndpoints++
		case EndpointStatusActive:
			snapshot.Counters.ActiveEndpoints++
		case EndpointStatusFailed:
			snapshot.Counters.FailedEndpoints++
		case EndpointStatusClosed:
			snapshot.Counters.ClosedEndpoints++
		}
	}
	sort.Slice(snapshot.Sessions, func(i, j int) bool {
		return snapshot.Sessions[i].SessionID < snapshot.Sessions[j].SessionID
	})
	sort.Slice(snapshot.Endpoints, func(i, j int) bool {
		if snapshot.Endpoints[i].SessionID == snapshot.Endpoints[j].SessionID {
			return snapshot.Endpoints[i].Descriptor < snapshot.Endpoints[j].Descriptor
		}
		return snapshot.Endpoints[i].SessionID < snapshot.Endpoints[j].SessionID
	})
	return snapshot
}

func (m *monitorState) markSessionPending(sessionID, remoteAddr string) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		s = &SessionSnapshot{
			SessionID: sessionID,
		}
		m.sessions[sessionID] = s
	}
	s.RemoteAddr = remoteAddr
	s.Status = SessionStatusPending
	s.Error = ""
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	s.UpdatedAt = now
}

func (m *monitorState) markSessionConnected(sessionID string) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		s = &SessionSnapshot{SessionID: sessionID}
		m.sessions[sessionID] = s
	}
	s.Status = SessionStatusConnected
	s.Error = ""
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	if s.ConnectedAt.IsZero() {
		s.ConnectedAt = now
	}
	s.UpdatedAt = now
}

func (m *monitorState) markSessionFailed(sessionID, message string) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		s = &SessionSnapshot{SessionID: sessionID}
		m.sessions[sessionID] = s
	}
	s.Status = SessionStatusFailed
	s.Error = message
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	s.UpdatedAt = now
}

func (m *monitorState) markSessionDisconnected(sessionID string) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		s = &SessionSnapshot{SessionID: sessionID}
		m.sessions[sessionID] = s
	}
	s.Status = SessionStatusDisconnected
	s.UpdatedAt = now

	for _, e := range m.endpoints {
		if e.SessionID != sessionID {
			continue
		}
		if e.Status == EndpointStatusActive || e.Status == EndpointStatusPending {
			e.Status = EndpointStatusClosed
			e.UpdatedAt = now
		}
	}
}

func (m *monitorState) updateEndpoint(sessionID, descriptor string, status EndpointStatus, lastErr string) {
	now := time.Now()
	key := sessionID + "|" + descriptor
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.endpoints[key]
	if !ok {
		e = &EndpointSnapshot{
			SessionID:  sessionID,
			Descriptor: descriptor,
		}
		m.endpoints[key] = e
	}
	e.Status = status
	e.LastError = lastErr
	e.UpdatedAt = now
}

func (s *Server) markSessionPending(sessionID, remoteAddr string) {
	s.monitor.markSessionPending(sessionID, remoteAddr)
	if s.otel != nil {
		s.otel.onSessionStatus(context.Background(), sessionID, SessionStatusPending, "", remoteAddr, time.Now())
		s.otel.updateSnapshot(s.monitor.snapshot())
	}
}

func (s *Server) markSessionConnected(sessionID string) {
	s.monitor.markSessionConnected(sessionID)
	if s.otel != nil {
		snap := s.monitor.snapshot()
		startedAt := time.Now()
		for _, sess := range snap.Sessions {
			if sess.SessionID == sessionID {
				startedAt = sess.StartedAt
				break
			}
		}
		s.otel.onSessionStatus(context.Background(), sessionID, SessionStatusConnected, "", "", startedAt)
		s.otel.updateSnapshot(snap)
	}
}

func (s *Server) markSessionFailed(sessionID, message string) {
	s.monitor.markSessionFailed(sessionID, message)
	if s.otel != nil {
		snap := s.monitor.snapshot()
		startedAt := time.Now()
		remoteAddr := ""
		for _, sess := range snap.Sessions {
			if sess.SessionID == sessionID {
				startedAt = sess.StartedAt
				remoteAddr = sess.RemoteAddr
				break
			}
		}
		s.otel.onSessionStatus(context.Background(), sessionID, SessionStatusFailed, message, remoteAddr, startedAt)
		s.otel.updateSnapshot(snap)
	}
}

func (s *Server) markSessionDisconnected(sessionID string) {
	s.monitor.markSessionDisconnected(sessionID)
	if s.otel != nil {
		s.otel.onSessionStatus(context.Background(), sessionID, SessionStatusDisconnected, "", "", time.Now())
		s.otel.updateSnapshot(s.monitor.snapshot())
	}
}

func (s *Server) onEndpointStateChange(change tunnel.EndpointStateChange) {
	status := EndpointStatusClosed
	switch change.State {
	case tunnel.EndpointStatePending:
		status = EndpointStatusPending
	case tunnel.EndpointStateActive:
		status = EndpointStatusActive
	case tunnel.EndpointStateFailed:
		status = EndpointStatusFailed
	case tunnel.EndpointStateClosed:
		status = EndpointStatusClosed
	}
	s.monitor.updateEndpoint(change.SessionID, change.Descriptor, status, change.Error)
	if s.otel != nil {
		s.otel.onEndpointStatus(context.Background(), change.SessionID, change.Descriptor, status, change.Error)
		s.otel.updateSnapshot(s.monitor.snapshot())
	}
}

func (s *Server) onStreamEvent(change tunnel.StreamEvent) {
	if s.otel == nil {
		return
	}
	s.otel.onStreamEvent(context.Background(), change)
}
