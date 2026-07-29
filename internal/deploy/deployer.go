package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/zoefix/openfrp/internal/deploy/detect"
	"github.com/zoefix/openfrp/internal/tunnel/protocol"
)

type Options struct {
	Credentials Credentials

	Token string

	BindPort       int
	VhostHTTPPort  int
	VhostHTTPSPort int

	BinaryPath  string
	ConfigPath  string
	StateDir    string
	ServiceUser string

	LocalBinary string

	ReleaseURL string

	SHA256 string

	EnableBBR bool
	DryRun    bool
}

func (o *Options) ApplyDefaults() {
	if o.BindPort == 0 {
		o.BindPort = 7000
	}
	if o.BinaryPath == "" {
		o.BinaryPath = "/usr/local/bin/openfrps"
	}
	if o.ConfigPath == "" {
		o.ConfigPath = "/etc/openfrp/openfrps.json"
	}
	if o.StateDir == "" {
		o.StateDir = "/var/lib/openfrp"
	}
	if o.ServiceUser == "" {
		o.ServiceUser = "openfrp"
	}
}

type Result struct {
	Replaced bool `json:"replaced,omitempty"`

	Fingerprint string            `json:"host_fingerprint"`
	Token       string            `json:"token"`
	BindPort    int               `json:"bind_port"`
	Detected    map[string]string `json:"detected"`
	Verified    bool              `json:"verified"`
}

type Deployer struct {
	opts    Options
	session *Session
	report  Reporter
}

func New(opts Options, report Reporter) *Deployer {
	opts.ApplyDefaults()
	return &Deployer{opts: opts, report: report}
}

func (d *Deployer) step(name string) stepReporter {
	return stepReporter{Reporter: d.report, step: name}
}

func (d *Deployer) Run(ctx context.Context) (*Result, error) {
	result := &Result{BindPort: d.opts.BindPort}

	if d.opts.Token == "" {
		token, err := protocol.NewRunID()
		if err != nil {
			return nil, fmt.Errorf("deploy: generate token: %w", err)
		}
		d.opts.Token = token
	}
	result.Token = d.opts.Token

	connect := d.step("connect")
	connect.Infof("connecting to %s", d.opts.Credentials.Host)

	session, err := Connect(ctx, d.opts.Credentials)
	if err != nil {
		if errors.Is(err, ErrHostKeyChanged) {
			connect.Emit(Event{Level: LevelError, Message: err.Error()})
			return nil, fmt.Errorf("%w — refusing to continue; if this change was "+
				"expected, clear the stored fingerprint and retry", err)
		}
		return nil, err
	}
	defer session.Close()

	d.session = session
	result.Fingerprint = session.Fingerprint

	if d.opts.Credentials.KnownFingerprint == "" {
		connect.Warnf("trusting host key on first use: %s", session.Fingerprint)
	}
	connect.Successf("connected")

	probe := d.step("detect")
	info, err := detect.Detect(ctx, session)
	if err != nil {
		return nil, err
	}

	existing := d.findExisting(ctx)
	result.Replaced = existing.Found
	if existing.Found {
		probe.Infof("an OpenFrp server is already installed: %s", existing.Describe())
	}
	result.Detected = info.Summary()
	probe.Detail("inspected the server", info.Summary())

	if !info.Root {
		return nil, errors.New("deploy: this needs root on the server; " +
			"connect as root or configure passwordless sudo")
	}

	if err := d.checkPorts(probe, info); err != nil {
		return nil, err
	}

	install := d.step("install")

	if d.opts.DryRun {
		if existing.Found {
			install.Infof("would remove the existing installation: %s", existing.Describe())
		}
	} else if err := d.removeExisting(ctx, install, existing); err != nil {
		return nil, err
	}

	if err := d.ensureServiceUser(ctx, install); err != nil {
		return nil, err
	}
	if err := d.installBinary(ctx, install, info); err != nil {
		return nil, err
	}
	if err := d.installConfig(ctx, install); err != nil {
		return nil, err
	}

	if err := d.installService(ctx, d.step("service"), info); err != nil {
		return nil, err
	}

	network := d.step("network")
	if err := d.openPorts(ctx, network, info, d.wantedPorts()); err != nil {
		return nil, err
	}
	d.enableBBR(ctx, network, info)

	verify := d.step("verify")
	if d.opts.DryRun {
		verify.Infof("dry run: nothing was changed")
		return result, nil
	}

	if err := d.verify(ctx, verify); err != nil {
		return result, err
	}
	result.Verified = true

	d.step("done").Successf("server is deployed and reachable on port %d", d.opts.BindPort)
	return result, nil
}

