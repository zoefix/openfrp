// Package detect inspects a target server before anything is installed on it.
//
// Every field here corresponds to a decision the installer has to make, and
// each was chosen because a real test server made it necessary: one had ports
// 80 and 443 already taken by an unrelated service, and both had the tcp_bbr
// module present but not loaded, which makes setting the sysctl fail silently.
package detect

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Runner is the subset of an SSH session this package needs. Declared here, on
// the consuming side, so detect does not import the deploy package and create
// a cycle.
type Runner interface {
	Output(ctx context.Context, command string) string
	HasCommand(ctx context.Context, name string) bool
	Exists(ctx context.Context, path string) bool
}

// InitSystem identifies how services are managed on the target.
type InitSystem string

const (
	InitSystemd  InitSystem = "systemd"
	InitOpenRC   InitSystem = "openrc"
	InitSysVInit InitSystem = "sysvinit"
	InitUnknown  InitSystem = "unknown"
)

// Firewall identifies the packet filter in use.
type Firewall string

const (
	FirewallFirewalld Firewall = "firewalld"
	FirewallUFW       Firewall = "ufw"
	FirewallNftables  Firewall = "nftables"
	FirewallIptables  Firewall = "iptables"
	FirewallNone      Firewall = "none"
)

// Result is everything learned about the target.
type Result struct {
	// Arch is the Go architecture name, so it can select a release artefact.
	Arch string
	// RawArch is what uname reported, kept for diagnostics.
	RawArch string

	Distro  string
	Version string

	Init     InitSystem
	Firewall Firewall
	// FirewallActive distinguishes "installed" from "running with rules".
	FirewallActive bool

	// BBRLoaded reports whether BBR is already usable.
	BBRLoaded bool
	// BBRAvailable reports whether the module exists and could be loaded.
	BBRAvailable bool

	// OccupiedPorts lists listening TCP ports, so the installer can refuse a
	// conflict rather than fight for a port and fail obscurely at startup.
	OccupiedPorts map[int]string

	// HasSystemdResolved and similar quirks can be added here as they arise.
	Root bool
}

// Detect probes the server.
func Detect(ctx context.Context, r Runner) (*Result, error) {
	res := &Result{OccupiedPorts: map[int]string{}}

	res.RawArch = r.Output(ctx, "uname -m")
	arch, err := goArch(res.RawArch)
	if err != nil {
		return nil, err
	}
	res.Arch = arch

	res.Distro, res.Version = distro(ctx, r)
	res.Init = initSystem(ctx, r)
	res.Firewall, res.FirewallActive = firewall(ctx, r)
	res.BBRLoaded, res.BBRAvailable = bbr(ctx, r)
	res.OccupiedPorts = listeningPorts(ctx, r)
	res.Root = r.Output(ctx, "id -u") == "0"

	return res, nil
}

// goArch maps uname output onto a Go architecture name.
func goArch(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	case "armv7l", "armv7", "armhf":
		return "arm", nil
	case "i386", "i686":
		return "386", nil
	case "mips":
		return "mips", nil
	case "mipsel":
		return "mipsle", nil
	case "riscv64":
		return "riscv64", nil
	case "loongarch64":
		return "loong64", nil
	case "":
		return "", fmt.Errorf("detect: could not determine the server's architecture")
	default:
		return "", fmt.Errorf("detect: unsupported architecture %q", raw)
	}
}

func distro(ctx context.Context, r Runner) (name, version string) {
	out := r.Output(ctx, ". /etc/os-release 2>/dev/null && echo \"$ID|$VERSION_ID|$PRETTY_NAME\"")
	parts := strings.Split(out, "|")
	if len(parts) >= 3 && parts[2] != "" {
		return parts[2], parts[1]
	}
	if len(parts) >= 1 && parts[0] != "" {
		return parts[0], ""
	}
	return "unknown", ""
}

func initSystem(ctx context.Context, r Runner) InitSystem {
	// /run/systemd/system exists only when systemd is the running init, which
	// is a stronger signal than systemctl merely being installed.
	if r.Exists(ctx, "/run/systemd/system") {
		return InitSystemd
	}
	if r.HasCommand(ctx, "openrc") || r.Exists(ctx, "/run/openrc/softlevel") {
		return InitOpenRC
	}
	if r.Exists(ctx, "/etc/init.d") {
		return InitSysVInit
	}
	return InitUnknown
}

