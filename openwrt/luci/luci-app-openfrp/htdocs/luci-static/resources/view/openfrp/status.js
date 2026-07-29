'use strict';
'require view';
'require rpc';
'require poll';
'require ui';
'require dom';

var callStatus = rpc.declare({
	object: 'luci.openfrp',
	method: 'status',
	expect: {}
});

var callJobStart = rpc.declare({
	object: 'luci.openfrp', method: 'job_start',
	params: ['kind', 'args'], expect: {}
});

var callJobStatus = rpc.declare({
	object: 'luci.openfrp', method: 'job_status',
	params: ['id', 'offset'], expect: {}
});

var callLogTail = rpc.declare({
	object: 'luci.openfrp',
	method: 'log_tail',
	params: ['lines'],
	expect: { log: '' }
});

var callUpdateCheck = rpc.declare({
	object: 'luci.openfrp',
	method: 'update_check',
	params: ['refresh'],
	expect: {}
});

var previous = null;
var updateInfo = null;

function formatBytes(value) {
	value = Number(value) || 0;
	var units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
	var index = 0;
	while (value >= 1024 && index < units.length - 1) {
		value /= 1024;
		index++;
	}
	return (index === 0 ? value : value.toFixed(value < 10 ? 2 : 1)) + ' ' + units[index];
}

function formatRate(bytesPerSecond) {
	if (!(bytesPerSecond > 0))
		return '0 B/s';
	return formatBytes(bytesPerSecond) + '/s';
}

function rates(status) {
	var now = status.traffic && status.traffic.updated_at;
	var out = { tunnels: {}, total: { in: 0, out: 0 } };

	if (!now || !previous || !previous.at || now <= previous.at) {
		return out;
	}

	var elapsed = now - previous.at;

	function rate(current, before) {
		var delta = (current || 0) - (before || 0);
		return delta > 0 ? delta / elapsed : 0;
	}

	(status.tunnels || []).forEach(function (tunnel) {
		var before = previous.tunnels[tunnel.name] || {};
		var traffic = tunnel.traffic || {};
		out.tunnels[tunnel.name] = {
			in: rate(traffic.bytes_in, before.bytes_in),
			out: rate(traffic.bytes_out, before.bytes_out)
		};
	});

	var totalBefore = previous.total || {};
	var total = (status.traffic && status.traffic.total) || {};
	out.total = {
		in: rate(total.bytes_in, totalBefore.bytes_in),
		out: rate(total.bytes_out, totalBefore.bytes_out)
	};

	return out;
}

function remember(status) {
	var snapshot = { at: (status.traffic && status.traffic.updated_at) || 0, tunnels: {} };

	(status.tunnels || []).forEach(function (tunnel) {
		snapshot.tunnels[tunnel.name] = tunnel.traffic || {};
	});
	snapshot.total = (status.traffic && status.traffic.total) || {};

	previous = snapshot;
}

function badge(ok, okText, badText) {
	return E('span', {
		'style': 'padding:2px 8px;border-radius:3px;white-space:nowrap;' +
			(ok ? 'background:#5bb75b;color:#fff' : 'background:#faa732;color:#fff')
	}, ok ? okText : badText);
}

var hiddenLogKey = 'openfrp.log.dismissed';
var hiddenLog = readHiddenLog();

function readHiddenLog() {
	try {
		return window.localStorage.getItem(hiddenLogKey) || '';
	} catch (e) {
		return '';
	}
}

function rememberHiddenLog(text) {
	hiddenLog = text;
	try {
		if (text)
			window.localStorage.setItem(hiddenLogKey, text);
		else
			window.localStorage.removeItem(hiddenLogKey);
	} catch (e) {
	}
}

function clearLogButton(logBox) {
	return E('button', {
		'class': 'btn',
		'click': function () {
			rememberHiddenLog(logBox.textContent || '');
			dom.content(logBox, _('No log output yet.'));
		}
	}, _('Clear'));
}

function visibleLog(text) {
	if (hiddenLog && text.indexOf(hiddenLog) === 0)
		return text.slice(hiddenLog.length).replace(/^\n+/, '');
	if (hiddenLog && text.indexOf(hiddenLog) < 0)
		rememberHiddenLog('');
	return text;
}

var restartControl = null;

function updateBadge() {
	if (!updateInfo || !updateInfo.available || !updateInfo.latest)
		return null;

	var button = E('button', {
		'class': 'btn cbi-button-action',
		'style': 'margin-left:1em'
	}, _('Update to %s').format(updateInfo.latest));

	button.addEventListener('click', function () { showUpdateDialog(); });
	return button;
}

