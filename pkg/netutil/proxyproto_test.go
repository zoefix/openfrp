package netutil

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

func tcpAddr(t *testing.T, addr string) net.Addr {
	t.Helper()

	parsed, err := ParseProxyAddr(addr)
	if err != nil {
		t.Fatalf("ParseProxyAddr(%q): %v", addr, err)
	}
	return parsed
}

// TestProxyV1Format checks the exact bytes against the specification's
// grammar. The backend parses this by hand, so a stray space or a missing CRLF
// is a rejected connection rather than a tolerated quirk.
func TestProxyV1Format(t *testing.T) {
	cases := []struct {
		name   string
		source string
		dest   string
		want   string
	}{
		{
			name:   "IPv4",
			source: "203.0.113.9:54321",
			dest:   "192.168.9.249:80",
			want:   "PROXY TCP4 203.0.113.9 192.168.9.249 54321 80\r\n",
		},
		{
			name:   "IPv6",
			source: "[2001:db8::1]:443",
			dest:   "[2001:db8::2]:80",
			want:   "PROXY TCP6 2001:db8::1 2001:db8::2 443 80\r\n",
		},
		{
			// Version 1 cannot say that one end is v4 and the other v6, so the
			// v4 side is rendered mapped rather than emitted in a family that
			// contradicts the one declared.
			name:   "mixed families are rendered as TCP6",
			source: "[2001:db8::1]:443",
			dest:   "192.168.9.249:80",
			want:   "PROXY TCP6 2001:db8::1 ::ffff:192.168.9.249 443 80\r\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WriteProxyHeader(&buf, ProxyProtoV1,
				tcpAddr(t, tc.source), tcpAddr(t, tc.dest))
			if err != nil {
				t.Fatalf("WriteProxyHeader: %v", err)
			}

			if got := buf.String(); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestProxyV1StaysWithinTheLineLimit guards the specification's 107-byte cap,
// which a parser is entitled to enforce by disconnecting.
func TestProxyV1StaysWithinTheLineLimit(t *testing.T) {
	var buf bytes.Buffer
	err := WriteProxyHeader(&buf, ProxyProtoV1,
		tcpAddr(t, "[2001:0db8:85a3:0000:0000:8a2e:0370:7334]:65535"),
		tcpAddr(t, "[2001:0db8:85a3:0000:0000:8a2e:0370:7335]:65535"))
	if err != nil {
		t.Fatal(err)
	}

	if buf.Len() > 107 {
		t.Errorf("header is %d bytes, over the 107 byte limit: %q", buf.Len(), buf.String())
	}
	if !strings.HasSuffix(buf.String(), "\r\n") {
		t.Error("header does not end with CRLF")
	}
}

// TestProxyV2Format decodes the binary header field by field rather than
// comparing it to a blob this same code produced.
func TestProxyV2Format(t *testing.T) {
	var buf bytes.Buffer
	err := WriteProxyHeader(&buf, ProxyProtoV2,
		tcpAddr(t, "203.0.113.9:54321"), tcpAddr(t, "192.168.9.249:80"))
	if err != nil {
		t.Fatal(err)
	}

	raw := buf.Bytes()
	if len(raw) != 28 {
		t.Fatalf("IPv4 header is %d bytes, want 28", len(raw))
	}

	if !bytes.Equal(raw[:12], proxyV2Signature) {
		t.Errorf("signature = %x", raw[:12])
	}
	if raw[12] != 0x21 {
		t.Errorf("version/command = %#x, want 0x21 (version 2, PROXY)", raw[12])
	}
	if raw[13] != 0x11 {
		t.Errorf("family = %#x, want 0x11 (AF_INET, STREAM)", raw[13])
	}
	if length := binary.BigEndian.Uint16(raw[14:16]); length != 12 {
		t.Errorf("address block length = %d, want 12", length)
	}

	if source := net.IP(raw[16:20]).String(); source != "203.0.113.9" {
		t.Errorf("source = %s", source)
	}
	if dest := net.IP(raw[20:24]).String(); dest != "192.168.9.249" {
		t.Errorf("destination = %s", dest)
	}
	if port := binary.BigEndian.Uint16(raw[24:26]); port != 54321 {
		t.Errorf("source port = %d", port)
	}
	if port := binary.BigEndian.Uint16(raw[26:28]); port != 80 {
		t.Errorf("destination port = %d", port)
	}
}

func TestProxyV2IPv6Length(t *testing.T) {
	var buf bytes.Buffer
	err := WriteProxyHeader(&buf, ProxyProtoV2,
		tcpAddr(t, "[2001:db8::1]:443"), tcpAddr(t, "[2001:db8::2]:80"))
	if err != nil {
		t.Fatal(err)
	}

	raw := buf.Bytes()
	if len(raw) != 52 {
		t.Fatalf("IPv6 header is %d bytes, want 52", len(raw))
	}
	if raw[13] != 0x21 {
		t.Errorf("family = %#x, want 0x21 (AF_INET6, STREAM)", raw[13])
	}
	if length := binary.BigEndian.Uint16(raw[14:16]); length != 36 {
		t.Errorf("address block length = %d, want 36", length)
	}
}

// TestProxyProtocolNoneWritesNothing matters because the header is written
// straight onto the connection: a stray byte when the feature is off would be
// read as the first byte of the request.
func TestProxyProtocolNoneWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	err := WriteProxyHeader(&buf, ProxyProtoNone,
		tcpAddr(t, "203.0.113.9:1"), tcpAddr(t, "192.168.9.249:80"))
	if err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes with the feature disabled: %q", buf.Len(), buf.String())
	}
}

func TestProxyProtocolRejectsUnknownVersion(t *testing.T) {
	var buf bytes.Buffer
	err := WriteProxyHeader(&buf, "v3",
		tcpAddr(t, "203.0.113.9:1"), tcpAddr(t, "192.168.9.249:80"))
	if err == nil {
		t.Error("an unknown version was accepted")
	}
	if buf.Len() != 0 {
		t.Error("bytes were written for an unknown version")
	}
}

func TestValidProxyProtocol(t *testing.T) {
	for _, good := range []string{"", "v1", "v2"} {
		if !ValidProxyProtocol(good) {
			t.Errorf("ValidProxyProtocol(%q) = false", good)
		}
	}
	for _, bad := range []string{"v0", "v3", "V1", "1", "proxy"} {
		if ValidProxyProtocol(bad) {
			t.Errorf("ValidProxyProtocol(%q) = true", bad)
		}
	}
}
