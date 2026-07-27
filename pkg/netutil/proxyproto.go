package netutil

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
)

// PROXY protocol versions.
//
// The header is written once, before any payload, and describes the connection
// the proxy is relaying on behalf of. Everything after it is untouched, which
// is why this works here: the relay still hands the kernel a raw socket and
// keeps the splice fast path.
const (
	ProxyProtoNone = ""
	ProxyProtoV1   = "v1"
	ProxyProtoV2   = "v2"
)

// ValidProxyProtocol reports whether version is one this can write.
func ValidProxyProtocol(version string) bool {
	switch version {
	case ProxyProtoNone, ProxyProtoV1, ProxyProtoV2:
		return true
	}
	return false
}

// proxyV2Signature is the fixed 12-byte prefix of a version 2 header. It is
// deliberately something no plausible protocol starts with, so a backend that
// is not expecting one fails immediately rather than misreading it as payload.
var proxyV2Signature = []byte{
	0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
}

// WriteProxyHeader announces the original client to the backend.
//
// source is the address of whoever connected to the public server. destination
// describes this hop — the local service being dialled — rather than the
// public address the client originally reached, which this end of the tunnel
// does not know. That is what the header's destination fields are least used
// for: nginx's real_ip, HAProxy's src and every equivalent read the source.
//
// A backend that is not configured to expect this will reject the connection,
// because the header arrives where it expects a request. That is the correct
// failure — the alternative would be logging every visitor as the router.
func WriteProxyHeader(w io.Writer, version string, source, destination net.Addr) error {
	switch version {
	case ProxyProtoNone:
		return nil
	case ProxyProtoV1:
		return writeProxyV1(w, source, destination)
	case ProxyProtoV2:
		return writeProxyV2(w, source, destination)
	default:
		return fmt.Errorf("netutil: unknown PROXY protocol version %q", version)
	}
}

// writeProxyV1 writes the text form: PROXY TCP4 src dst sport dport\r\n.
func writeProxyV1(w io.Writer, source, destination net.Addr) error {
	sourceIP, sourcePort, err := splitAddr(source)
	if err != nil {
		return err
	}
	destIP, destPort, err := splitAddr(destination)
	if err != nil {
		return err
	}

	family := "TCP4"
	sourceText, destText := sourceIP.String(), destIP.String()

	if sourceIP.To4() == nil || destIP.To4() == nil {
		// Version 1 has no way to say that one end is v4 and the other v6, so
		// both are written in the declared family. The v4 side is rendered in
		// its mapped form explicitly: net.IP.String collapses a v4-mapped
		// address back to dotted quad, which would declare TCP6 and then carry
		// something a strict parser rejects as not being an IPv6 address.
		family = "TCP6"
		sourceText, destText = renderAsV6(sourceIP), renderAsV6(destIP)
	}

	header := fmt.Sprintf("PROXY %s %s %s %d %d\r\n",
		family, sourceText, destText, sourcePort, destPort)

	_, err = io.WriteString(w, header)
	return err
}

// writeProxyV2 writes the binary form, which is what most current backends
// prefer: fixed size, unambiguous, and no parsing of decimal text.
func writeProxyV2(w io.Writer, source, destination net.Addr) error {
	sourceIP, sourcePort, err := splitAddr(source)
	if err != nil {
		return err
	}
	destIP, destPort, err := splitAddr(destination)
	if err != nil {
		return err
	}

	header := make([]byte, 0, 52)
	header = append(header, proxyV2Signature...)

	// Version 2, command PROXY: this describes a connection being relayed on
	// someone else's behalf, as opposed to LOCAL for the proxy's own health
	// checks.
	header = append(header, 0x21)

	var (
		family    byte
		addresses []byte
	)

	if v4Source, v4Dest := sourceIP.To4(), destIP.To4(); v4Source != nil && v4Dest != nil {
		family = 0x11 // AF_INET over STREAM
		addresses = append(addresses, v4Source...)
		addresses = append(addresses, v4Dest...)
	} else {
		family = 0x21 // AF_INET6 over STREAM
		addresses = append(addresses, normaliseTo6(sourceIP).To16()...)
		addresses = append(addresses, normaliseTo6(destIP).To16()...)
	}

	addresses = binary.BigEndian.AppendUint16(addresses, uint16(sourcePort))
	addresses = binary.BigEndian.AppendUint16(addresses, uint16(destPort))

	header = append(header, family)
	header = binary.BigEndian.AppendUint16(header, uint16(len(addresses)))
	header = append(header, addresses...)

	_, err = w.Write(header)
	return err
}

// splitAddr pulls an IP and port out of a net.Addr.
func splitAddr(addr net.Addr) (net.IP, int, error) {
	if addr == nil {
		return nil, 0, fmt.Errorf("netutil: no address to describe")
	}

	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP != nil {
		return tcp.IP, tcp.Port, nil
	}
	if udp, ok := addr.(*net.UDPAddr); ok && udp.IP != nil {
		return udp.IP, udp.Port, nil
	}

	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, 0, fmt.Errorf("netutil: %q is not an address with a port: %w",
			addr.String(), err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return nil, 0, fmt.Errorf("netutil: %q is not an IP address", host)
	}

	number, err := strconv.Atoi(port)
	if err != nil {
		return nil, 0, fmt.Errorf("netutil: %q is not a port: %w", port, err)
	}
	return ip, number, nil
}

// renderAsV6 writes an address in IPv6 notation, keeping a v4 address in its
// mapped form rather than letting it collapse back to dotted quad.
func renderAsV6(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return "::ffff:" + v4.String()
	}
	return ip.String()
}

// normaliseTo6 renders a v4 address as its v6-mapped form.
func normaliseTo6(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return net.IP(append([]byte{
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xFF,
		}, v4...))
	}
	return ip
}

// ParseProxyAddr turns the address string carried on the wire into a net.Addr.
func ParseProxyAddr(addr string) (net.Addr, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("netutil: %q is not an address with a port: %w", addr, err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("netutil: %q is not an IP address", host)
	}

	number, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("netutil: %q is not a port: %w", port, err)
	}
	return &net.TCPAddr{IP: ip, Port: number}, nil
}
