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

	full := buf.Bytes()
	for cut := 1; cut < len(full); cut++ {
		if _, err := ReadUDPPacket(bytes.NewReader(full[:cut])); err == nil {
			t.Errorf("truncation at %d bytes was accepted", cut)
		}
	}
}

func TestUDPReadIntoMatchesRead(t *testing.T) {
	cases := []UDPPacket{
		{Addr: "192.0.2.7:53", Payload: []byte("query")},
		{Addr: "[2001:db8::1]:9999", Payload: bytes.Repeat([]byte{0xAB}, MaxUDPPayload)},
		{Addr: "198.51.100.1:1", Payload: []byte{}},
		{Addr: "", Payload: []byte("no address")},
	}

	scratch := make([]byte, MaxUDPFrame)
	for _, want := range cases {
		var buf bytes.Buffer
		if err := WriteUDPPacket(&buf, want); err != nil {
			t.Fatalf("write %q: %v", want.Addr, err)
		}

		addr, payload, err := ReadUDPPacketInto(bytes.NewReader(buf.Bytes()), scratch)
		if err != nil {
			t.Fatalf("read into %q: %v", want.Addr, err)
		}
		if string(addr) != want.Addr {
			t.Errorf("addr = %q, want %q", addr, want.Addr)
		}
		if !bytes.Equal(payload, want.Payload) {
			t.Errorf("payload mismatch for %q: got %d bytes, want %d",
				want.Addr, len(payload), len(want.Payload))
		}
	}
}

func TestUDPReadIntoRejectsSmallScratch(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteUDPPacket(&buf, UDPPacket{Addr: "a:1", Payload: []byte("x")}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := ReadUDPPacketInto(&buf, make([]byte, 100)); err == nil {
		t.Fatal("small scratch was accepted")
	}
}

func TestUDPAppendRoundTrip(t *testing.T) {
	frame := make([]byte, 0, MaxUDPFrame)
	frame, err := AppendUDPPacket(frame, "203.0.113.9:4242", []byte("hello"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	packet, err := ReadUDPPacket(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if packet.Addr != "203.0.113.9:4242" || string(packet.Payload) != "hello" {
		t.Errorf("round trip = %q/%q", packet.Addr, packet.Payload)
	}
}

func TestUDPSteadyStateAllocations(t *testing.T) {
	payload := bytes.Repeat([]byte{0x42}, 512)
	addr := "192.0.2.55:60000"

	var wire bytes.Buffer
	if err := WriteUDPPacket(&wire, UDPPacket{Addr: addr, Payload: payload}); err != nil {
		t.Fatalf("prime: %v", err)
	}
	frameBytes := append([]byte(nil), wire.Bytes()...)

	scratch := make([]byte, MaxUDPFrame)
	reader := bytes.NewReader(frameBytes)

	readAllocs := testing.AllocsPerRun(200, func() {
		reader.Reset(frameBytes)
		if _, _, err := ReadUDPPacketInto(reader, scratch); err != nil {
			t.Fatalf("read: %v", err)
		}
	})
	if readAllocs > 0 {
		t.Errorf("ReadUDPPacketInto allocates %.1f times per packet, want 0", readAllocs)
	}

	frame := make([]byte, 0, MaxUDPFrame)
	appendAllocs := testing.AllocsPerRun(200, func() {
		var err error
		if _, err = AppendUDPPacket(frame[:0], addr, payload); err != nil {
			t.Fatalf("append: %v", err)
		}
	})
	if appendAllocs > 0 {
		t.Errorf("AppendUDPPacket allocates %.1f times per packet, want 0", appendAllocs)
	}
}
