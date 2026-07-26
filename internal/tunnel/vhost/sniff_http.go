package vhost

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// h2cPreface is the opening of a cleartext HTTP/2 connection sent with prior
// knowledge. It is recognised only so the error can say what happened.
var h2cPreface = []byte("PRI * HTTP/2.0\r\n")

// HTTPInfo is what the request head tells us.
type HTTPInfo struct {
	// Host is the normalised value of the Host header, without any port.
	Host string
	// Method and Path are carried for logging.
	Method string
	Path   string
	// Consumed holds every byte read while sniffing. The caller must write
	// these to the upstream connection before relaying, or the request will
	// arrive truncated.
	Consumed []byte
}

// SniffHTTP reads just enough of an HTTP/1.x request to recover its Host.
//
// It stops at the end of the header block and returns the bytes it consumed so
// the caller can replay them and then splice the raw sockets. Nothing is
// rewritten: the request reaches the local service byte for byte.
func SniffHTTP(r io.Reader) (HTTPInfo, error) {
	head := newHeadReader(r, MaxHTTPHead)

	headerEnd := -1
	for headerEnd < 0 {
		if err := head.readMore(); err != nil {
			// A peer may legitimately close after sending a complete head, so
			// check what we have before giving up.
			if idx := bytes.Index(head.bytes(), []byte("\r\n\r\n")); idx >= 0 {
				headerEnd = idx + 4
				break
			}
			return HTTPInfo{Consumed: head.bytes()},
				fmt.Errorf("vhost: reading request head: %w", err)
		}
		if idx := bytes.Index(head.bytes(), []byte("\r\n\r\n")); idx >= 0 {
			headerEnd = idx + 4
		}
	}

	raw := head.bytes()
	info := HTTPInfo{Consumed: raw}

	if bytes.HasPrefix(raw, h2cPreface) {
		return info, fmt.Errorf("vhost: cleartext HTTP/2 with prior knowledge " +
			"carries no Host header and cannot be routed by this listener")
	}

	lines := strings.Split(string(raw[:headerEnd]), "\r\n")
	if len(lines) == 0 || lines[0] == "" {
		return info, fmt.Errorf("vhost: empty request line")
	}

	// Request line: METHOD SP PATH SP VERSION
	if parts := strings.Fields(lines[0]); len(parts) >= 2 {
		info.Method = parts[0]
		info.Path = parts[1]
	} else {
		return info, fmt.Errorf("vhost: malformed request line %q", lines[0])
	}

	for _, line := range lines[1:] {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(name), "host") {
			continue
		}

		host, err := NormaliseHost(strings.TrimSpace(value))
		if err != nil {
			return info, fmt.Errorf("vhost: %w", err)
		}
		info.Host = host
		return info, nil
	}

	// HTTP/1.1 requires Host; HTTP/1.0 does not, and we cannot route without it.
	return info, ErrNoHost
}
