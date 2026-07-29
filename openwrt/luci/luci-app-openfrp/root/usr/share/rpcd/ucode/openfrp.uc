#!/usr/bin/env ucode

'use strict';

import { cursor } from 'uci';
import { popen, open, stat, unlink, mkdir } from 'fs';
import { rand, srand } from 'math';

const RUNDIR = '/var/run/openfrp';
const WORKER = '/usr/libexec/openfrp/job';
const INIT = '/etc/init.d/openfrp';
const CLIENT = '/usr/bin/openfrpc';
const STATS = '/var/run/openfrp/stats.json';

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

function serviceRunning() {
	const out = runCommand('/etc/init.d/openfrp running 2>/dev/null; echo $?');
	return trim(out ?? '') == '0';
}

let versionCache = null;

function binaryVersion() {
	const info = stat(CLIENT);
	if (!info)
		return '';
	if (versionCache && versionCache.mtime == info.mtime)
		return versionCache.version;

	const out = runCommand(CLIENT + ' version 2>/dev/null');
	const fields = split(trim(out ?? ''), ' ');
	const version = length(fields) > 1 ? split(fields[1], '+')[0] : '';

	versionCache = { mtime: info.mtime, version: version };
	return version;
}

function jobPath(id, suffix) {
	if (!match(id, /^[a-z0-9]+$/))
		return null;
	return RUNDIR + '/' + id + suffix;
}

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

	status: {
		call: function() {
			const uci = cursor();
			uci.load('openfrp');

			const zones = {};
			let firstServer = null;
			uci.foreach('openfrp', 'server', function(section) {
				const name = section['.name'];
				if (firstServer === null)
					firstServer = name;
				if (section.kind == 'cloudflare')
					zones[name] = section.zone ?? '';
			});

			const tunnels = [];
			uci.foreach('openfrp', 'tunnel', function(section) {
				let domains = section.domains ?? [];

				const owner = section.server ?? firstServer;
				const zone = zones[owner];
				if (zone) {
					domains = [];
					let prefixes = section.cf_prefix ?? [];
					if (type(prefixes) != 'array')
						prefixes = [prefixes];
					for (let prefix in prefixes)
						push(domains, prefix == '@' ? zone : prefix + '.' + zone);
				}

				push(tunnels, {
					name: section.name ?? section['.name'],
					enabled: section.enabled == '1',
					type: section.type ?? 'tcp',
					local: (section.local_ip ?? '') + ':' + (section.local_port ?? ''),
					remote_port: section.remote_port ?? '',
					domains: domains
				});
			});

			let traffic = {};
			const raw = readFile(STATS);
			if (raw) {
				const parsed = json(trim(raw));
				if (type(parsed) == 'object')
					traffic = parsed;
			}

			for (let tunnel in tunnels)
				tunnel.traffic = traffic.tunnels?.[tunnel.name];

			const running = serviceRunning();

			const result = {
				enabled: uci.get('openfrp', 'global', 'enabled') == '1',
				running: running,

				client_version: (running && traffic.client_version)
					? traffic.client_version : binaryVersion(),

				servers: running ? (traffic.servers ?? {}) : {},
				traffic: {
					updated_at: traffic.updated_at ?? 0,
					uptime_seconds: traffic.uptime_seconds ?? 0,
					total: traffic.total ?? {}
				},
				server: {
					addr: uci.get('openfrp', 'server', 'addr') ?? '',
					port: uci.get('openfrp', 'server', 'port') ?? '',

					mux: uci.get('openfrp', 'server', 'mux') == '1'
				},
				tunnels: tunnels
			};

			uci.unload('openfrp');
			return result;
		}
	},

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

	job_start: {
		args: { kind: '', args: '' },
		call: function(req) {
			const kind = req.args?.kind ?? '';
			if (!match(kind, /^[a-z_]+$/))
				return { error: 'invalid job kind' };

			if (!stat(WORKER))
				return { error: 'job worker is not installed: ' + WORKER };

			mkdir(RUNDIR, 0o750);

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

			const fd = open(argsPath, 'w', 0o600);
			if (!fd)
				return { error: 'could not stage the job arguments' };
			fd.write(req.args?.args ?? '');
			fd.close();

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
