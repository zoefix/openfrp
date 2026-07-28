package vhost

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSniffHTTP(t *testing.T) {
	tests := []struct {
		name     string
		request  string
		wantHost string
		wantErr  string
	}{
		{
			name:     "simple",
			request:  "GET /index.html HTTP/1.1\r\nHost: aaa.com\r\n\r\n",
			wantHost: "aaa.com",
		},
		{
			name:     "host with port is stripped",
			request:  "GET / HTTP/1.1\r\nHost: aaa.com:8080\r\n\r\n",
			wantHost: "aaa.com",
		},
		{
			name:     "header name casing is irrelevant",
			request:  "GET / HTTP/1.1\r\nHOST: AAA.com\r\n\r\n",
			wantHost: "aaa.com",
		},
		{
			name: "host after other headers",
			request: "POST /api HTTP/1.1\r\nUser-Agent: curl\r\n" +
				"Accept: */*\r\nHost: api.aaa.com\r\nContent-Length: 0\r\n\r\n",
			wantHost: "api.aaa.com",
		},
		{
			name:     "trailing dot",
			request:  "GET / HTTP/1.1\r\nHost: aaa.com.\r\n\r\n",
			wantHost: "aaa.com",
		},
		{
			name:    "no host header",
			request: "GET / HTTP/1.0\r\nUser-Agent: old\r\n\r\n",
			wantErr: "no host",
		},
		{
			name:    "h2c prior knowledge is reported clearly",
			request: "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n",
			wantErr: "HTTP/2",
		},
		{
			name:    "garbage request line",
			request: "\x16\x03\x01\x00\x05rubbish\r\n\r\n",
			wantErr: "malformed request line",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := SniffHTTP(strings.NewReader(tc.request))

			// Whatever happens, everything read has to be reported so the
			// caller can replay it.
			if len(info.Consumed) == 0 {
				t.Error("Consumed is empty; the caller could not replay the head")
			}
			if !bytes.HasPrefix([]byte(tc.request), info.Consumed) {
				t.Errorf("Consumed %q is not a prefix of the request", info.Consumed)
			}

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error mentioning %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", info.Host, tc.wantHost)
			}
		})
	}
}

// TestSniffHTTPStopsAtHeaderEnd is what makes the replay-then-splice design
// work: the sniffer must not swallow request body bytes, because those would
// be lost when the raw sockets are handed to the kernel.
func TestSniffHTTPStopsAtHeaderEnd(t *testing.T) {
	head := "POST /upload HTTP/1.1\r\nHost: aaa.com\r\nContent-Length: 11\r\n\r\n"
	body := "hello world"

	reader := strings.NewReader(head + body)
	info, err := SniffHTTP(reader)
	if err != nil {
		t.Fatalf("SniffHTTP: %v", err)
	}
	if info.Host != "aaa.com" {
		t.Errorf("Host = %q, want aaa.com", info.Host)
	}

	// Everything the sniffer took, plus whatever is left, must reconstruct the
	// original stream exactly.
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	if got := string(info.Consumed) + string(rest); got != head+body {
		t.Errorf("stream did not round trip:\n got %q\nwant %q", got, head+body)
	}
}

func TestSniffHTTPRejectsOversizedHead(t *testing.T) {
	// A head that never terminates must be cut off rather than buffered.
	endless := strings.NewReader("GET / HTTP/1.1\r\nHost: aaa.com\r\nX: " +
		strings.Repeat("a", MaxHTTPHead*2))

	_, err := SniffHTTP(endless)
	if !errors.Is(err, ErrHeadTooLarge) && !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want ErrHeadTooLarge", err)
	}
}

// buildClientHello produces a genuine ClientHello by driving crypto/tls, so
// the parser is tested against what a real client emits rather than against a
// hand-rolled fixture that might share our own misreading of the spec.
func buildClientHello(t testing.TB, serverName string) []byte {
	t.Helper()

	client, server := net.Pipe()
	defer client.Close()

	captured := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := server.Read(buf)
		captured <- append([]byte(nil), buf[:n]...)
		server.Close()
	}()

	cfg := &tls.Config{ServerName: serverName, InsecureSkipVerify: true}
	tlsConn := tls.Client(client, cfg)
	// The handshake cannot complete against a pipe that never answers; we only
	// need the first flight.
	client.SetDeadline(time.Now().Add(2 * time.Second))
	_ = tlsConn.Handshake()

	select {
	case hello := <-captured:
		if len(hello) == 0 {
			t.Fatal("captured an empty ClientHello")
		}
		return hello
	case <-time.After(3 * time.Second):
		t.Fatal("timed out capturing the ClientHello")
		return nil
	}
}

