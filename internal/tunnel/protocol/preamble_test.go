package protocol

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPreambleRoundTrip(t *testing.T) {
	for _, mode := range []Mode{ModePlain, ModeMux} {
		var buf bytes.Buffer
		if err := WritePreamble(&buf, Preamble{Version: Version, Mode: mode}); err != nil {
			t.Fatalf("write %s: %v", mode, err)
		}
		if buf.Len() != preambleSize {
			t.Errorf("%s preamble is %d bytes, want %d", mode, buf.Len(), preambleSize)
		}

		got, err := ReadPreamble(&buf)
		if err != nil {
			t.Fatalf("read %s: %v", mode, err)
		}
		if got.Mode != mode {
			t.Errorf("Mode = %s, want %s", got.Mode, mode)
		}
		if got.Version != Version {
			t.Errorf("Version = %d, want %d", got.Version, Version)
		}
	}
}

func TestPreambleRejectsNonProtocolTraffic(t *testing.T) {
	cases := map[string]string{
		"http request": "GET / HTTP/1.1\r\nHost: x\r\n\r\n",
		"tls hello":    "\x16\x03\x01\x02\x00\x01\x00",
		"empty-ish":    "\x00\x00\x00\x00\x00\x00",
		"ssh banner":   "SSH-2.0-OpenSSH_9.2",
	}

	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ReadPreamble(strings.NewReader(payload))
			if err == nil {
				t.Fatal("expected rejection")
			}
			if !strings.Contains(err.Error(), "magic") {
				t.Errorf("err = %v, want a magic-mismatch error", err)
			}
		})
	}
}

func TestPreambleRejectsVersionMismatch(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePreamble(&buf, Preamble{Version: Version + 9, Mode: ModePlain}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadPreamble(&buf); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("err = %v, want ErrVersionMismatch", err)
	}
}

func TestPreambleRejectsUnknownMode(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePreamble(&buf, Preamble{Version: Version, Mode: Mode(0x7F)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadPreamble(&buf); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("err = %v, want an unknown-mode error", err)
	}
}

func TestPreambleTruncated(t *testing.T) {
	_, err := ReadPreamble(strings.NewReader(string(Magic[:])))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want it to wrap io.ErrUnexpectedEOF", err)
	}
}

func TestPreambleThenCodecShareOneStream(t *testing.T) {
	var buf bytes.Buffer

	if err := WritePreamble(&buf, Preamble{Version: Version, Mode: ModePlain}); err != nil {
		t.Fatalf("write preamble: %v", err)
	}
	if err := NewCodec(&buf).Write(&Login{Version: Version, ClientName: "router"}); err != nil {
		t.Fatalf("write login: %v", err)
	}

	if _, err := ReadPreamble(&buf); err != nil {
		t.Fatalf("read preamble: %v", err)
	}
	msg, err := NewCodec(&buf).ReadExpect(TypeLogin)
	if err != nil {
		t.Fatalf("read login: %v", err)
	}
	if login := msg.(*Login); login.ClientName != "router" {
		t.Errorf("ClientName = %q, want %q", login.ClientName, "router")
	}
}
