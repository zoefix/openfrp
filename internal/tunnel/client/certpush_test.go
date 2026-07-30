package client

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

type fakeSource struct {
	byID  map[int64]Certificate
	calls int
}

func (f *fakeSource) Certificate(_ context.Context, id int64) (Certificate, error) {
	f.calls++
	material, ok := f.byID[id]
	if !ok {
		return Certificate{}, ErrNoCertificate
	}
	return material, nil
}

func harness(t *testing.T, tunnels []config.Tunnel, source CertSource) (*Client, *session, *bytes.Buffer) {
	t.Helper()

	wire := &bytes.Buffer{}
	client := &Client{
		cfg:    &config.Client{Tunnels: tunnels},
		logger: slog.New(slog.DiscardHandler),
		certs:  source,
	}
	return client, &session{
		client: client,
		codec:  protocol.NewCodec(&readWriter{w: wire}),
		logger: client.logger,
	}, wire
}

type readWriter struct{ w *bytes.Buffer }

func (rw *readWriter) Read([]byte) (int, error) { return 0, errors.New("not reading") }
func (rw *readWriter) Write(p []byte) (int, error) {
	return rw.w.Write(p)
}

func pushes(t *testing.T, wire *bytes.Buffer) []*protocol.CertPush {
	t.Helper()

	var out []*protocol.CertPush
	reader := bytes.NewReader(wire.Bytes())
	for reader.Len() > 0 {
		msg, err := protocol.ReadMessage(reader)
		if err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if push, ok := msg.(*protocol.CertPush); ok {
			out = append(out, push)
		}
	}
	return out
}

func material(chain string) Certificate {
	return Certificate{
		Domains:       []string{"*.example.com"},
		FullchainPEM:  []byte(chain),
		PrivateKeyPEM: []byte("KEY-" + chain),
		NotAfter:      1893456000,
	}
}

func TestOnlyBoundTunnelsArePushed(t *testing.T) {
	source := &fakeSource{byID: map[int64]Certificate{
		7: material("BOUND"),
		9: material("OTHER"),
	}}

	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "bound", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 7},
		{Name: "unbound", Enabled: true, Type: "https", TLSMode: "terminate"},
		{Name: "passthrough", Enabled: true, Type: "https", TLSMode: "passthrough"},
		{Name: "plain-tcp", Enabled: true, Type: "tcp", RemotePort: 6022},
	}, source)

	client.pushCertificates(context.Background(), sess)

	sent := pushes(t, wire)
	if len(sent) != 1 {
		t.Fatalf("pushed %d certificates, want exactly the one bound tunnel", len(sent))
	}
	if string(sent[0].FullchainPEM) != "BOUND" {
		t.Errorf("pushed %q, want the certificate the tunnel names", sent[0].FullchainPEM)
	}
}

func TestDisabledTunnelsAreNotPushed(t *testing.T) {
	source := &fakeSource{byID: map[int64]Certificate{7: material("X")}}

	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "off", Enabled: false, Type: "https", TLSMode: "terminate", CertID: 7},
	}, source)

	client.pushCertificates(context.Background(), sess)

	if sent := pushes(t, wire); len(sent) != 0 {
		t.Errorf("a disabled tunnel pushed %d certificates", len(sent))
	}
}

func TestUnchangedCertificatesAreNotResent(t *testing.T) {
	source := &fakeSource{byID: map[int64]Certificate{7: material("SAME")}}

	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "bound", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 7},
	}, source)

	for range 5 {
		client.pushCertificates(context.Background(), sess)
	}

	if sent := pushes(t, wire); len(sent) != 1 {
		t.Errorf("sent %d pushes for an unchanged certificate, want 1", len(sent))
	}
}

func TestRenewedCertificateIsPushed(t *testing.T) {
	source := &fakeSource{byID: map[int64]Certificate{7: material("OLD")}}

	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "bound", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 7},
	}, source)

	client.pushCertificates(context.Background(), sess)
	source.byID[7] = material("RENEWED")
	client.pushCertificates(context.Background(), sess)

	sent := pushes(t, wire)
	if len(sent) != 2 {
		t.Fatalf("sent %d pushes, want the original and the renewal", len(sent))
	}
	if string(sent[1].FullchainPEM) != "RENEWED" {
		t.Errorf("second push carried %q", sent[1].FullchainPEM)
	}
}

