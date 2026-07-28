package vhost

import (
	"bytes"
	"fmt"
	"io"
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

	// The head is parsed in place. The obvious alternative — one big
	// string(raw) plus a Split — copies the entire head and allocates a slice
	// per request, on a path that runs for every connection the vhost ports
	// accept. Only the three values that outlive the buffer (method, path,
	// host) are copied out.
	rest := raw[:headerEnd]

	line, rest := cutCRLF(rest)
	if len(line) == 0 {
		return info, fmt.Errorf("vhost: empty request line")
	}

	// Request line: METHOD SP PATH SP VERSION
	method, afterMethod, ok := bytes.Cut(line, []byte(" "))
	path, _, ok2 := bytes.Cut(afterMethod, []byte(" "))
	if !ok || !ok2 || len(method) == 0 || len(path) == 0 {
		return info, fmt.Errorf("vhost: malformed request line %q", line)
	}
	info.Method = string(method)
	info.Path = string(path)

	for len(rest) > 0 {
		line, rest = cutCRLF(rest)
		name, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		if !asciiEqualFold(bytes.TrimSpace(name), "host") {
			continue
		}

		host, err := NormaliseHost(string(bytes.TrimSpace(value)))
		if err != nil {
			return info, fmt.Errorf("vhost: %w", err)
		}
		info.Host = host
		return info, nil
	}

	// HTTP/1.1 requires Host; HTTP/1.0 does not, and we cannot route without it.
	return info, ErrNoHost
}

// cutCRLF splits off the first \r\n-terminated line.
func cutCRLF(b []byte) (line, rest []byte) {
	if idx := bytes.Index(b, []byte("\r\n")); idx >= 0 {
		return b[:idx], b[idx+2:]
	}
	return b, nil
}

// asciiEqualFold reports whether b equals the lower-case ASCII string want,
// ignoring case, without allocating.
func asciiEqualFold(b []byte, want string) bool {
	if len(b) != len(want) {
		return false
	}
	for i := range b {
		c := b[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != want[i] {
			return false
		}
	}
	return true
}