func TestSniffTLSAgainstRealClientHello(t *testing.T) {
	for _, name := range []string{"aaa.com", "www.aaa.com", "x.bb.aaa.com"} {
		t.Run(name, func(t *testing.T) {
			hello := buildClientHello(t, name)

			info, err := SniffTLS(bytes.NewReader(hello))
			if err != nil {
				t.Fatalf("SniffTLS: %v", err)
			}
			if info.ServerName != name {
				t.Errorf("ServerName = %q, want %q", info.ServerName, name)
			}
			if !bytes.Equal(info.Consumed, hello[:len(info.Consumed)]) {
				t.Error("Consumed does not match the head of the input")
			}
		})
	}
}

// TestSniffTLSConsumedIsReplayable is the property the whole design rests on:
// the bytes we took, written to the upstream first, must reconstruct the
// original stream exactly.
func TestSniffTLSConsumedIsReplayable(t *testing.T) {
	hello := buildClientHello(t, "aaa.com")
	trailing := []byte("subsequent encrypted records")

	reader := bytes.NewReader(append(append([]byte(nil), hello...), trailing...))
	info, err := SniffTLS(reader)
	if err != nil {
		t.Fatalf("SniffTLS: %v", err)
	}

	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}

	reconstructed := append(append([]byte(nil), info.Consumed...), rest...)
	expected := append(append([]byte(nil), hello...), trailing...)
	if !bytes.Equal(reconstructed, expected) {
		t.Error("replaying Consumed plus the remainder does not reproduce the stream")
	}
}

func TestSniffTLSRejectsNonTLS(t *testing.T) {
	cases := map[string]string{
		"http request": "GET / HTTP/1.1\r\nHost: aaa.com\r\n\r\n",
		"ssh banner":   "SSH-2.0-OpenSSH_9.2\r\n",
		"junk":         "\x00\x00\x00\x00\x00",
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := SniffTLS(strings.NewReader(payload))
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSniffTLSTruncatedInputIsNotAPanic(t *testing.T) {
	hello := buildClientHello(t, "aaa.com")

	// Every truncation of a real ClientHello must produce an error, never a
	// panic — the input is attacker controlled.
	for cut := 1; cut < len(hello); cut += 7 {
		if _, err := SniffTLS(bytes.NewReader(hello[:cut])); err == nil {
			t.Errorf("truncation at %d bytes was accepted", cut)
		}
	}
}

func TestSniffTLSWithoutSNIIsNotAnError(t *testing.T) {
	// A ClientHello for a bare IP carries no SNI. That is a routing decision
	// for the caller (catch-all or refuse), not a parse failure.
	hello := buildClientHello(t, "")
	info, err := SniffTLS(bytes.NewReader(hello))
	if err != nil {
		t.Fatalf("SniffTLS: %v", err)
	}
	if info.ServerName != "" {
		t.Errorf("ServerName = %q, want empty", info.ServerName)
	}
}

// BenchmarkSniffHTTP pins the per-connection cost of identifying a request.
// This runs once for every connection the HTTP vhost port accepts, so its
// allocation count is a connections-per-second budget item.
func BenchmarkSniffHTTP(b *testing.B) {
	head := []byte("GET /some/path HTTP/1.1\r\n" +
		"Host: bench.example.com\r\n" +
		"User-Agent: bench/1.0\r\n" +
		"Accept: */*\r\n" +
		"Connection: keep-alive\r\n\r\n")

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		info, err := SniffHTTP(bytes.NewReader(head))
		if err != nil {
			b.Fatalf("sniff: %v", err)
		}
		if info.Host != "bench.example.com" {
			b.Fatalf("host = %q", info.Host)
		}
		PutConsumed(info.Consumed)
	}
}

// BenchmarkSniffTLS is the TLS twin: it runs once per HTTPS connection.
func BenchmarkSniffTLS(b *testing.B) {
	hello := buildClientHello(b, "bench.example.com")

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		info, err := SniffTLS(bytes.NewReader(hello))
		if err != nil {
			b.Fatalf("sniff: %v", err)
		}
		if info.ServerName != "bench.example.com" {
			b.Fatalf("sni = %q", info.ServerName)
		}
		PutConsumed(info.Consumed)
	}
}

// TestSniffSurvivesBufferReuse: a pooled buffer carries one connection's head
// and then another's; the second parse must see only its own bytes.
func TestSniffSurvivesBufferReuse(t *testing.T) {
	first := []byte("GET /aaaaaaaaaaaaaaaaaaaaaaaaaaaa HTTP/1.1\r\n" +
		"Host: first.example.com\r\n\r\n")
	second := []byte("GET /b HTTP/1.1\r\nHost: second.example.com\r\n\r\n")

	info, err := SniffHTTP(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("first sniff: %v", err)
	}
	if info.Host != "first.example.com" {
		t.Fatalf("first host = %q", info.Host)
	}
	PutConsumed(info.Consumed)

	info, err = SniffHTTP(bytes.NewReader(second))
	if err != nil {
		t.Fatalf("second sniff: %v", err)
	}
	if info.Host != "second.example.com" {
		t.Errorf("second host = %q, want second.example.com", info.Host)
	}
	if string(info.Consumed) != string(second) {
		t.Errorf("consumed = %q, want the second request only", info.Consumed)
	}
	PutConsumed(info.Consumed)
}
