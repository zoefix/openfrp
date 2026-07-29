package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodecRoundTripsEveryMessageType(t *testing.T) {
	// One representative value per type. If a new message is added to
	// factories without a case here, the completeness check below fails.
	cases := []Message{
		&Login{Version: Version, ClientName: "router", Hostname: "OpenWrt",
			OS: "linux", Arch: "amd64", Timestamp: 1700000000,
			AuthKey: "deadbeef", PoolCount: 8, RunID: "abc"},
		&LoginResp{Version: Version, RunID: "abc", ServerVersion: "0.1.0"},
		&NewProxy{Proxy: ProxySpec{Name: "nas", Kind: "http",
			Domains: []string{"*.aaa.com", "aaa.com"}, TLSMode: "terminate"}},
		&NewProxyResp{Name: "nas", RemotePort: 8080},
		&CloseProxy{Name: "nas"},
		&ReqWorkConn{Count: 4},
		&NewWorkConn{RunID: "abc", Timestamp: 1700000000, AuthKey: "cafe"},
		&StartWorkConn{ProxyName: "nas", SourceAddr: "203.0.113.7:51234"},
		&Ping{Timestamp: 1700000000},
		&Pong{Timestamp: 1700000000},
		&CertPush{Domains: []string{"*.aaa.com"},
			FullchainPEM:  []byte("-----BEGIN CERTIFICATE-----"),
			PrivateKeyPEM: []byte("-----BEGIN PRIVATE KEY-----"),
			NotAfter:      1700000000},
		&CertPushResp{},
		&HTTPChallenge{Domain: "openwrt.arm.moe", Token: "tok", KeyAuth: "tok.thumb"},
		&HTTPChallengeResp{},
		&NewMuxConn{RunID: "abc", Timestamp: 1700000000, AuthKey: "cafe"},
	}

	if len(cases) != len(factories) {
		t.Fatalf("test covers %d message types but %d are registered; "+
			"add the new message to this table", len(cases), len(factories))
	}

	var buf bytes.Buffer
	codec := NewCodec(&buf)

	for _, want := range cases {
		buf.Reset()
		if err := codec.Write(want); err != nil {
			t.Fatalf("write %s: %v", want.Type(), err)
		}
		got, err := codec.Read()
		if err != nil {
			t.Fatalf("read %s: %v", want.Type(), err)
		}
		if got.Type() != want.Type() {
			t.Errorf("type = %s, want %s", got.Type(), want.Type())
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s round trip mismatch:\n got %+v\nwant %+v", want.Type(), got, want)
		}
	}
}

func TestCodecErrorPropagatesThroughResponses(t *testing.T) {
	var buf bytes.Buffer
	codec := NewCodec(&buf)

	if err := codec.Write(&LoginResp{Error: "token rejected"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	msg, err := codec.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resp, ok := msg.(*LoginResp)
	if !ok {
		t.Fatalf("got %T, want *LoginResp", msg)
	}
	if resp.Error != "token rejected" {
		t.Errorf("Error = %q, want %q", resp.Error, "token rejected")
	}
}

func TestCodecRejectsOversizedLengthPrefix(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(byte(TypePing))
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], MaxMessageSize+1)
	buf.Write(size[:])

	_, err := NewCodec(&buf).Read()
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", err)
	}
}

// TestCodecSkipsUnknownTypeWithoutDesync covers forward compatibility: a newer
// peer must be able to send a message we predate without corrupting the
// framing of everything after it.
func TestCodecSkipsUnknownTypeWithoutDesync(t *testing.T) {
	var buf bytes.Buffer

	unknownPayload := []byte(`{"future":"field"}`)
	buf.WriteByte(0xFE)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(unknownPayload)))
	buf.Write(size[:])
	buf.Write(unknownPayload)

	codec := NewCodec(&buf)
	if err := codec.Write(&Ping{Timestamp: 42}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := codec.Read(); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("first read err = %v, want ErrUnknownType", err)
	}

	// The stream must still be aligned.
	msg, err := codec.Read()
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	ping, ok := msg.(*Ping)
	if !ok {
		t.Fatalf("got %T, want *Ping — the stream desynchronised", msg)
	}
	if ping.Timestamp != 42 {
		t.Errorf("Timestamp = %d, want 42", ping.Timestamp)
	}
}

