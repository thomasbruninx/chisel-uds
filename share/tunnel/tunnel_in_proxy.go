package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thomasbruninx/chisel-uds/share/cio"
	"github.com/thomasbruninx/chisel-uds/share/settings"
	"github.com/jpillora/sizestr"
	"golang.org/x/crypto/ssh"
)

// sshTunnel exposes a subset of Tunnel to subtypes
type sshTunnel interface {
	getSSH(ctx context.Context) ssh.Conn
}

// Proxy is the inbound portion of a Tunnel
type Proxy struct {
	*cio.Logger
	sshTun sshTunnel
	id     int
	count  int
	remote *settings.Remote
	dialer net.Dialer
	tcp    *net.TCPListener
	unix   *net.UnixListener
	udp    *udpListener
	mu     sync.Mutex
	// test hook; if set, used by openRemoteStream
	streamOpener func(context.Context) (io.ReadWriteCloser, error)
	sessionID    string
	onState      func(EndpointStateChange)
	onStream     func(StreamEvent)
	active       bool
}

// NewProxy creates a Proxy
func NewProxy(
	logger *cio.Logger, sshTun sshTunnel, index int, remote *settings.Remote,
	sessionID string, onState func(EndpointStateChange), onStream func(StreamEvent),
) (*Proxy, error) {
	id := index + 1
	p := &Proxy{
		Logger:    logger.Fork("proxy#%s", remote.String()),
		sshTun:    sshTun,
		id:        id,
		remote:    remote,
		sessionID: sessionID,
		onState:   onState,
		onStream:  onStream,
	}
	p.emitEndpointState(EndpointStatePending, "")
	if err := p.listen(); err != nil {
		p.emitEndpointState(EndpointStateFailed, err.Error())
		return nil, err
	}
	p.active = true
	p.emitEndpointState(EndpointStateActive, "")
	return p, nil
}

func (p *Proxy) listen() error {
	if p.remote.Stdio {
		//TODO check if pipes active?
	} else if p.remote.IsUDS() && p.remote.UDSMode == settings.UDSModePair {
		p.Infof("Waiting for UDS peer pairing on %s", p.remote.UDSSocketPath)
	} else if p.remote.IsUDS() && p.remote.UDSMode == settings.UDSModeListen {
		if err := os.MkdirAll(filepath.Dir(p.remote.UDSSocketPath), 0o755); err != nil {
			return p.Errorf("mkdir: %s", err)
		}
		if err := removeUnixSocketIfExists(p.remote.UDSSocketPath); err != nil {
			return p.Errorf("cleanup stale socket: %s", err)
		}
		addr, err := net.ResolveUnixAddr("unix", p.remote.UDSSocketPath)
		if err != nil {
			return p.Errorf("resolve unix: %s", err)
		}
		l, err := net.ListenUnix("unix", addr)
		if err != nil {
			return p.Errorf("unix: %s", err)
		}
		if err := os.Chmod(p.remote.UDSSocketPath, 0o660); err != nil {
			_ = l.Close()
			return p.Errorf("chmod unix socket: %s", err)
		}
		p.Infof("Listening")
		p.unix = l
	} else if p.remote.LocalProto == "tcp" {
		addr, err := net.ResolveTCPAddr("tcp", p.remote.LocalHost+":"+p.remote.LocalPort)
		if err != nil {
			return p.Errorf("resolve: %s", err)
		}
		l, err := net.ListenTCP("tcp", addr)
		if err != nil {
			return p.Errorf("tcp: %s", err)
		}
		p.Infof("Listening")
		p.tcp = l
	} else if p.remote.LocalProto == "udp" {
		l, err := listenUDP(p.Logger, p.sshTun, p.remote)
		if err != nil {
			return err
		}
		p.Infof("Listening")
		p.udp = l
	} else {
		return p.Errorf("unknown local proto")
	}
	return nil
}

// Run enables the proxy and blocks while its active,
// close the proxy by cancelling the context.
func (p *Proxy) Run(ctx context.Context) error {
	defer func() {
		if p.active {
			p.emitEndpointState(EndpointStateClosed, "")
		}
	}()
	if p.remote.Stdio {
		return p.runStdio(ctx)
	} else if p.remote.IsUDS() && p.remote.UDSMode == settings.UDSModePair {
		return defaultUDSPairRegistry.run(ctx, p)
	} else if p.remote.IsUDS() && p.remote.UDSMode == settings.UDSModeListen {
		return p.runUnix(ctx)
	} else if p.remote.LocalProto == "tcp" {
		return p.runTCP(ctx)
	} else if p.remote.LocalProto == "udp" {
		return p.udp.run(ctx)
	}
	panic("should not get here")
}

