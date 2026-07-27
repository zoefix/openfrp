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
function overviewChildren(status) {
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

	return [
		E('h3', {}, _('Overview')),
		E('table', { 'class': 'table' }, [
			infoRow(_('Service'), serviceState),
			infoRow(_('Server'), server),
			infoRow(_('Transport'), transport)
		])
	];
}

// Returns the children of the tunnels section.
function tunnelsChildren(tunnels) {
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

		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td' }, tunnel.name),
			E('td', { 'class': 'td' }, tunnel.type),
			E('td', { 'class': 'td' }, tunnel.local),
			E('td', { 'class': 'td' }, exposed),
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

		var overview = E('div', { 'class': 'cbi-section' }, overviewChildren(status));
		var tunnels = E('div', { 'class': 'cbi-section' }, tunnelsChildren(status.tunnels));

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
				dom.content(overview, overviewChildren(fresh[0]));
				dom.content(tunnels, tunnelsChildren(fresh[0].tunnels));
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
