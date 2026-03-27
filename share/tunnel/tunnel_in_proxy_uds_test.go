package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/jpillora/chisel/share/cio"
	"github.com/jpillora/chisel/share/settings"
)

func TestUDSListenProxyForwardsAndCleansSocket(t *testing.T) {
	socketPath := fmt.Sprintf("/tmp/chisel-uds-%d.sock", time.Now().UnixNano())
	if len(socketPath) > 90 {
		socketPath = "/tmp/ch-uds.sock"
	}
	defer os.Remove(socketPath)

	remote := &settings.Remote{
		Reverse:       true,
		UDSMode:       settings.UDSModeListen,
		UDSSocketPath: socketPath,
		LocalProto:    "unix",
		RemoteHost:    "127.0.0.1",
		RemotePort:    "8080",
		RemoteProto:   "tcp",
	}
	p, err := NewProxy(cio.NewLogger("test"), nil, 0, remote, "1", nil, nil)
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}

	targetConn, tunnelConn := net.Pipe()
	defer targetConn.Close()
	p.streamOpener = func(context.Context) (io.ReadWriteCloser, error) {
		return tunnelConn, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	var srcConn net.Conn
	for time.Now().Before(deadline) {
		srcConn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial unix socket: %v", err)
	}
	defer srcConn.Close()

	want := []byte("hello-over-uds")
	if _, err := srcConn.Write(want); err != nil {
		t.Fatalf("write to uds: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(targetConn, got); err != nil {
		t.Fatalf("read forwarded bytes: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected forwarded payload: got %q want %q", string(got), string(want))
	}

	reply := []byte("reply")
	if _, err := targetConn.Write(reply); err != nil {
		t.Fatalf("write reply: %v", err)
	}
	back := make([]byte, len(reply))
	if _, err := io.ReadFull(srcConn, back); err != nil {
		t.Fatalf("read reply via uds: %v", err)
	}
	if string(back) != string(reply) {
		t.Fatalf("unexpected reply payload: got %q want %q", string(back), string(reply))
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("proxy run returned error: %v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("expected socket cleanup after shutdown, stat err=%v", err)
	}
}

func TestUDSPairRelaysBetweenTwoClients(t *testing.T) {
	socketPath := "/tmp/chisel-test-pair.sock"
	registry := &udsPairRegistry{waiters: map[string][]*udsPairWaiter{}}

	proxyA := &Proxy{
		Logger: cio.NewLogger("pair-a"),
		remote: &settings.Remote{UDSMode: settings.UDSModePair, UDSSocketPath: socketPath},
	}
	proxyB := &Proxy{
		Logger: cio.NewLogger("pair-b"),
		remote: &settings.Remote{UDSMode: settings.UDSModePair, UDSSocketPath: socketPath},
	}

	aLocal, aRemote := net.Pipe()
	defer aLocal.Close()
	proxyA.streamOpener = func(context.Context) (io.ReadWriteCloser, error) { return aRemote, nil }

	bLocal, bRemote := net.Pipe()
	defer bLocal.Close()
	proxyB.streamOpener = func(context.Context) (io.ReadWriteCloser, error) { return bRemote, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneA := make(chan error, 1)
	doneB := make(chan error, 1)
	go func() { doneA <- registry.run(ctx, proxyA) }()
	time.Sleep(20 * time.Millisecond)
	go func() { doneB <- registry.run(ctx, proxyB) }()

	msgAB := []byte("from-a")
	if _, err := aLocal.Write(msgAB); err != nil {
		t.Fatalf("write a->b: %v", err)
	}
	gotAB := make([]byte, len(msgAB))
	if _, err := io.ReadFull(bLocal, gotAB); err != nil {
		t.Fatalf("read a->b: %v", err)
	}
	if string(gotAB) != string(msgAB) {
		t.Fatalf("unexpected a->b payload: got %q want %q", string(gotAB), string(msgAB))
	}

	msgBA := []byte("from-b")
	if _, err := bLocal.Write(msgBA); err != nil {
		t.Fatalf("write b->a: %v", err)
	}
	gotBA := make([]byte, len(msgBA))
	if _, err := io.ReadFull(aLocal, gotBA); err != nil {
		t.Fatalf("read b->a: %v", err)
	}
	if string(gotBA) != string(msgBA) {
		t.Fatalf("unexpected b->a payload: got %q want %q", string(gotBA), string(msgBA))
	}

	aLocal.Close()
	bLocal.Close()
	if err := <-doneA; err != nil {
		t.Fatalf("proxy A returned error: %v", err)
	}
	if err := <-doneB; err != nil {
		t.Fatalf("proxy B returned error: %v", err)
	}
}

func TestUDSPairWaiterRemovedOnDisconnect(t *testing.T) {
	socketPath := "/tmp/chisel-test-disconnect.sock"
	registry := &udsPairRegistry{waiters: map[string][]*udsPairWaiter{}}
	proxy := &Proxy{
		Logger: cio.NewLogger("pair"),
		remote: &settings.Remote{UDSMode: settings.UDSModePair, UDSSocketPath: socketPath},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- registry.run(ctx, proxy)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("pair run returned error: %v", err)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if n := len(registry.waiters[socketPath]); n != 0 {
		t.Fatalf("expected empty waiter queue, got %d", n)
	}
}
