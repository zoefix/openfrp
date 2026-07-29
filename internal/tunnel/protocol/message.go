package protocol

import "fmt"

const Version = 1

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

func (t Type) String() string {
	if name, ok := typeNames[t]; ok {
		return name
	}
	return fmt.Sprintf("Type(%d)", uint8(t))
}

type Message interface {
	Type() Type
}

type Login struct {
	Version int `json:"version"`

	ClientName string `json:"client_name,omitempty"`

	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`

	Timestamp int64 `json:"timestamp"`

	AuthKey string `json:"auth_key,omitempty"`

	PoolCount int `json:"pool_count,omitempty"`

	// Limits for everything this client publishes, in bytes and bytes per
	// second. Zero for no limit. A tunnel's own limit nests inside these.
	DownRate int64 `json:"down_rate,omitempty"`
	UpRate   int64 `json:"up_rate,omitempty"`
	Quota    int64 `json:"quota,omitempty"`

	RunID string `json:"run_id,omitempty"`
}

func (*Login) Type() Type { return TypeLogin }

type LoginResp struct {
	ObservedAddr string `json:"observed_addr,omitempty"`

	Version int    `json:"version"`
	RunID   string `json:"run_id,omitempty"`

	ServerVersion string `json:"server_version,omitempty"`
	Error         string `json:"error,omitempty"`
}

func (*LoginResp) Type() Type { return TypeLoginResp }

type ProxySpec struct {
	Name string `json:"name"`

	Kind string `json:"kind"`

	RemotePort int `json:"remote_port,omitempty"`

	Domains []string `json:"domains,omitempty"`

	TLSMode string `json:"tls_mode,omitempty"`

	SecretKey string `json:"secret_key,omitempty"`

	// Limits travel with the tunnel because the server is where they are
	// applied: it sees every visitor of a tunnel, while the client sees only
	// the connections that reached it.
	DownRate int64 `json:"down_rate,omitempty"`
	UpRate   int64 `json:"up_rate,omitempty"`
	Quota    int64 `json:"quota,omitempty"`
}

type NewProxy struct {
	Proxy ProxySpec `json:"proxy"`
}

func (*NewProxy) Type() Type { return TypeNewProxy }

type NewProxyResp struct {
	Name string `json:"name"`

	RemotePort int    `json:"remote_port,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (*NewProxyResp) Type() Type { return TypeNewProxyResp }

type CloseProxy struct {
	Name string `json:"name"`
}

func (*CloseProxy) Type() Type { return TypeCloseProxy }

type ReqWorkConn struct {
	Count int `json:"count,omitempty"`
}

func (*ReqWorkConn) Type() Type { return TypeReqWorkConn }

type NewWorkConn struct {
	RunID     string `json:"run_id"`
	Timestamp int64  `json:"timestamp"`
	AuthKey   string `json:"auth_key,omitempty"`
}

func (*NewWorkConn) Type() Type { return TypeNewWorkConn }

type NewMuxConn struct {
	RunID     string `json:"run_id"`
	Timestamp int64  `json:"timestamp"`
	AuthKey   string `json:"auth_key,omitempty"`
}

func (*NewMuxConn) Type() Type { return TypeNewMuxConn }

type StartWorkConn struct {
	ProxyName string `json:"proxy_name"`

	SourceAddr string `json:"source_addr,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (*StartWorkConn) Type() Type { return TypeStartWorkConn }

type Ping struct {
	Timestamp int64 `json:"timestamp"`
}

func (*Ping) Type() Type { return TypePing }

type Pong struct {
	Timestamp int64  `json:"timestamp"`
	Error     string `json:"error,omitempty"`
}

func (*Pong) Type() Type { return TypePong }

type CertPush struct {
	Domains []string `json:"domains"`

	FullchainPEM  []byte `json:"fullchain_pem"`
	PrivateKeyPEM []byte `json:"private_key_pem"`

	NotAfter int64 `json:"not_after,omitempty"`
}

func (*CertPush) Type() Type { return TypeCertPush }

type HTTPChallenge struct {
	Domain string `json:"domain"`

	Token string `json:"token"`

	KeyAuth string `json:"key_auth"`

	Remove bool `json:"remove,omitempty"`
}

func (*HTTPChallenge) Type() Type { return TypeHTTPChallenge }

type HTTPChallengeResp struct {
	Error string `json:"error,omitempty"`
}

func (*HTTPChallengeResp) Type() Type { return TypeHTTPChallengeResp }

type CertPushResp struct {
	Error string `json:"error,omitempty"`
}

func (*CertPushResp) Type() Type { return TypeCertPushResp }

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
