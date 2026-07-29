// Package protocol defines the OpenFrp control-plane wire format.
//
// Both daemons link this package, so it must stay free of server- or
// client-specific imports. Control messages are infrequent and small, so the
// payload encoding is JSON: the debuggability is worth far more than the bytes
// it costs. Bulk payload never passes through here — it moves over work
// connections, ideally without entering userspace at all.
package protocol

import "fmt"

// Version is the wire protocol version. Bump it only for incompatible changes;
// additive fields do not require a bump because unknown JSON fields are
// ignored on the receiving side.
const Version = 1

// Type identifies a control message.
type Type uint8

const (
	TypeLogin Type = iota + 1
	TypeLoginResp
	TypeNewProxy
	TypeNewProxyResp
	TypeCloseProxy
	TypeReqWorkConn
	TypeNewWorkConn
	TypeStartWorkConn
	TypePing
	TypePong
	TypeCertPush
	TypeCertPushResp
	TypeHTTPChallenge
	TypeHTTPChallengeResp
	TypeNewMuxConn
)

var typeNames = map[Type]string{
	TypeLogin:             "Login",
	TypeLoginResp:         "LoginResp",
	TypeNewProxy:          "NewProxy",
	TypeNewProxyResp:      "NewProxyResp",
	TypeCloseProxy:        "CloseProxy",
	TypeReqWorkConn:       "ReqWorkConn",
	TypeNewWorkConn:       "NewWorkConn",
	TypeStartWorkConn:     "StartWorkConn",
	TypePing:              "Ping",
	TypePong:              "Pong",
	TypeCertPush:          "CertPush",
	TypeCertPushResp:      "CertPushResp",
	TypeHTTPChallenge:     "HTTPChallenge",
	TypeHTTPChallengeResp: "HTTPChallengeResp",
	TypeNewMuxConn:        "NewMuxConn",
}

// String implements fmt.Stringer.
func (t Type) String() string {
	if name, ok := typeNames[t]; ok {
		return name
	}
	return fmt.Sprintf("Type(%d)", uint8(t))
}

// Message is a control-plane message. Every implementation is a pointer type
// so the codec can decode into it.
type Message interface {
	Type() Type
}

// Login opens a control connection. It is always the first message a client
// sends.
type Login struct {
	// Version is the protocol version the client speaks.
	Version int `json:"version"`
	// ClientName is a stable identifier chosen by the operator. Empty asks the
	// server to assign one.
	ClientName string `json:"client_name,omitempty"`
	// Hostname, OS and Arch are diagnostics shown in the server status panel.
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
	// Timestamp is the client's Unix time, used to bound replay of AuthKey.
	Timestamp int64 `json:"timestamp"`
	// AuthKey is the hex HMAC over Timestamp keyed by the shared token.
	AuthKey string `json:"auth_key,omitempty"`
	// PoolCount is how many work connections the client intends to keep warm.
	PoolCount int `json:"pool_count,omitempty"`
	// RunID lets a reconnecting client reclaim its previous identity instead
	// of appearing as a second client.
	RunID string `json:"run_id,omitempty"`
}

func (*Login) Type() Type { return TypeLogin }

// LoginResp answers a Login.
type LoginResp struct {
	// ObservedAddr is the address this login arrived from, as the server saw
	// it. The client cannot learn this any other way, and it is the only
	// honest answer to "is my traffic going through the proxy on my own
	// router" — a redirected connection reaches the server from the proxy's
	// exit rather than from the router's own WAN address, while from the
	// client's own socket the two are indistinguishable.
	ObservedAddr string `json:"observed_addr,omitempty"`

	Version int    `json:"version"`
	RunID   string `json:"run_id,omitempty"`
	// ServerVersion is the human-readable build of the server.
	ServerVersion string `json:"server_version,omitempty"`
	Error         string `json:"error,omitempty"`
}

func (*LoginResp) Type() Type { return TypeLoginResp }

// ProxySpec describes one tunnel the client wants published. It mirrors
// config.Tunnel but is a separate type on purpose: the wire format must be
// able to evolve independently of the on-disk config schema.
type ProxySpec struct {
	Name string `json:"name"`
	// Kind is tcp, udp, http, https or stcp.
	Kind string `json:"kind"`
	// RemotePort is requested by tcp and udp proxies. Zero asks the server to
	// allocate one.
	RemotePort int `json:"remote_port,omitempty"`
	// Domains carries the routing patterns for http and https proxies.
	Domains []string `json:"domains,omitempty"`
	// TLSMode is none, passthrough or terminate.
	TLSMode string `json:"tls_mode,omitempty"`
	// SecretKey authenticates visitors to an stcp proxy.
	SecretKey string `json:"secret_key,omitempty"`
}

// NewProxy asks the server to publish a proxy.
type NewProxy struct {
	Proxy ProxySpec `json:"proxy"`
}

func (*NewProxy) Type() Type { return TypeNewProxy }

