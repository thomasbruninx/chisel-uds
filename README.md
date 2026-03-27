# Chisel-UDS 

Fork of [Chisel by Jaime Pillora, v1.11.5](https://github.com/jpillora/chisel/tree/v1.11.5) with support for Unix Domain Socket and additional telemetry integration.

## Fork-Specific Additions

This fork includes custom library-level extensions beyond upstream chisel.

### 1) Reverse Unix Domain Socket remotes

New reverse remote formats are supported:
- `R:uds-listen:<socket_path>:<target_host>:<target_port>`
- `R:uds-pair:<socket_path>:<target_host>:<target_port>`

Behavior:
- `uds-listen`: server binds a Unix socket and forwards accepted streams through the client target.
- `uds-pair`: two clients registering the same socket path are FIFO-paired and relayed stream-to-stream.

Implementation notes:
- stale socket cleanup and socket permission handling are included for `uds-listen`.
- parser/encoding support is in `share/settings/remote.go`.

### 2) Native server telemetry snapshot API

Server-side structured telemetry is exposed directly (instead of relying on log parsing):
- `Server.MonitorSnapshot() MonitorSnapshot`

Snapshot includes:
- sessions with statuses: `pending`, `connected`, `failed`, `disconnected`
- endpoints with statuses: `pending`, `active`, `failed`, `closed`
- status counters for both session and endpoint states

Primary implementation is in `server/monitor_state.go`.

### 3) Tunnel lifecycle callbacks

Tunnel config has been extended with callbacks:
- `OnEndpointStateChange func(EndpointStateChange)`
- `OnStreamEvent func(StreamEvent)`

These report endpoint lifecycle transitions and per-stream lifecycle events (`accepted`, `open`, `error`, `closed`).

Primary implementation is in `share/tunnel/tunnel.go` and `share/tunnel/tunnel_in_proxy.go`.

### 4) Optional OpenTelemetry integration

Server config struct includes:
- `Config.OTELEnabled bool`
- `Config.OTELEndpoint string` (OTLP/HTTP target)

When enabled, the fork emits OTEL traces and metrics for:
- session lifecycle
- endpoint lifecycle
- stream lifecycle

Primary implementation is in `server/otel.go`.

## Changelog

- `1.0` - Fork of [Chisel by Jaime Pillora, v1.11.5](https://github.com/jpillora/chisel/tree/v1.11.5), added Unix Domain Socket support and telemetry improvements
## License

Chisal itself is protected by the MIT license 
[Chisal - Jaime Pillora - MIT](https://github.com/jpillora/chisel/blob/master/LICENSE) © Jaime Pillora

My additions are also protected by the MIT license
[Chisal-UDS - Thomas Bruninx - MIT](https://github.com/thomasbruninx/chisel-uds/blob/master/LICENSE) © Thomas Bruninx
