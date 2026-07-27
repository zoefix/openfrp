package protocol

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestUDPPacketRoundTrip(t *testing.T) {
	cases := []UDPPacket{
		{Addr: "203.0.113.7:51234", Payload: []byte("hello")},
		{Addr: "[2001:db8::1%eth0]:53", Payload: []byte{0, 1, 2, 255}},
		// A zero-length datagram is legal UDP and must survive as a
		// zero-length datagram rather than as nothing.
		{Addr: "198.51.100.1:9", Payload: []byte{}},
		{Addr: "", Payload: []byte("no source")},
		{Addr: "10.0.0.1:1", Payload: bytes.Repeat([]byte("x"), MaxUDPPayload)},
	}

	var buf bytes.Buffer
	for _, want := range cases {
		buf.Reset()
		if err := WriteUDPPacket(&buf, want); err != nil {
			t.Fatalf("write %q: %v", want.Addr, err)
		}
		got, err := ReadUDPPacket(&buf)
		if err != nil {
			t.Fatalf("read %q: %v", want.Addr, err)
		}
		if got.Addr != want.Addr {
			t.Errorf("Addr = %q, want %q", got.Addr, want.Addr)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("payload mismatch for %q: %d vs %d bytes",
				want.Addr, len(got.Payload), len(want.Payload))
		}
	}
}

// TestUDPBoundariesArePreserved is the property the framing exists for. A UDP
// application that receives two datagrams merged, or one split in half, breaks
// in ways that are very hard to diagnose from the far end.
func TestUDPBoundariesArePreserved(t *testing.T) {
	var buf bytes.Buffer

	sent := []UDPPacket{
		{Addr: "a:1", Payload: []byte("first")},
		{Addr: "b:2", Payload: []byte("second-is-longer")},
		{Addr: "c:3", Payload: []byte{}},
		{Addr: "d:4", Payload: []byte("fourth")},
	}
	for _, packet := range sent {
		if err := WriteUDPPacket(&buf, packet); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	for i, want := range sent {
		got, err := ReadUDPPacket(&buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if got.Addr != want.Addr || !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("datagram %d came back as %q/%q, want %q/%q",
				i, got.Addr, got.Payload, want.Addr, want.Payload)
		}
	}

	if _, err := ReadUDPPacket(&buf); err != io.EOF {
		t.Errorf("expected clean EOF after the last datagram, got %v", err)
	}
}

func TestUDPRejectsOversizedPayload(t *testing.T) {
	var buf bytes.Buffer
	err := WriteUDPPacket(&buf, UDPPacket{
		Addr:    "a:1",
		Payload: bytes.Repeat([]byte("x"), MaxUDPPayload+1),
	})
	if err == nil {
		t.Fatal("a payload above the IPv4 UDP maximum should be refused")
	}
}

func TestUDPRejectsOversizedAddress(t *testing.T) {
	var buf bytes.Buffer
	err := WriteUDPPacket(&buf, UDPPacket{
		Addr:    strings.Repeat("a", 300),
		Payload: []byte("x"),
	})
	if err == nil {
		t.Fatal("an address longer than the length field allows should be refused")
	}
}

func TestUDPTruncatedFrameIsAnError(t *testing.T) {
	var buf bytes.Buffer
	WriteUDPPacket(&buf, UDPPacket{Addr: "a:1", Payload: []byte("payload")})

	// Every truncation must error rather than panic; the input is attacker
	// controlled.
	full := buf.Bytes()
	for cut := 1; cut < len(full); cut++ {
		if _, err := ReadUDPPacket(bytes.NewReader(full[:cut])); err == nil {
			t.Errorf("truncation at %d bytes was accepted", cut)
		}
	}
}