func TestCodecReturnsBareEOFOnCleanClose(t *testing.T) {
	// An empty buffer stands in for a peer that closed at a frame boundary.
	_, err := NewCodec(&bytes.Buffer{}).Read()
	if err != io.EOF {
		t.Fatalf("err = %v, want bare io.EOF so callers can spot a clean close", err)
	}
}

func TestCodecTruncatedFrameIsNotEOF(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(byte(TypePing))
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], 128)
	buf.Write(size[:])
	buf.WriteString("only a few bytes")

	_, err := NewCodec(&buf).Read()
	if err == nil || err == io.EOF {
		t.Fatalf("err = %v, want a wrapped unexpected-EOF error", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want it to wrap io.ErrUnexpectedEOF", err)
	}
}

func TestReadExpectRejectsWrongType(t *testing.T) {
	var buf bytes.Buffer
	codec := NewCodec(&buf)
	if err := codec.Write(&Pong{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := codec.ReadExpect(TypeLogin); !errors.Is(err, ErrUnexpectedMessage) {
		t.Fatalf("err = %v, want ErrUnexpectedMessage", err)
	}
}

// TestCodecConcurrentWritesStayFramed exercises the real reason Write holds a
// mutex: the control loop writes heartbeats and responses from separate
// goroutines.
func TestCodecConcurrentWritesStayFramed(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	writer := NewCodec(client)
	reader := NewCodec(server)

	const writers, perWriter = 8, 25
	total := writers * perWriter

	received := make(chan Message, total)
	go func() {
		for range total {
			msg, err := reader.Read()
			if err != nil {
				t.Errorf("read: %v", err)
				close(received)
				return
			}
			received <- msg
		}
		close(received)
	}()

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWriter {
				var err error
				if w%2 == 0 {
					err = writer.Write(&Ping{Timestamp: int64(i)})
				} else {
					err = writer.Write(&CloseProxy{Name: strings.Repeat("x", i+1)})
				}
				if err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	count := 0
	for range received {
		count++
	}
	if count != total {
		t.Errorf("decoded %d messages, want %d — frames interleaved", count, total)
	}
}

func TestAuthKeyVerification(t *testing.T) {
	const token = "s3cret"
	now := time.Unix(1700000000, 0)
	ts := now.Unix()
	key := AuthKey(token, ts)

	if err := VerifyAuth(token, key, ts, now, DefaultAuthSkew); err != nil {
		t.Errorf("valid key rejected: %v", err)
	}

	if err := VerifyAuth(token, "wrong", ts, now, DefaultAuthSkew); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("bad key: err = %v, want ErrAuthFailed", err)
	}

	// A key that was valid an hour ago must not be replayable now.
	stale := now.Add(-time.Hour).Unix()
	staleKey := AuthKey(token, stale)
	if err := VerifyAuth(token, staleKey, stale, now, DefaultAuthSkew); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("stale timestamp: err = %v, want ErrAuthFailed", err)
	}

	// Clock drift within the window is tolerated in both directions.
	for _, drift := range []time.Duration{-5 * time.Minute, 5 * time.Minute} {
		skewed := now.Add(drift).Unix()
		if err := VerifyAuth(token, AuthKey(token, skewed), skewed, now, DefaultAuthSkew); err != nil {
			t.Errorf("drift %s rejected: %v", drift, err)
		}
	}

	// An empty token disables authentication outright.
	if err := VerifyAuth("", "anything", ts, now, DefaultAuthSkew); err != nil {
		t.Errorf("empty token should disable auth, got %v", err)
	}

	// The token must not be recoverable from the wire value.
	if strings.Contains(key, token) {
		t.Error("auth key leaks the token")
	}
}

func TestCheckVersion(t *testing.T) {
	if err := CheckVersion(Version); err != nil {
		t.Errorf("matching version rejected: %v", err)
	}
	if err := CheckVersion(Version + 1); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("err = %v, want ErrVersionMismatch", err)
	}
}

func TestNewRunIDIsUniqueAndHex(t *testing.T) {
	seen := make(map[string]struct{}, 256)
	for range 256 {
		id, err := NewRunID()
		if err != nil {
			t.Fatalf("NewRunID: %v", err)
		}
		if len(id) != 32 {
			t.Fatalf("id %q has length %d, want 32", id, len(id))
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate run id %q", id)
		}
		seen[id] = struct{}{}
	}
}
