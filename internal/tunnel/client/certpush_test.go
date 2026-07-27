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

// fakeSource stands in for the database.
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

// harness wires a client to an in-memory connection and returns both, so a
// test can read back exactly what went onto the wire.
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

// readWriter satisfies io.ReadWriter with a write-only buffer; nothing in
// these tests reads from the peer.
type readWriter struct{ w *bytes.Buffer }

func (rw *readWriter) Read([]byte) (int, error) { return 0, errors.New("not reading") }
func (rw *readWriter) Write(p []byte) (int, error) {
	return rw.w.Write(p)
}

// pushes decodes every CertPush written to the wire.
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
		Domains:       []string{"*.aiqno.com"},
		FullchainPEM:  []byte(chain),
		PrivateKeyPEM: []byte("KEY-" + chain),
		NotAfter:      1893456000,
	}
}

// TestOnlyBoundTunnelsArePushed is the rule the feature rests on.
//
// With several certificates on file, pushing one for a tunnel that names none
// would be a guess, and a wrong guess serves the wrong name — which a browser
// reports to the visitor as an impersonation attempt rather than a
// misconfiguration.
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

// TestDisabledTunnelsAreNotPushed keeps a switched-off tunnel from publishing
// its certificate to the server anyway.
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

// TestUnchangedCertificatesAreNotResent covers the renewal watcher's steady
// state. It runs every minute; without this it would spend a control round
// trip and a store rebuild on the server each time, per tunnel, forever.
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

// TestRenewedCertificateIsPushed is the reason the watcher exists: renewal
// happens in another process, so the running daemon has to notice by itself.
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

// TestReconnectResendsCertificates covers a server that restarted. Its store
// is empty and it will not ask, so a reconnect has to re-push regardless of
// what the previous session sent.
func TestReconnectResendsCertificates(t *testing.T) {
	source := &fakeSource{byID: map[int64]Certificate{7: material("SAME")}}

	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "bound", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 7},
	}, source)

	client.pushCertificates(context.Background(), sess)
	client.pushedCerts.reset() // what serve() does on a new session
	client.pushCertificates(context.Background(), sess)

	if sent := pushes(t, wire); len(sent) != 2 {
		t.Errorf("sent %d pushes across a reconnect, want 2", len(sent))
	}
}

// TestMissingCertificateDoesNotStopOtherTunnels keeps one deleted binding from
// costing every other tunnel its certificate.
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

// TestNoSourceIsNotAnError covers a build or architecture with no database:
// tunnels must still run, and only edge termination is unavailable.
func TestNoSourceIsNotAnError(t *testing.T) {
	client, sess, wire := harness(t, []config.Tunnel{
		{Name: "bound", Enabled: true, Type: "https", TLSMode: "terminate", CertID: 7},
	}, nil)

	client.pushCertificates(context.Background(), sess)

	if sent := pushes(t, wire); len(sent) != 0 {
		t.Errorf("pushed %d certificates with no source configured", len(sent))
	}
}

// TestSessionShutdownDoesNotDeadlock is a regression test for an ordering bug
// that made the client hang forever on its first disconnect.
//
// serve registers a cancel and a waitgroup wait. Defers run last-registered
// first, so registering the cancel first means the wait runs first — and the
// wait blocks on goroutines whose only exit is that cancellation. The symptom
// is brutal to diagnose: the process is alive, procd considers the service
// healthy, there is no connection, nothing reconnects, and nothing is logged.
//
// The test drives the same shape rather than serve itself, which needs a live
// server: a waitgroup holding a goroutine that exits only on cancellation.
func TestSessionShutdownDoesNotDeadlock(t *testing.T) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup

		// The order under test.
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

// TestRejectedRetriesOnlyAStaleClaim covers the restart race.
//
// procd starts the new client before the old one has finished exiting, so the
// new session publishes while the server still holds the old session's routes
// and reaps them a fraction of a second later. Treating that as final leaves
// the tunnel down until something restarts it again — an outage caused by the
// act of applying a configuration change.
//
// A rejection for any other reason is a real disagreement and must not be
// retried, or a genuinely misconfigured tunnel would hammer the server.
func TestRejectedRetriesOnlyAStaleClaim(t *testing.T) {
	cases := []struct {
		name  string
		error string
		retry bool
	}{
		{
			name:  "a route still held by the previous session",
			error: `vhost: "*.aiqno.com" is already routed to tunnel "acgshop" on client "fd31c9e5"`,
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
				Domains: []string{"*.aiqno.com"},
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

// TestRejectedGivesUpEventually stops a permanent conflict becoming a loop.
func TestRejectedGivesUpEventually(t *testing.T) {
	tunnel := config.Tunnel{
		Name: "acgshop", Enabled: true, Type: "https",
		Domains: []string{"*.aiqno.com"},
	}

	_, sess, wire := harness(t, []config.Tunnel{tunnel}, nil)
	sess.tunnels = map[string]config.Tunnel{tunnel.Name: tunnel}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stale := `vhost: "*.aiqno.com" is already routed to tunnel "acgshop" on client "other"`
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

// TestLoginTimesOutOnASilentServer covers the failure that took the client
// down twice in one session.
//
// A server that completes the TCP handshake and then says nothing left the
// client blocked in login forever: alive, no connection, never reconnecting,
// logging nothing, with procd reporting the service healthy. The control loop
// had always had a read deadline; login was the gap.
func TestLoginTimesOutOnASilentServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Accept and say nothing at all, which is what a server mid-shutdown or a
	// middlebox answering on its behalf does.
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
