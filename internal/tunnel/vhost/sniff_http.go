package vhost

import (
	"bytes"
	"fmt"
	"io"
)

var h2cPreface = []byte("PRI * HTTP/2.0\r\n")

type HTTPInfo struct {
	Host string

	Method string
	Path   string

	Consumed []byte
}

func SniffHTTP(r io.Reader) (HTTPInfo, error) {
	head := newHeadReader(r, MaxHTTPHead)

	headerEnd := -1
	for headerEnd < 0 {
		if err := head.readMore(); err != nil {

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

	rest := raw[:headerEnd]

	line, rest := cutCRLF(rest)
	if len(line) == 0 {
		return info, fmt.Errorf("vhost: empty request line")
	}

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

	return info, ErrNoHost
}

func cutCRLF(b []byte) (line, rest []byte) {
	if idx := bytes.Index(b, []byte("\r\n")); idx >= 0 {
		return b[:idx], b[idx+2:]
	}
	return b, nil
}

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