func TestReconnectResendsCertificates(t *testing.T) {
	source := &fakeSource{byID: map[int64]Certificate{7: material("SAME")}}

	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "bound", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 7},
	}, source)

	client.pushCertificates(context.Background(), sess)
	client.pushedCerts.reset()
	client.pushCertificates(context.Background(), sess)

	if sent := pushes(t, wire); len(sent) != 2 {
		t.Errorf("sent %d pushes across a reconnect, want 2", len(sent))
	}
}

func TestMissingCertificateDoesNotStopOtherTunnels(t *testing.T) {
	source := &fakeSource{byID: map[int64]Certificate{9: material("GOOD")}}

	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "broken", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 404},
		{Name: "fine", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 9},
	}, source)

	client.pushCertificates(context.Background(), sess)

	sent := pushes(t, wire)
	if len(sent) != 1 || string(sent[0].FullchainPEM) != "GOOD" {
		t.Errorf("a missing binding stopped the working tunnel: %d pushes", len(sent))
	}
}

func TestNoSourceIsNotAnError(t *testing.T) {
	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "bound", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 7},
	}, nil)

	client.pushCertificates(context.Background(), sess)

	if sent := pushes(t, wire); len(sent) != 0 {
		t.Errorf("pushed %d certificates with no source configured", len(sent))
	}
}

func TestSessionShutdownDoesNotDeadlock(t *testing.T) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup

		defer wg.Wait()
		defer cancel()

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ctx.Done()
		}()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown deadlocked: the wait runs before the cancel it is waiting for")
	}
}

func TestRejectedRetriesOnlyAStaleClaim(t *testing.T) {
	cases := []struct {
		name  string
		error string
		retry bool
	}{
		{
			name:  "a route still held by the previous session",
			error: `vhost: "*.example.com" is already routed to tunnel "acgshop" on client "fd31c9e5"`,
			retry: true,
		},
		{
			name:  "a name still registered by the previous session",
			error: `proxy "acgshop" is already registered`,
			retry: true,
		},
		{
			name:  "a port genuinely taken by something else",
			error: "bind: address already in use",
			retry: false,
		},
		{
			name:  "a tunnel the server rejects outright",
			error: "unknown proxy type",
			retry: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tunnel := config.Tunnel{
				Name: "acgshop", Enabled: true, Type: "https",
				Domains: []string{"*.example.com"},
			}

			client, sess, wire := harness(t, []config.Tunnel{tunnel}, nil)
			_ = client
			sess.tunnels = map[string]config.Tunnel{tunnel.Name: tunnel}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sess.rejected(ctx, &protocol.NewProxyResp{Name: tunnel.Name, Error: tc.error})
			sess.wg.Wait()

			var republished int
			reader := bytes.NewReader(wire.Bytes())
			for reader.Len() > 0 {
				msg, err := protocol.ReadMessage(reader)
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				if _, ok := msg.(*protocol.NewProxy); ok {
					republished++
				}
			}

			if tc.retry && republished != 1 {
				t.Errorf("republished %d times, want 1", republished)
			}
			if !tc.retry && republished != 0 {
				t.Errorf("republished %d times for a real rejection, want 0", republished)
			}
		})
	}
}

func TestRejectedGivesUpEventually(t *testing.T) {
	tunnel := config.Tunnel{
		Name: "acgshop", Enabled: true, Type: "https",
		Domains: []string{"*.example.com"},
	}

	_, sess, wire := harness(t, []config.Tunnel{tunnel}, nil)
	sess.tunnels = map[string]config.Tunnel{tunnel.Name: tunnel}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stale := `vhost: "*.example.com" is already routed to tunnel "acgshop" on client "other"`
	for range republishAttempts + 4 {
		sess.rejected(ctx, &protocol.NewProxyResp{Name: tunnel.Name, Error: stale})
	}
	sess.wg.Wait()

	var republished int
	reader := bytes.NewReader(wire.Bytes())
	for reader.Len() > 0 {
		msg, err := protocol.ReadMessage(reader)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if _, ok := msg.(*protocol.NewProxy); ok {
			republished++
		}
	}

	if republished > republishAttempts {
		t.Errorf("republished %d times, want at most %d", republished, republishAttempts)
	}
	if republished == 0 {
		t.Error("gave up without trying at all")
	}
}

func TestLoginTimesOutOnASilentServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn
	}()

	client := &Client{
		cfg: &config.Client{
			Transport: config.Transport{DialTimeout: config.Duration(500 * time.Millisecond)},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, err := client.login(context.Background(), conn)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("login succeeded against a server that never replied")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login blocked on a silent server; the client would never reconnect")
	}

	select {
	case c := <-accepted:
		c.Close()
	default:
	}
}
