'use strict';
'require view';
'require rpc';
'require poll';
'require ui';
'require dom';

/*
 * Live status.
 *
 * Everything here comes from one cheap rpcd call on a timer. The backend
 * deliberately does no network work in `status`, so polling it every few
 * seconds costs nothing on the router.
 *
 * The render helpers return ARRAYS of child nodes rather than a wrapper
 * element. That is not a style preference: dom.content() accepts a string, a
 * Node or an Array, and anything else is stringified. Passing a live NodeList
 * — the obvious thing to reach for when refreshing a container — renders the
 * literal text "[object NodeList]" with no error anywhere.
 */

var callStatus = rpc.declare({
	object: 'luci.openfrp',
	method: 'status',
	expect: {}
});

var callLogTail = rpc.declare({
	object: 'luci.openfrp',
	method: 'log_tail',
	params: ['lines'],
	expect: { log: '' }
});

/*
 * Rates are derived here rather than reported by the daemon.
 *
 * The daemon publishes cumulative byte counts and the timestamp it wrote them.
 * A rate it computed would be an average over whatever interval it happened to
 * run on; computed from two consecutive readings it is an average over exactly
 * the period between them, which is what "current speed" means to a reader.
 */
var previous = null;

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

// rates returns per-tunnel and total byte rates between two status readings.
function rates(status) {
	var now = status.traffic && status.traffic.updated_at;
	var out = { tunnels: {}, total: { in: 0, out: 0 } };

	if (!now || !previous || !previous.at || now <= previous.at) {
		// No second sample yet, or the daemon has not republished since the
		// last poll. Reporting a rate from a single reading, or dividing by a
		// zero interval, would invent a number.
		return out;
	}

	var elapsed = now - previous.at;

	function rate(current, before) {
		// Counters reset when the daemon restarts. A negative delta means that
		// happened, and reporting it as a huge negative rate would be worse
		// than reporting nothing for one interval.
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

// remember stores this reading so the next one can be differenced against it.
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

function infoRow(label, value) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'style': 'width:28%' }, label),
		E('td', { 'class': 'td left' }, value)
	]);
}

// Returns the children of the overview section.
function overviewChildren(status, speed) {
	speed = speed || { total: { in: 0, out: 0 } };
	var serviceState;
	if (!status.enabled)
		serviceState = badge(false, '', _('Disabled'));
	else
		serviceState = badge(status.running, _('Running'), _('Enabled but not running'));

	var server = status.server.addr
		? status.server.addr + ':' + status.server.port
		: E('em', {}, _('not configured'));

	var transport = status.server.mux
		? _('Multiplexed — shared congestion window, no kernel zero-copy')
		: _('Connection pool — independent congestion windows, kernel zero-copy');

	var total = (status.traffic && status.traffic.total) || {};

	var traffic = E('span', {}, [
		'↓ ' + formatBytes(total.bytes_in) + ' (' + formatRate(speed.total.in) + ')',
		E('span', { 'style': 'margin-left:1.5em' },
			'↑ ' + formatBytes(total.bytes_out) + ' (' + formatRate(speed.total.out) + ')')
	]);

	var rows = [
		infoRow(_('Service'), serviceState),
		infoRow(_('Server'), server),
		infoRow(_('Transport'), transport),
		infoRow(_('Total traffic'), traffic)
	];

	return [E('h3', {}, _('Overview')), E('table', { 'class': 'table' }, rows)];
}

// Returns the children of the tunnels section.
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

		// Cumulative on top, current rate underneath: the total answers "how
		// much has this moved", the rate answers "is it moving now", and a
		// status page is asked both.
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
		callLogTail(200).catch(function () { return ''; })
	]);
}

return view.extend({
	load: fetch,

	render: function (data) {
		var status = data[0];

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

		// Refresh on a timer rather than requiring a page reload. Five seconds
		// is frequent enough to feel live and slow enough to stay invisible on
		// the router's CPU.
		poll.add(function () {
			return fetch().then(function (fresh) {
				if (!fresh[0])
					return;

				// rates() differences against the previous reading, so it has
				// to run before remember() replaces it.
				var speed = rates(fresh[0]);
				dom.content(overview, overviewChildren(fresh[0], speed));
				dom.content(tunnels, tunnelsChildren(fresh[0].tunnels, speed));
				remember(fresh[0]);
				if (fresh[1] !== logBox.textContent)
					dom.content(logBox, fresh[1] || _('No log output yet.'));
			});
		}, 5);

		return E('div', {}, [
			E('h2', {}, 'OpenFrp'),
			overview,
			tunnels,
			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('Recent log')),
				E('p', { 'class': 'cbi-section-descr' },
					_('The daemon writes to syslog through procd.')),
				logBox
			])
		]);
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