func (p *Proxy) runStdio(ctx context.Context) error {
	defer p.Infof("Closed")
	for {
		p.pipeRemote(ctx, cio.Stdio)
		select {
		case <-ctx.Done():
			return nil
		default:
			// the connection is not ready yet, keep waiting
		}
	}
}

func (p *Proxy) runTCP(ctx context.Context) error {
	done := make(chan struct{})
	//implements missing net.ListenContext
	go func() {
		select {
		case <-ctx.Done():
			p.tcp.Close()
		case <-done:
		}
	}()
	for {
		src, err := p.tcp.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				//listener closed
				err = nil
			default:
				p.Infof("Accept error: %s", err)
			}
			close(done)
			return err
		}
		p.emitStreamEvent(StreamStateAccepted, p.count+1, "", p.remote.LocalProto)
		go p.pipeRemote(ctx, src)
	}
}

func (p *Proxy) pipeRemote(ctx context.Context, src io.ReadWriteCloser) {
	defer src.Close()

	p.mu.Lock()
	p.count++
	cid := p.count
	p.mu.Unlock()

	l := p.Fork("conn#%d", cid)
	l.Debugf("Open")
	p.emitStreamEvent(StreamStateOpen, cid, "", p.remote.LocalProto)
	dst, err := p.openRemoteStream(ctx)
	if err != nil {
		p.emitStreamEvent(StreamStateError, cid, err.Error(), p.remote.LocalProto)
		l.Infof("Stream error: %s", err)
		return
	}
	defer dst.Close()
	//then pipe
	s, r := cio.Pipe(src, dst)
	p.emitStreamEvent(StreamStateClosed, cid, "", p.remote.LocalProto)
	l.Debugf("Close (sent %s received %s)", sizestr.ToString(s), sizestr.ToString(r))
}

func (p *Proxy) openRemoteStream(ctx context.Context) (io.ReadWriteCloser, error) {
	if p.streamOpener != nil {
		return p.streamOpener(ctx)
	}
	sshConn := p.sshTun.getSSH(ctx)
	if sshConn == nil {
		return nil, p.Errorf("No remote connection")
	}
	dst, reqs, err := sshConn.OpenChannel("chisel", []byte(p.remote.Remote()))
	if err != nil {
		return nil, err
	}
	go ssh.DiscardRequests(reqs)
	return io.ReadWriteCloser(dst), nil
}

func (p *Proxy) runUnix(ctx context.Context) error {
	if p.unix == nil {
		return p.Errorf("unix listener is not initialized")
	}
	defer func() {
		_ = p.unix.Close()
		_ = os.Remove(p.remote.UDSSocketPath)
	}()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = p.unix.Close()
		case <-done:
		}
	}()
	for {
		src, err := p.unix.AcceptUnix()
		if err != nil {
			select {
			case <-ctx.Done():
				err = nil
			default:
				p.Infof("Accept error: %s", err)
			}
			close(done)
			return err
		}
		p.emitStreamEvent(StreamStateAccepted, p.count+1, "", "unix")
		go p.pipeRemote(ctx, src)
	}
}

func removeUnixSocketIfExists(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if (fi.Mode() & os.ModeSocket) == 0 {
		return fmt.Errorf("path exists and is not a unix socket: %s", path)
	}
	return os.Remove(path)
}

func (p *Proxy) emitEndpointState(state EndpointState, message string) {
	if p.onState == nil {
		return
	}
	p.onState(EndpointStateChange{
		SessionID:  p.sessionID,
		Descriptor: p.remote.String(),
		State:      state,
		Error:      message,
	})
}

func (p *Proxy) emitStreamEvent(state StreamState, connID int, message, protocol string) {
	if p.onStream == nil {
		return
	}
	if protocol == "" {
		protocol = "tcp"
	}
	p.onStream(StreamEvent{
		SessionID:  p.sessionID,
		Descriptor: p.remote.String(),
		Protocol:   protocol,
		ConnID:     connID,
		State:      state,
		Error:      message,
		Time:       time.Now(),
	})
}