func firewall(ctx context.Context, r Runner) (Firewall, bool) {
	if r.HasCommand(ctx, "firewall-cmd") {
		active := r.Output(ctx, "firewall-cmd --state 2>/dev/null") == "running"
		return FirewallFirewalld, active
	}
	if r.HasCommand(ctx, "ufw") {
		out := r.Output(ctx, "ufw status 2>/dev/null | head -1")
		return FirewallUFW, strings.Contains(out, "active")
	}
	if r.HasCommand(ctx, "nft") {
		// An installed nft with an empty ruleset is the common case on a fresh
		// VPS, and it means nothing needs opening.
		rules := r.Output(ctx, "nft list ruleset 2>/dev/null | wc -l")
		count, _ := strconv.Atoi(strings.TrimSpace(rules))
		return FirewallNftables, count > 0
	}
	if r.HasCommand(ctx, "iptables") {
		out := r.Output(ctx, "iptables -S 2>/dev/null | grep -c '^-A' || true")
		count, _ := strconv.Atoi(strings.TrimSpace(out))
		return FirewallIptables, count > 0
	}
	return FirewallNone, false
}

// bbr reports whether BBR is usable now, and whether it could be.
//
// The distinction matters: on both test servers tcp_bbr shipped with the
// kernel but was not loaded, so it was absent from
// tcp_available_congestion_control and setting the sysctl would have failed
// silently. The module has to be loaded first.
func bbr(ctx context.Context, r Runner) (loaded, available bool) {
	avail := r.Output(ctx, "sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null")
	loaded = strings.Contains(avail, "bbr")
	if loaded {
		return true, true
	}

	modulePath := r.Output(ctx, "modinfo -F filename tcp_bbr 2>/dev/null")
	return false, modulePath != "" && !strings.Contains(modulePath, "ERROR")
}

// listeningPorts maps a TCP port onto the process holding it.
func listeningPorts(ctx context.Context, r Runner) map[int]string {
	ports := map[int]string{}

	out := r.Output(ctx, "ss -lntp 2>/dev/null || netstat -lntp 2>/dev/null")
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// The local address column differs between ss and netstat; find the
		// first field that parses as addr:port.
		for _, field := range fields {
			idx := strings.LastIndexByte(field, ':')
			if idx < 0 {
				continue
			}
			port, err := strconv.Atoi(field[idx+1:])
			if err != nil || port < 1 || port > 65535 {
				continue
			}
			ports[port] = processName(line)
			break
		}
	}
	return ports
}

// processName pulls a program name out of an ss or netstat line.
func processName(line string) string {
	if idx := strings.Index(line, `users:(("`); idx >= 0 {
		rest := line[idx+len(`users:(("`):]
		if end := strings.IndexByte(rest, '"'); end > 0 {
			return rest[:end]
		}
	}
	// netstat renders this as pid/name.
	fields := strings.Fields(line)
	if len(fields) > 0 {
		last := fields[len(fields)-1]
		if idx := strings.IndexByte(last, '/'); idx >= 0 {
			return last[idx+1:]
		}
	}
	return "unknown"
}

// Summary renders the findings for a progress event.
func (r *Result) Summary() map[string]string {
	bbrState := "loaded"
	switch {
	case r.BBRLoaded:
	case r.BBRAvailable:
		bbrState = "available, needs modprobe"
	default:
		bbrState = "unavailable"
	}

	firewallState := string(r.Firewall)
	if r.Firewall != FirewallNone && !r.FirewallActive {
		firewallState += " (no rules)"
	}

	return map[string]string{
		"distro":   r.Distro,
		"arch":     r.Arch + " (" + r.RawArch + ")",
		"init":     string(r.Init),
		"firewall": firewallState,
		"bbr":      bbrState,
	}
}

// PortConflict reports what is holding a port, if anything.
func (r *Result) PortConflict(port int) (string, bool) {
	holder, taken := r.OccupiedPorts[port]
	return holder, taken
}