function showUpdateDialog() {
	var info = updateInfo || {};

	var notes = E('pre', {
		'style': 'max-height:22em;overflow:auto;white-space:pre-wrap;' +
			'background:#f6f6f6;padding:.8em;border-radius:4px;margin:0'
	});
	notes.textContent = info.notes || _('This release published no notes.');

	var progress = E('pre', {
		'style': 'display:none;max-height:16em;overflow:auto;white-space:pre-wrap;' +
			'background:#111;color:#eee;padding:.8em;border-radius:4px;margin:.8em 0 0'
	});

	var note = E('span', { 'style': 'margin-right:1em' }, '');
	var confirmButton = E('button', { 'class': 'btn cbi-button-positive' }, _('Update now'));
	var closeButton = E('button', { 'class': 'btn' }, _('Cancel'));

	closeButton.addEventListener('click', function () { ui.hideModal(); });

	confirmButton.addEventListener('click', function () {
		confirmButton.disabled = true;
		closeButton.disabled = true;
		note.textContent = _('Updating…');
		progress.style.display = '';
		progress.textContent = '';

		callJobStart('update', '').then(function (res) {
			if (!res || res.error || !res.id) {
				note.textContent = (res && res.error) || _('no response');
				confirmButton.disabled = false;
				closeButton.disabled = false;
				return;
			}
			followUpdate(res.id, progress, note, confirmButton, closeButton);
		});
	});

	ui.showModal(_('Update to %s').format(info.latest || ''), [
		E('p', {}, _('Running %s. The client, the server binary and this interface are all replaced, then the service restarts. If it does not come back up the previous version is put back automatically.').format(info.current || '')),
		notes,
		progress,
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			note, closeButton, ' ', confirmButton
		])
	]);
}

function followUpdate(jobId, progress, note, confirmButton, closeButton) {
	var seen = 0;

	function tick() {
		return callJobStatus(jobId, seen).then(function (res) {
			if (!res || res.error)
				return;

			if (res.log) {
				progress.textContent += res.log;
				progress.scrollTop = progress.scrollHeight;
			}
			if (typeof res.offset === 'number')
				seen = res.offset;

			if (res.state === 'running')
				return;

			poll.remove(tick);
			closeButton.disabled = false;
			closeButton.textContent = _('Close');

			if (res.state === 'succeeded') {
				note.textContent = _('Updated. Reload the page.');
				updateInfo = null;
			} else {
				note.textContent = _('The update failed — the previous version is still running.');
				confirmButton.disabled = false;
			}
		});
	}

	poll.add(tick, 1);
	tick();
}

function restartButton() {
	if (restartControl)
		return restartControl;

	var button = E('button', { 'class': 'btn cbi-button-action' }, _('Restart'));
	var note = E('span', { 'style': 'margin-left:1em' }, '');

	button.addEventListener('click', function () {
		if (!confirm(_('Restart the client? Every connection in progress is dropped.')))
			return;

		button.disabled = true;
		note.textContent = _('Restarting…');

		callJobStart('restart', '').then(function (res) {
			if (!res || res.error || !res.id) {
				note.textContent = (res && res.error) || _('no response');
				button.disabled = false;
				return;
			}
			follow(res.id);
		});
	});

	function follow(jobId) {
		function tick() {
			return callJobStatus(jobId, 0).then(function (res) {
				if (!res || res.error || res.state === 'running')
					return;

				poll.remove(tick);
				button.disabled = false;
				note.textContent = res.state === 'succeeded'
					? _('Restarted.')

					: _('Did not come back up — see the log below.');
			});
		}
		poll.add(tick, 1);
		tick();
	}

	restartControl = E('span', {}, [button, note]);
	return restartControl;
}

function infoRow(label, value) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'style': 'width:28%' }, label),
		E('td', { 'class': 'td left' }, value)
	]);
}

function overviewChildren(status, speed) {
	speed = speed || { total: { in: 0, out: 0 } };
	var serviceState;
	if (!status.enabled)
		serviceState = badge(false, '', _('Disabled'));
	else
		serviceState = badge(status.running, _('Running'), _('Enabled but not running'));

	var total = (status.traffic && status.traffic.total) || {};

	var traffic = E('span', {}, [
		'↓ ' + formatBytes(total.bytes_in) + ' (' + formatRate(speed.total.in) + ')',
		E('span', { 'style': 'margin-left:1.5em' },
			'↑ ' + formatBytes(total.bytes_out) + ' (' + formatRate(speed.total.out) + ')')
	]);

	var rows = [
		infoRow(_('Service'), E('span', {}, [
			serviceState,
			E('span', { 'style': 'margin-left:1em' }, restartButton())
		])),

		infoRow(_('Client version'), E('span', {}, [
			status.client_version
				? E('code', {}, status.client_version)
				: E('em', {}, _('unknown')),
			updateBadge()
		].filter(Boolean))),
		infoRow(_('Total traffic'), traffic)
	];

	return [E('h3', {}, _('Overview')), E('table', { 'class': 'table' }, rows)];
}

