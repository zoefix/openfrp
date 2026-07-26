#!/usr/bin/env ucode
//
// rpcd backend for luci-app-openfrp.
//
// rpcd kills any call that runs past 30 seconds (/etc/config/rpcd, option
// timeout), and uhttpd caps a CGI request at 60. Server provisioning over SSH
// takes longer than either. So every long operation here is fire-and-forget:
// job_start detaches a worker and returns immediately, and the UI polls
// job_status for incremental output. That also means a deploy survives the
// user navigating away or the browser reconnecting.
//
'use strict';

import { cursor } from 'uci';
import { popen, open, stat, unlink, lsdir, mkdir } from 'fs';

const RUNDIR = '/var/run/openfrp';
const WORKER = '/usr/libexec/openfrp/job';
const INIT = '/etc/init.d/openfrp';

// shellQuote wraps a value for safe interpolation into a shell command.
//
// Job arguments reach us from the browser, so they are untrusted. Single
// quoting with an escape for embedded quotes is the only form that is safe
// for arbitrary bytes in POSIX sh.
function shellQuote(value) {
	return "'" + replace('' + value, "'", "'\\''") + "'";
}

function readFile(path) {
	const fd = open(path, 'r');
	if (!fd)
		return null;
	const data = fd.read('all');
	fd.close();
	return data;
}

function runCommand(cmd) {
	const proc = popen(cmd, 'r');
	if (!proc)
		return null;
	const out = proc.read('all');
	proc.close();
	return out;
}

// serviceRunning reports whether procd has a live instance.
function serviceRunning() {
	const out = runCommand('/etc/init.d/openfrp running 2>/dev/null; echo $?');
	return trim(out ?? '') == '0';
}

function jobPath(id, suffix) {
	// Job ids are generated here, but they still arrive back from the client,
	// so refuse anything that could escape the directory.
	if (!match(id, /^[a-z0-9]+$/))
		return null;
	return RUNDIR + '/' + id + suffix;
}

const methods = {

	// status is the cheap poll the status page runs on a timer. It must stay
	// fast: no shelling out to anything that talks to the network.
	status: {
		call: function() {
			const uci = cursor();
			uci.load('openfrp');

			const tunnels = [];
			uci.foreach('openfrp', 'tunnel', function(section) {
				push(tunnels, {
					name: section.name ?? section['.name'],
					enabled: section.enabled == '1',
					type: section.type ?? 'tcp',
					local: (section.local_ip ?? '') + ':' + (section.local_port ?? ''),
					remote_port: section.remote_port ?? '',
					domains: section.domains ?? []
				});
			});

			const result = {
				enabled: uci.get('openfrp', 'global', 'enabled') == '1',
				running: serviceRunning(),
				server: {
					addr: uci.get('openfrp', 'server', 'addr') ?? '',
					port: uci.get('openfrp', 'server', 'port') ?? '',
					// The token is deliberately not returned. It is a shared
					// secret and the status page has no use for it.
					mux: uci.get('openfrp', 'server', 'mux') == '1'
				},
				tunnels: tunnels
			};

			uci.unload('openfrp');
			return result;
		}
	},

	// log_tail returns recent daemon output. procd pipes the daemon's stdout
	// and stderr into syslog, so that is where it lives.
	log_tail: {
		args: { lines: 0 },
		call: function(req) {
			let lines = req.args?.lines ?? 100;
			if (lines < 1) lines = 100;
			if (lines > 1000) lines = 1000;

			const out = runCommand('logread -e openfrp 2>/dev/null | tail -n ' + int(lines));
			return { log: out ?? '' };
		}
	},

	// job_start detaches a worker and returns immediately.
	job_start: {
		args: { kind: '', args: '' },
		call: function(req) {
			const kind = req.args?.kind ?? '';
			if (!match(kind, /^[a-z_]+$/))
				return { error: 'invalid job kind' };

			if (!stat(WORKER))
				return { error: 'job worker is not installed: ' + WORKER };

			mkdir(RUNDIR, 0o750);

			// Time plus pid is unique enough here: jobs are serialised per
			// router and the directory is wiped on reboot.
			const id = sprintf('%x%x', time(), getpid());

			const logPath = jobPath(id, '.log');
			const statusPath = jobPath(id, '.status');
			if (!logPath || !statusPath)
				return { error: 'could not allocate a job id' };

			// setsid detaches the worker from rpcd's process group, so it
			// survives the 30-second timeout that is about to end this call.
			//
			// Arguments go on the worker's stdin, never in argv: /proc/*/cmdline
			// is readable by every local process, and a deploy carries an SSH
			// password.
			const cmd = sprintf(
				"printf '%%s' %s | setsid %s %s %s %s >/dev/null 2>&1 &",
				shellQuote(req.args?.args ?? ''),
				WORKER, shellQuote(kind), shellQuote(logPath), shellQuote(statusPath)
			);

			const proc = popen(cmd, 'r');
			if (!proc)
				return { error: 'could not start the job worker' };
			proc.close();

			return { id: id };
		}
	},

	// job_status returns the job state plus whatever output has appeared past
	// the offset the caller already has.
	job_status: {
		args: { id: '', offset: 0 },
		call: function(req) {
			const id = req.args?.id ?? '';
			const logPath = jobPath(id, '.log');
			const statusPath = jobPath(id, '.status');

			if (!logPath || !statusPath)
				return { error: 'invalid job id' };

			const offset = int(req.args?.offset ?? 0);
			const raw = readFile(logPath) ?? '';
			const chunk = (offset > 0 && offset <= length(raw))
				? substr(raw, offset)
				: raw;

			let state = 'running';
			const statusRaw = readFile(statusPath);
			if (statusRaw) {
				const parsed = json(trim(statusRaw));
				if (type(parsed) == 'object')
					state = parsed.state ?? state;
			} else if (!stat(logPath)) {
				state = 'unknown';
			}

			return {
				id: id,
				state: state,
				offset: length(raw),
				log: chunk
			};
		}
	},

	job_cancel: {
		args: { id: '' },
		call: function(req) {
			const statusPath = jobPath(req.args?.id ?? '', '.status');
			if (!statusPath)
				return { error: 'invalid job id' };

			const raw = readFile(statusPath);
			if (!raw)
				return { error: 'no such job' };

			const parsed = json(trim(raw));
			const pid = int(parsed?.pid ?? 0);
			if (pid > 1)
				runCommand('kill -TERM -' + pid + ' 2>/dev/null || kill -TERM ' + pid + ' 2>/dev/null');

			return { cancelled: true };
		}
	}
};

return { 'luci.openfrp': methods };
