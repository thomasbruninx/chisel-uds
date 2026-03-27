package chserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/thomasbruninx/chisel-uds/share/cio"
	"github.com/thomasbruninx/chisel-uds/share/tunnel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

type otelPublisher struct {
	tracer trace.Tracer

	meter metric.Meter

	sessionGauge  metric.Int64ObservableGauge
	endpointGauge metric.Int64ObservableGauge

	sessionFailureCounter  metric.Int64Counter
	endpointFailureCounter metric.Int64Counter
	streamErrorCounter     metric.Int64Counter
	streamAcceptedCounter  metric.Int64Counter

	handshakeDuration     metric.Float64Histogram
	sessionDuration       metric.Float64Histogram
	endpointSetupDuration metric.Float64Histogram
	streamDuration        metric.Float64Histogram

	mu                sync.Mutex
	sessionStartedAt  map[string]time.Time
	endpointPendingAt map[string]time.Time
	streamOpenAt      map[string]time.Time
	lastSnapshot      MonitorSnapshot
}

func newOTELPublisher(logger *cio.Logger, endpoint string) (*otelPublisher, func(context.Context) error, error) {
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	if endpoint == "" {
		return nil, nil, fmt.Errorf("otel endpoint is empty")
	}

	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create otel trace exporter: %w", err)
	}

	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName("http-tcpudp-tunnel-server"),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create otel resource: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	p := &otelPublisher{
		tracer:            tracerProvider.Tracer("github.com/thomasbruninx/chisel-uds/server"),
		meter:             meterProvider.Meter("github.com/thomasbruninx/chisel-uds/server"),
		sessionStartedAt:  map[string]time.Time{},
		endpointPendingAt: map[string]time.Time{},
		streamOpenAt:      map[string]time.Time{},
	}
	if err := p.initMetrics(); err != nil {
		return nil, nil, err
	}
	logger.Infof("otel: exporter initialized (otlp/http -> %s)", endpoint)

	shutdown := func(ctx context.Context) error {
		if err := meterProvider.Shutdown(ctx); err != nil {
			return err
		}
		return tracerProvider.Shutdown(ctx)
	}
	return p, shutdown, nil
}

func (p *otelPublisher) initMetrics() error {
	var err error
	if p.sessionGauge, err = p.meter.Int64ObservableGauge("chisel_sessions_status"); err != nil {
		return err
	}
	if p.endpointGauge, err = p.meter.Int64ObservableGauge("chisel_endpoints_status"); err != nil {
		return err
	}
	if p.sessionFailureCounter, err = p.meter.Int64Counter("chisel_session_failures_total"); err != nil {
		return err
	}
	if p.endpointFailureCounter, err = p.meter.Int64Counter("chisel_endpoint_failures_total"); err != nil {
		return err
	}
	if p.streamErrorCounter, err = p.meter.Int64Counter("chisel_stream_errors_total"); err != nil {
		return err
	}
	if p.streamAcceptedCounter, err = p.meter.Int64Counter("chisel_streams_accepted_total"); err != nil {
		return err
	}
	if p.handshakeDuration, err = p.meter.Float64Histogram("chisel_handshake_duration_seconds"); err != nil {
		return err
	}
	if p.sessionDuration, err = p.meter.Float64Histogram("chisel_session_duration_seconds"); err != nil {
		return err
	}
	if p.endpointSetupDuration, err = p.meter.Float64Histogram("chisel_endpoint_setup_duration_seconds"); err != nil {
		return err
	}
	if p.streamDuration, err = p.meter.Float64Histogram("chisel_stream_duration_seconds"); err != nil {
		return err
	}

	_, err = p.meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		p.mu.Lock()
		snap := p.lastSnapshot
		p.mu.Unlock()
		observer.ObserveInt64(p.sessionGauge, int64(snap.Counters.PendingSessions), metric.WithAttributes(attribute.String("status", string(SessionStatusPending))))
		observer.ObserveInt64(p.sessionGauge, int64(snap.Counters.ConnectedSessions), metric.WithAttributes(attribute.String("status", string(SessionStatusConnected))))
		observer.ObserveInt64(p.sessionGauge, int64(snap.Counters.FailedSessions), metric.WithAttributes(attribute.String("status", string(SessionStatusFailed))))
		observer.ObserveInt64(p.sessionGauge, int64(snap.Counters.DisconnectedSessions), metric.WithAttributes(attribute.String("status", string(SessionStatusDisconnected))))

		observer.ObserveInt64(p.endpointGauge, int64(snap.Counters.PendingEndpoints), metric.WithAttributes(attribute.String("status", string(EndpointStatusPending))))
		observer.ObserveInt64(p.endpointGauge, int64(snap.Counters.ActiveEndpoints), metric.WithAttributes(attribute.String("status", string(EndpointStatusActive))))
		observer.ObserveInt64(p.endpointGauge, int64(snap.Counters.FailedEndpoints), metric.WithAttributes(attribute.String("status", string(EndpointStatusFailed))))
		observer.ObserveInt64(p.endpointGauge, int64(snap.Counters.ClosedEndpoints), metric.WithAttributes(attribute.String("status", string(EndpointStatusClosed))))
		return nil
	}, p.sessionGauge, p.endpointGauge)
	return err
}

func (p *otelPublisher) updateSnapshot(snapshot MonitorSnapshot) {
	p.mu.Lock()
	p.lastSnapshot = snapshot
	p.mu.Unlock()
}