function tunnelsChildren(tunnels, speed) {
	speed = speed || { tunnels: {} };
	if (!tunnels || !tunnels.length)
		return [
			E('h3', {}, _('Tunnels')),
			E('p', {}, _('No tunnels are configured yet.'))
		];

	var head = E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, _('Name')),
		E('th', { 'class': 'th' }, _('Type')),
		E('th', { 'class': 'th' }, _('Local target')),
		E('th', { 'class': 'th' }, _('Exposed as')),
		E('th', { 'class': 'th' }, _('Connections')),
		E('th', { 'class': 'th' }, _('Download')),
		E('th', { 'class': 'th' }, _('Upload')),
		E('th', { 'class': 'th' }, _('State'))
	]);

	var rows = tunnels.map(function (tunnel) {
		var exposed;
		if (tunnel.domains && tunnel.domains.length)
			exposed = tunnel.domains.join(', ');
		else if (tunnel.remote_port)
			exposed = _('port %s').format(tunnel.remote_port);
		else
			exposed = E('em', {}, _('server-allocated'));

		var traffic = tunnel.traffic || {};
		var rate = speed.tunnels[tunnel.name] || { in: 0, out: 0 };

		function cell(bytes, perSecond) {
			return E('td', { 'class': 'td', 'style': 'white-space:nowrap' }, [
				E('div', {}, formatBytes(bytes)),
				E('div', { 'style': 'font-size:88%;opacity:0.7' }, formatRate(perSecond))
			]);
		}

		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td' }, tunnel.name),
			E('td', { 'class': 'td' }, tunnel.type),
			E('td', { 'class': 'td' }, tunnel.local),
			E('td', { 'class': 'td' }, exposed),
			E('td', { 'class': 'td' }, String(traffic.active || 0) +
				' / ' + String(traffic.connections || 0)),
			cell(traffic.bytes_in, rate.in),
			cell(traffic.bytes_out, rate.out),
			E('td', { 'class': 'td' }, badge(tunnel.enabled, _('Enabled'), _('Disabled')))
		]);
	});

	return [
		E('h3', {}, _('Tunnels')),
		E('table', { 'class': 'table' }, [head].concat(rows))
	];
}

function fetch() {
	return Promise.all([
		callStatus().catch(function () { return null; }),
		callLogTail(200).catch(function () { return ''; }),
		callUpdateCheck(false).catch(function () { return null; })
	]);
}

function refreshUpdate() {
	return callUpdateCheck(false).then(function (res) {
		if (res && !res.error)
			updateInfo = res;
	}).catch(function () { });
}

function stylesheet() {
	return E('link', {
		'rel': 'stylesheet',
		'href': L.resource('openfrp/openfrp.css')
	});
}

return view.extend({
	load: fetch,

	render: function (data) {
		var status = data[0];

		if (data[2] && !data[2].error)
			updateInfo = data[2];

		if (!status)
			return E('div', { 'class': 'alert-message warning' }, [
				E('p', {}, _('Could not reach the OpenFrp backend.')),
				E('p', {}, _('If the ACL file was just installed, rpcd needs a restart: ' +
					'/etc/init.d/rpcd restart'))
			]);

		var speed = rates(status);
		var overview = E('div', { 'class': 'cbi-section' },
			overviewChildren(status, speed));
		var tunnels = E('div', { 'class': 'cbi-section' },
			tunnelsChildren(status.tunnels, speed));
		remember(status);

		var logBox = E('pre', {
			'style': 'max-height:24em;overflow:auto;white-space:pre-wrap;' +
				'font-size:90%;background:#1e1e1e;color:#ddd;padding:0.6em;border-radius:3px'
		}, data[1] || _('No log output yet.'));

		poll.add(function () {
			return fetch().then(function (fresh) {
				if (!fresh[0])
					return;

				var speed = rates(fresh[0]);
				dom.content(overview, overviewChildren(fresh[0], speed));
				dom.content(tunnels, tunnelsChildren(fresh[0].tunnels, speed));
				remember(fresh[0]);
				var log = visibleLog(fresh[1] || '');
				if (log !== logBox.textContent)
					dom.content(logBox, log || _('No log output yet.'));
			});
		}, 5);

		poll.add(refreshUpdate, 300);

		return E('div', {}, [
			stylesheet(),
			E('h2', {}, 'OpenFrp'),
			overview,
			tunnels,
			E('div', { 'class': 'cbi-section' }, [
				E('div', {
					'style': 'display:flex;align-items:baseline;justify-content:space-between'
				}, [E('h3', {}, _('Recent log')), clearLogButton(logBox)]),
				logBox
			])
		]);
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
