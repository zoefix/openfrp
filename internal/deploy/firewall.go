package deploy

import (
	"context"
	"fmt"
	"strconv"

	"github.com/zoefix/openfrp/internal/deploy/detect"
)

// openPorts opens the ports the server needs.
//
// The rule everywhere here is to add, never to replace. A VPS firewall usually
// belongs to somebody else's automation, and a deployment tool that flushes a
// ruleset to install itself has done far more damage than the convenience is
// worth.
func (d *Deployer) openPorts(ctx context.Context, report stepReporter, info *detect.Result, ports []int) error {
	if len(ports) == 0 {
		return nil
	}

	if info.Firewall == detect.FirewallNone {
		report.Infof("no firewall found; nothing to open")
		return nil
	}

	if !info.FirewallActive {
		// An installed-but-empty nftables is the normal state of a fresh VPS.
		// Adding rules to an empty ruleset would be the first step toward a
		// default-deny policy nobody asked for.
		report.Infof("%s is installed but has no rules; leaving it alone", info.Firewall)
		return nil
	}

	for _, port := range ports {
		commands := firewallCommands(info.Firewall, port)
		if len(commands) == 0 {
			report.Warnf("cannot open port %d automatically under %s; open it by hand",
				port, info.Firewall)
			continue
		}

		if d.opts.DryRun {
			report.Infof("would open port %d via %s", port, info.Firewall)
			continue
		}

		for _, command := range commands {
			if _, err := d.session.Run(ctx, command); err != nil {
				// Not fatal. A rule may already exist, or the ruleset may be
				// managed declaratively. Verification will catch a genuine
				// reachability problem later, with a better message.
				report.Warnf("could not open port %d: %v", port, err)
				break
			}
		}
		report.Infof("opened port %d", port)
	}

	return nil
}

func firewallCommands(fw detect.Firewall, port int) []string {
	p := strconv.Itoa(port)

	switch fw {
	case detect.FirewallFirewalld:
		return []string{
			"firewall-cmd --permanent --add-port=" + p + "/tcp",
			"firewall-cmd --reload",
		}
	case detect.FirewallUFW:
		return []string{"ufw allow " + p + "/tcp"}
	case detect.FirewallNftables:
		// Target the inet filter input chain, which is what every modern
		// distribution ships. Failure is reported by the caller.
		return []string{fmt.Sprintf(
			"nft add rule inet filter input tcp dport %s accept", p)}
	case detect.FirewallIptables:
		// Check before inserting so a re-run does not stack duplicate rules.
		return []string{fmt.Sprintf(
			"iptables -C INPUT -p tcp --dport %s -j ACCEPT 2>/dev/null || "+
				"iptables -I INPUT -p tcp --dport %s -j ACCEPT", p, p)}
	default:
		return nil
	}
}

// enableBBR loads the BBR module and switches congestion control to it.
//
// The module has to be loaded before the sysctl is set. On both test servers
// tcp_bbr shipped with the kernel but was not loaded, which keeps BBR out of
// tcp_available_congestion_control — and writing the sysctl in that state
// fails without saying anything useful.
func (d *Deployer) enableBBR(ctx context.Context, report stepReporter, info *detect.Result) {
	if !d.opts.EnableBBR {
		return
	}

	if info.BBRLoaded {
		current := d.session.Output(ctx, "sysctl -n net.ipv4.tcp_congestion_control")
		if current == "bbr" {
			report.Infof("BBR is already in use")
			return
		}
	} else if !info.BBRAvailable {
		report.Warnf("BBR is not available on this kernel; leaving congestion control alone")
		return
	}

	if d.opts.DryRun {
		report.Infof("would load tcp_bbr and switch congestion control to BBR")
		return
	}

	if !info.BBRLoaded {
		if _, err := d.session.Run(ctx, "modprobe tcp_bbr"); err != nil {
			report.Warnf("could not load tcp_bbr: %v", err)
			return
		}
	}

	// Persist all three: the module so it survives a reboot, and both sysctls
	// because BBR without fq falls back to a queue discipline that undoes much
	// of the benefit.
	commands := []string{
		"echo tcp_bbr > /etc/modules-load.d/openfrp-bbr.conf 2>/dev/null || " +
			"echo tcp_bbr >> /etc/modules 2>/dev/null || true",
		"printf 'net.core.default_qdisc=fq\\nnet.ipv4.tcp_congestion_control=bbr\\n' " +
			"> /etc/sysctl.d/99-openfrp-bbr.conf",
		"sysctl -w net.core.default_qdisc=fq",
		"sysctl -w net.ipv4.tcp_congestion_control=bbr",
	}
	for _, command := range commands {
		if _, err := d.session.Run(ctx, command); err != nil {
			report.Warnf("BBR tuning step failed: %v", err)
			return
		}
	}

	active := d.session.Output(ctx, "sysctl -n net.ipv4.tcp_congestion_control")
	if active == "bbr" {
		report.Successf("congestion control switched to BBR with fq")
	} else {
		report.Warnf("BBR did not take effect; congestion control is %q", active)
	}
}