func (p *otelPublisher) onSessionStatus(ctx context.Context, sessionID string, status SessionStatus, errText string, remoteAddr string, startedAt time.Time) {
	if p == nil {
		return
	}
	if p.tracer != nil {
		attrs := []attribute.KeyValue{
			attribute.String("session.status", string(status)),
		}
		if remoteAddr != "" {
			attrs = append(attrs, attribute.String("net.peer", remoteAddr))
		}
		_, span := p.tracer.Start(ctx, "chisel.session.lifecycle", trace.WithAttributes(attrs...))
		if errText != "" {
			span.SetAttributes(attribute.String("error.message", errText))
		}
		span.End()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionStartedAt == nil {
		p.sessionStartedAt = map[string]time.Time{}
	}
	switch status {
	case SessionStatusPending:
		p.sessionStartedAt[sessionID] = startedAt
	case SessionStatusConnected, SessionStatusFailed:
		if t0, ok := p.sessionStartedAt[sessionID]; ok && !t0.IsZero() {
			if p.handshakeDuration != nil {
				p.handshakeDuration.Record(ctx, time.Since(t0).Seconds(), metric.WithAttributes(attribute.String("outcome", string(status))))
			}
		}
		if status == SessionStatusFailed {
			if p.sessionFailureCounter != nil {
				p.sessionFailureCounter.Add(ctx, 1)
			}
		}
	case SessionStatusDisconnected:
		if t0, ok := p.sessionStartedAt[sessionID]; ok && !t0.IsZero() {
			if p.sessionDuration != nil {
				p.sessionDuration.Record(ctx, time.Since(t0).Seconds())
			}
			delete(p.sessionStartedAt, sessionID)
		}
	}
}

func (p *otelPublisher) onEndpointStatus(ctx context.Context, sessionID, descriptor string, status EndpointStatus, errText string) {
	if p == nil {
		return
	}
	mode := endpointMode(descriptor)
	if p.tracer != nil {
		_, span := p.tracer.Start(ctx, "chisel.endpoint.lifecycle", trace.WithAttributes(
			attribute.String("endpoint.status", string(status)),
			attribute.String("endpoint.mode", mode),
		))
		if errText != "" {
			span.SetAttributes(attribute.String("error.message", errText))
		}
		span.End()
	}

	key := sessionID + "|" + descriptor
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.endpointPendingAt == nil {
		p.endpointPendingAt = map[string]time.Time{}
	}
	switch status {
	case EndpointStatusPending:
		p.endpointPendingAt[key] = time.Now()
	case EndpointStatusActive, EndpointStatusFailed:
		if t0, ok := p.endpointPendingAt[key]; ok {
			if p.endpointSetupDuration != nil {
				p.endpointSetupDuration.Record(ctx, time.Since(t0).Seconds(), metric.WithAttributes(attribute.String("outcome", string(status)), attribute.String("endpoint.mode", mode)))
			}
		}
		if status == EndpointStatusFailed {
			if p.endpointFailureCounter != nil {
				p.endpointFailureCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("endpoint.mode", mode)))
			}
		}
	case EndpointStatusClosed:
		delete(p.endpointPendingAt, key)
	}
}

func (p *otelPublisher) onStreamEvent(ctx context.Context, ev tunnel.StreamEvent) {
	if p == nil {
		return
	}
	mode := endpointMode(ev.Descriptor)
	attrs := []attribute.KeyValue{
		attribute.String("stream.state", string(ev.State)),
		attribute.String("endpoint.mode", mode),
		attribute.String("stream.proto", ev.Protocol),
	}
	if p.tracer != nil {
		_, span := p.tracer.Start(ctx, "chisel.stream.event", trace.WithAttributes(attrs...))
		if ev.Error != "" {
			span.SetAttributes(attribute.String("error.message", ev.Error))
		}
		span.End()
	}

	key := fmt.Sprintf("%s|%s|%d", ev.SessionID, ev.Descriptor, ev.ConnID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streamOpenAt == nil {
		p.streamOpenAt = map[string]time.Time{}
	}
	switch ev.State {
	case tunnel.StreamStateAccepted:
		if p.streamAcceptedCounter != nil {
			p.streamAcceptedCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("endpoint.mode", mode), attribute.String("stream.proto", ev.Protocol)))
		}
	case tunnel.StreamStateOpen:
		p.streamOpenAt[key] = ev.Time
	case tunnel.StreamStateError:
		if p.streamErrorCounter != nil {
			p.streamErrorCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("endpoint.mode", mode), attribute.String("stream.proto", ev.Protocol)))
		}
	case tunnel.StreamStateClosed:
		if t0, ok := p.streamOpenAt[key]; ok {
			if p.streamDuration != nil {
				p.streamDuration.Record(ctx, ev.Time.Sub(t0).Seconds(), metric.WithAttributes(attribute.String("endpoint.mode", mode), attribute.String("stream.proto", ev.Protocol)))
			}
			delete(p.streamOpenAt, key)
		}
	}
}

func endpointMode(descriptor string) string {
	switch {
	case strings.Contains(descriptor, "uds-listen"):
		return "uds-listen"
	case strings.Contains(descriptor, "uds-pair"):
		return "uds-pair"
	case strings.HasSuffix(descriptor, "/udp"):
		return "udp"
	default:
		return "tcp"
	}
}