func (d *Deployer) wantedPorts() []int {
	ports := []int{d.opts.BindPort}
	if d.opts.VhostHTTPPort != 0 {
		ports = append(ports, d.opts.VhostHTTPPort)
	}
	if d.opts.VhostHTTPSPort != 0 {
		ports = append(ports, d.opts.VhostHTTPSPort)
	}
	return ports
}

func (d *Deployer) checkPorts(report stepReporter, info *detect.Result) error {
	for _, port := range d.wantedPorts() {
		holder, taken := info.PortConflict(port)
		if !taken {
			continue
		}

		if holder == "openfrps" {
			continue
		}
		return fmt.Errorf(
			"deploy: port %d is already held by %q; choose a different port "+
				"or stop that service first", port, holder)
	}
	return nil
}

func (d *Deployer) verify(ctx context.Context, report stepReporter) error {
	status := d.session.Output(ctx,
		"ss -lnt 2>/dev/null | grep -c ':"+strconv.Itoa(d.opts.BindPort)+"' || true")
	if status == "0" {
		logs := d.session.Output(ctx,
			"journalctl -u openfrps -n 20 --no-pager 2>/dev/null || tail -20 /var/log/openfrps.log 2>/dev/null")
		report.Emit(Event{Level: LevelError, Message: "the server is not listening"})
		if logs != "" {
			report.Infof("recent server log:\n%s", logs)
		}
		return errors.New("deploy: the server did not start listening")
	}
	report.Infof("the server is listening on the target port")

	addr := net.JoinHostPort(d.opts.Credentials.Host, strconv.Itoa(d.opts.BindPort))

	var lastErr error
	for attempt := range 10 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}

		if err := d.handshake(addr); err != nil {
			lastErr = err
			continue
		}
		report.Successf("completed a protocol handshake with %s", addr)
		return nil
	}

	report.Emit(Event{Level: LevelError,
		Message: fmt.Sprintf("the server is listening locally but unreachable from here: %v", lastErr)})
	report.Infof("this is normally a firewall in front of the host — a cloud " +
		"provider security group, or a network filter on the path. Allow " +
		"inbound TCP on the ports above and run this again.")

	return fmt.Errorf("deploy: %s is not usable from this machine: %w", addr, lastErr)
}

func (d *Deployer) handshake(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}

	if err := protocol.WritePreamble(conn, protocol.Preamble{
		Version: protocol.Version,
		Mode:    protocol.ModePlain,
	}); err != nil {
		return fmt.Errorf("send greeting: %w", err)
	}

	timestamp := time.Now().Unix()
	if err := protocol.WriteMessage(conn, &protocol.Login{
		Version:    protocol.Version,
		ClientName: "deploy-verify",
		Timestamp:  timestamp,
		AuthKey:    protocol.AuthKey(d.opts.Token, timestamp),
		PoolCount:  1,
	}); err != nil {
		return fmt.Errorf("send login: %w", err)
	}

	msg, err := protocol.ReadMessage(conn)
	if err != nil {
		return fmt.Errorf("read login response: %w", err)
	}
	resp, ok := msg.(*protocol.LoginResp)
	if !ok {
		return fmt.Errorf("unexpected reply %s", msg.Type())
	}
	if resp.Error != "" {
		return fmt.Errorf("server rejected the handshake: %s", resp.Error)
	}
	return nil
}
