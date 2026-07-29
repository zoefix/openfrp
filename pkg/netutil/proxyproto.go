package netutil

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
)

const (
	ProxyProtoNone = ""
	ProxyProtoV1   = "v1"
	ProxyProtoV2   = "v2"
)

func ValidProxyProtocol(version string) bool {
	switch version {
	case ProxyProtoNone, ProxyProtoV1, ProxyProtoV2:
		return true
	}
	return false
}

var proxyV2Signature = []byte{
	0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
}

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

		family = "TCP6"
		sourceText, destText = renderAsV6(sourceIP), renderAsV6(destIP)
	}

	header := fmt.Sprintf("PROXY %s %s %s %d %d\r\n",
		family, sourceText, destText, sourcePort, destPort)

	_, err = io.WriteString(w, header)
	return err
}

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

	header = append(header, 0x21)

	var (
		family    byte
		addresses []byte
	)

	if v4Source, v4Dest := sourceIP.To4(), destIP.To4(); v4Source != nil && v4Dest != nil {
		family = 0x11
		addresses = append(addresses, v4Source...)
		addresses = append(addresses, v4Dest...)
	} else {
		family = 0x21
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

func renderAsV6(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return "::ffff:" + v4.String()
	}
	return ip.String()
}

func normaliseTo6(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return net.IP(append([]byte{
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xFF,
		}, v4...))
	}
	return ip
}

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
