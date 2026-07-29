package server

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/zoefix/openfrp/internal/tunnel/vhost"
)

func TestUnclaimedPageExplainsItself(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	listener := &vhostListener{scheme: vhost.SchemeHTTP}
	go func() {
		listener.unclaimed(server, "openwrt.arm.moe")
		server.Close()
	}()

	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("the response is not valid HTTP: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the name genuinely is not served", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content type = %q", got)
	}

	body := make([]byte, resp.ContentLength)
	if _, err := resp.Body.Read(body); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
	page := string(body)

	if !strings.Contains(page, "openwrt.arm.moe") {
		t.Error("the page does not name the host that was asked for")
	}
	if !strings.Contains(page, "OpenFrp") {
		t.Error("the page does not say what answered")
	}
	if !strings.Contains(page, "domains") {
		t.Error("the page does not say what to do about it")
	}
}

func TestUnclaimedPageEscapesTheHost(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	listener := &vhostListener{scheme: vhost.SchemeHTTP}
	go func() {
		listener.unclaimed(server, "<script>alert(1)</script>")
		server.Close()
	}()

	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body := make([]byte, resp.ContentLength)
	resp.Body.Read(body)

	if strings.Contains(string(body), "<script>") {
		t.Error("the Host header was echoed into the page unescaped")
	}
}

func TestUnclaimedSaysNothingOverTLS(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	listener := &vhostListener{scheme: vhost.SchemeHTTPS}
	go func() {
		listener.unclaimed(server, "example.com")
		server.Close()
	}()

	buf := make([]byte, 1)
	if n, err := client.Read(buf); err == nil && n > 0 {
		t.Errorf("wrote %d bytes of plaintext onto a TLS connection", n)
	}
}
