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
import { popen, open, stat, unlink, mkdir } from 'fs';
import { rand, srand } from 'math';

const RUNDIR = '/var/run/openfrp';
const WORKER = '/usr/libexec/openfrp/job';
const INIT = '/etc/init.d/openfrp';
const CLIENT = '/usr/bin/openfrpc';

// Management actions, by domain. An allowlist rather than a pass-through:
// the action becomes a command line, and "whatever the browser sent" is not
// something to hand to a shell.
//
// Issuing a certificate is absent on purpose. It talks to a CA and waits for
// DNS propagation, so it takes minutes and would be killed by rpcd's 30 second
// timeout; it runs as a detached job instead.
const ACTIONS = {
	dns: [
		'providers', 'accounts', 'account-add', 'account-update',
		'account-delete', 'account-test', 'capabilities',
		'domains', 'records', 'record-add', 'record-update',
		'record-delete', 'record-status'
	],
	cert: [
		'cas', 'keytypes', 'orders', 'order-add', 'order-delete',
		'events', 'export', 'eab', 'eab-status'
	]
};

// Flags a management action may carry, and how to check each value.
//
// Everything is shell quoted as well; this is the second layer, and it is what
// stops a plausible-looking value reaching a flag that was never meant to take
// one.
const FLAGS = {
	id:      /^[0-9]+$/,
	limit:   /^[0-9]+$/,
	page:    /^[0-9]+$/,
	record:  /^[A-Za-z0-9._:-]+$/,
	zone:    /^[A-Za-z0-9._-]+$/,
	enabled: /^(true|false)$/,
	keyword: /^[^\n\r]{0,64}$/,
	ca:      /^[a-z0-9-]{1,32}$/,
	email:   /^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/
};

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

// stageStdin writes a request body to a private file and returns its path.
//
// The body carries DNS provider credentials, so it must not appear in any
// command line: /proc/<pid>/cmdline is readable by every local process. RUNDIR
// is tmpfs, so this never reaches flash.
function stageStdin(payload) {
	mkdir(RUNDIR, 0o750);

	const path = sprintf('%s/req%x%04x', RUNDIR, time(), rand() % 0x10000);
	const fd = open(path, 'w', 0o600);
	if (!fd)
		return null;

	fd.write(payload);
	fd.close();
	return path;
}

// manageCall runs one openfrpc management action and returns its parsed reply.
function manageCall(domain, action, params, payload) {
	const allowed = ACTIONS[domain];
	if (!allowed || index(allowed, action) < 0)
		return { error: 'unsupported action: ' + domain + ' ' + action };

	if (!stat(CLIENT))
		return { error: 'openfrpc is not installed' };

	let cmd = CLIENT + ' ' + domain + ' ' + shellQuote(action);

	for (let name, value in (params ?? {})) {
		const pattern = FLAGS[name];
		if (!pattern)
			return { error: 'unsupported parameter: ' + name };
		if (!match('' + value, pattern))
			return { error: sprintf('invalid value for %s', name) };

		cmd += ' -' + name + ' ' + shellQuote(value);
	}

	let staged = null;
	if (payload != null && payload != '') {
		staged = stageStdin(payload);
		if (!staged)
			return { error: 'could not stage the request' };
		cmd += ' < ' + shellQuote(staged);
	}

	const proc = popen(cmd + ' 2>/dev/null', 'r');
	if (!proc) {
		if (staged) unlink(staged);
		return { error: 'could not run openfrpc' };
	}
	const out = proc.read('all');
	proc.close();

	if (staged)
		unlink(staged);

	const parsed = json(trim(out ?? ''));
	if (type(parsed) != 'object')
		return { error: 'openfrpc returned nothing usable' };

	return parsed;
}

const methods = {

	// dns and cert are thin, validated pass-throughs to openfrpc. The real
	// logic lives in Go, where it is testable, rather than in ucode.
	dns: {
		args: { action: '', params: {}, payload: '' },
		call: function(req) {
			return manageCall('dns', req.args?.action ?? '',
				req.args?.params, req.args?.payload);
		}
	},

	cert: {
		args: { action: '', params: {}, payload: '' },
		call: function(req) {
			return manageCall('cert', req.args?.action ?? '',
				req.args?.params, req.args?.payload);
		}
	},


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

			// ucode has no getpid(), so the id is the clock plus a random
			// suffix, retried until it names a file that does not exist.
			//
			// An earlier version called getpid() — which does not exist in
			// ucode either — and the resulting exception failed *every*
			// job_start with a bare "Unknown error", including the deploy
			// button. Nothing in the log said which call was at fault.
			srand(time());
			let id = null;
			for (let attempt = 0; attempt < 16; attempt++) {
				const candidate = sprintf('%x%04x', time(), rand() % 0x10000);
				if (!stat(RUNDIR + '/' + candidate + '.status')) {
					id = candidate;
					break;
				}
			}
			if (!id)
				return { error: 'could not allocate a job id' };

			const logPath = jobPath(id, '.log');
			const statusPath = jobPath(id, '.status');
			const argsPath = jobPath(id, '.args');
			if (!logPath || !statusPath || !argsPath)
				return { error: 'could not allocate a job id' };

			// Job arguments carry the SSH password, so they are handed to the
			// worker through a file and only the path is ever spoken aloud.
			//
			// The obvious `printf '%s' <args> | worker` does not work here: it
			// keeps the password out of the *worker's* argv but puts it in the
			// argv of the intermediate shell, where any local process can read
			// it from /proc/<pid>/cmdline. That was verified on a live router,
			// not assumed — a grep for the secret across /proc matches that
			// shell for as long as it lives.
			//
			// RUNDIR is on tmpfs, so this file is RAM only and never reaches
			// flash, and the worker unlinks it the moment it has been read.
			const fd = open(argsPath, 'w', 0o600);
			if (!fd)
				return { error: 'could not stage the job arguments' };
			fd.write(req.args?.args ?? '');
			fd.close();

			// setsid detaches the worker from rpcd's process group, so it
			// survives the 30-second timeout that is about to end this call.
			const cmd = sprintf('setsid %s %s %s %s %s >/dev/null 2>&1 &',
				WORKER, shellQuote(kind), shellQuote(logPath),
				shellQuote(statusPath), shellQuote(argsPath));

			const proc = popen(cmd, 'r');
			if (!proc) {
				unlink(argsPath);
				return { error: 'could not start the job worker' };
			}
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

// An uncaught exception in a method reaches the browser as a bare "Unknown
// error" and writes nothing to the log — there is no indication of which call
// failed, or that a call failed at all rather than the object being missing.
// A getpid() typo hid behind that message and broke every job on this backend.
//
// Wrapping the methods turns any such crash into a message the UI can show.
for (let name in methods) {
	const inner = methods[name].call;
	methods[name].call = function(req) {
		try {
			return inner(req);
		} catch (e) {
			return { error: 'openfrp backend: ' + e };
		}
	};
}

return { 'luci.openfrp': methods };