// NewProxyResp reports whether the proxy was published.
type NewProxyResp struct {
	Name string `json:"name"`
	// RemotePort echoes the port actually bound, which matters when the client
	// asked the server to choose.
	RemotePort int    `json:"remote_port,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (*NewProxyResp) Type() Type { return TypeNewProxyResp }

// CloseProxy withdraws a previously published proxy.
type CloseProxy struct {
	Name string `json:"name"`
}

func (*CloseProxy) Type() Type { return TypeCloseProxy }

// ReqWorkConn asks the client to open another work connection. The server
// sends this when the warm pool runs dry.
type ReqWorkConn struct {
	// Count is how many connections to open. Batching the request avoids a
	// round trip per connection during a burst.
	Count int `json:"count,omitempty"`
}

func (*ReqWorkConn) Type() Type { return TypeReqWorkConn }

// NewWorkConn is the first message on a freshly dialled work connection. It
// tells the server which client the connection belongs to.
type NewWorkConn struct {
	RunID     string `json:"run_id"`
	Timestamp int64  `json:"timestamp"`
	AuthKey   string `json:"auth_key,omitempty"`
}

func (*NewWorkConn) Type() Type { return TypeNewWorkConn }

// NewMuxConn offers one connection as a multiplexed overflow carrier.
//
// It is the same handshake as NewWorkConn and means something different: the
// connection does not become one visitor's payload, it stays open and the
// server opens a stream on it whenever the warm pool is empty. That turns the
// worst case — a visitor arriving with nothing warm to serve them — from a
// wait of roughly two round trips into a stream on a connection that already
// exists.
//
// The direction is the reason this needs its own message. On a work
// connection the client is the one that dialled and the server replies; here
// the server must be able to initiate, so it takes the multiplexer's client
// role on a connection the client dialled, and the client accepts.
type NewMuxConn struct {
	RunID     string `json:"run_id"`
	Timestamp int64  `json:"timestamp"`
	AuthKey   string `json:"auth_key,omitempty"`
}

func (*NewMuxConn) Type() Type { return TypeNewMuxConn }

// StartWorkConn tells the client which proxy a work connection has been
// assigned to. After this message the connection carries raw payload only.
type StartWorkConn struct {
	ProxyName string `json:"proxy_name"`
	// SourceAddr is the remote user's address, for logging and PROXY-protocol
	// style forwarding later.
	SourceAddr string `json:"source_addr,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (*StartWorkConn) Type() Type { return TypeStartWorkConn }

// Ping is the client's liveness probe.
type Ping struct {
	Timestamp int64 `json:"timestamp"`
}

func (*Ping) Type() Type { return TypePing }

// Pong answers a Ping.
type Pong struct {
	Timestamp int64  `json:"timestamp"`
	Error     string `json:"error,omitempty"`
}

func (*Pong) Type() Type { return TypePong }

// CertPush hands the server a certificate for edge TLS termination.
//
// This is the mechanism that lets us rotate certificates without dropping a
// single connection, which frp cannot do: frps loads its certificates once at
// startup, so rotation there means a restart and a full client disconnect.
type CertPush struct {
	// Domains are the names this certificate covers.
	Domains []string `json:"domains"`
	// FullchainPEM and PrivateKeyPEM carry the material itself.
	FullchainPEM  []byte `json:"fullchain_pem"`
	PrivateKeyPEM []byte `json:"private_key_pem"`
	// NotAfter lets the server report expiry without parsing the chain.
	NotAfter int64 `json:"not_after,omitempty"`
}

func (*CertPush) Type() Type { return TypeCertPush }

// HTTPChallenge asks the server to answer one ACME HTTP-01 challenge.
//
// The certificate authority fetches http://<domain>/.well-known/acme-challenge/
// <token> and expects the key authorisation back. That request arrives at the
// server's shared HTTP port, which is exactly where this project already
// terminates port 80 — so the server can answer it directly, without the
// challenge ever reaching the LAN service or needing one to exist.
//
// This is what makes HTTP validation usable here at all: the router has no
// public address of its own, and the name already points at the server.
type HTTPChallenge struct {
	// Domain is the name being validated, for logging and scoping.
	Domain string `json:"domain"`
	// Token is the last path element the authority will request.
	Token string `json:"token"`
	// KeyAuth is the exact body to return.
	KeyAuth string `json:"key_auth"`
	// Remove withdraws a previously published challenge instead of adding one.
	Remove bool `json:"remove,omitempty"`
}

func (*HTTPChallenge) Type() Type { return TypeHTTPChallenge }

// HTTPChallengeResp acknowledges an HTTPChallenge.
type HTTPChallengeResp struct {
	Error string `json:"error,omitempty"`
}

func (*HTTPChallengeResp) Type() Type { return TypeHTTPChallengeResp }

// CertPushResp acknowledges a CertPush.
type CertPushResp struct {
	Error string `json:"error,omitempty"`
}

func (*CertPushResp) Type() Type { return TypeCertPushResp }

// factories maps a wire type onto a constructor for its payload. Decoding is
// table driven so adding a message means adding one entry, not editing a
// switch that lives somewhere else.
var factories = map[Type]func() Message{
	TypeLogin:             func() Message { return new(Login) },
	TypeLoginResp:         func() Message { return new(LoginResp) },
	TypeNewProxy:          func() Message { return new(NewProxy) },
	TypeNewProxyResp:      func() Message { return new(NewProxyResp) },
	TypeCloseProxy:        func() Message { return new(CloseProxy) },
	TypeReqWorkConn:       func() Message { return new(ReqWorkConn) },
	TypeNewWorkConn:       func() Message { return new(NewWorkConn) },
	TypeStartWorkConn:     func() Message { return new(StartWorkConn) },
	TypePing:              func() Message { return new(Ping) },
	TypePong:              func() Message { return new(Pong) },
	TypeCertPush:          func() Message { return new(CertPush) },
	TypeCertPushResp:      func() Message { return new(CertPushResp) },
	TypeHTTPChallenge:     func() Message { return new(HTTPChallenge) },
	TypeHTTPChallengeResp: func() Message { return new(HTTPChallengeResp) },
	TypeNewMuxConn:        func() Message { return new(NewMuxConn) },
}
