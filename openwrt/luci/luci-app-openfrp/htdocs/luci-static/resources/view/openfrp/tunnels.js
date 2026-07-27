'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';
'require dom';
'require poll';

/*
 * Tunnel configuration.
 *
 * Domain patterns are validated here as well as in the daemon. The daemon is
 * the authority, but a rejected pattern there surfaces as a log line the user
 * has to go looking for, whereas catching it in the form points at the field.
 *
 * A tunnel that terminates TLS names the certificate it uses. Only a bound
 * tunnel has its certificate pushed to the server: with several certificates
 * on file, picking one automatically would sooner or later serve the wrong
 * name, which a browser reports to the visitor as an impersonation attempt.
 */

var callCert = rpc.declare({
	object: 'luci.openfrp',
	method: 'cert',
	params: ['action', 'params', 'payload'],
	expect: {}
});

var callDNS = rpc.declare({
	object: 'luci.openfrp',
	method: 'dns',
	params: ['action', 'params', 'payload'],
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

// certCall unwraps the management envelope.
function certCall(action, params, payload) {
	return callCert(action, params || {}, payload ? JSON.stringify(payload) : '')
		.then(function (res) {
			if (!res || res.error)
				return Promise.reject(new Error((res && res.error) || _('no response')));
			if (res.ok === false)
				return Promise.reject(new Error(res.error || _('the request failed')));
			return res.data;
		});
}

// requestCertificate obtains a certificate for one tunnel and binds it.
//
// The tunnel already says which names it serves, so the only things left to
// decide are the contact address the authority requires and, for a wildcard,
// which DNS account proves it. Everything else is inferred: a previous order's
// address is reused, and a single name is validated over HTTP, which needs no
// credentials at all.
function requestCertificate(section_id, domains, accounts, lastEmail) {
	var wildcard = domains.some(function (d) { return d.indexOf('*') === 0; });

	var emailInput = E('input', {
		'type': 'text', 'class': 'cbi-input-text', 'style': 'width:100%',
		'value': lastEmail || '', 'placeholder': 'ops@example.com'
	});

	var accountSelect = E('select', { 'class': 'cbi-input-select' },
		(wildcard ? [] : [E('option', { 'value': '' }, _('None — validate over HTTP'))])
			.concat(accounts.map(function (a) {
				return E('option', { 'value': String(a.id) },
					a.name + ' (' + a.provider_label + ')');
			})));

	function field(label, control, help) {
		var children = [control];
		if (help)
			children.push(E('div', { 'class': 'cbi-value-description' }, help));
		return E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, label),
			E('div', { 'class': 'cbi-value-field' }, children)
		]);
	}

	var output = E('pre', {
		'style': 'display:none;max-height:20em;overflow:auto;white-space:pre-wrap;' +
			'font-size:90%;background:#1e1e1e;color:#ddd;padding:0.6em;border-radius:3px'
	}, '');
	var statusLine = E('p', {}, _('Covers: %s').format(domains.join(', ')));

	function start() {
		if (!emailInput.value) {
			ui.addNotification(null,
				E('p', {}, _('Enter a contact email address.')), 'warning');
			return;
		}
		if (wildcard && !accountSelect.value) {
			ui.addNotification(null, E('p', {},
				_('A wildcard can only be proved through DNS. Select an account.')),
				'warning');
			return;
		}

		statusLine.textContent = _('Requesting…');
		output.style.display = '';

		certCall('order-add', {}, {
			domains: domains,
			key_type: 'ec256',
			ca: 'letsencrypt',
			email: emailInput.value,
			account_id: parseInt(accountSelect.value, 10) || 0,
			auto_renew: true
		}).then(function (order) {
			return callJobStart('cert_issue', JSON.stringify({ id: order.id }))
				.then(function (res) {
					if (!res || res.error || !res.id)
						throw new Error((res && res.error) || _('no response'));
					follow(res.id, order.id);
				});
		}).catch(function (err) {
			statusLine.textContent = err.message;
		});
	}

	var offset = 0;

	function follow(jobId, orderId) {
		function tick() {
			return callJobStatus(jobId, offset).then(function (res) {
				if (!res || res.error)
					return;

				if (res.log && res.log.length) {
					if (offset === 0)
						dom.content(output, res.log);
					else
						output.appendChild(document.createTextNode(res.log));
					output.scrollTop = output.scrollHeight;
				}
				offset = res.offset || offset;

				if (res.state === 'running')
					return;

				poll.remove(tick);

				if (res.state !== 'succeeded') {
					statusLine.textContent = _('Failed — see the output above.');
					return;
				}

				// Bind it here rather than making the operator go and pick it:
				// this certificate was requested for this tunnel and nothing
				// else, so leaving them unlinked would be busywork with a
				// chance of choosing the wrong one.
				uci.set('openfrp', section_id, 'cert_id', String(orderId));
				uci.save().then(function () {
					statusLine.textContent =
						_('Issued and bound to this tunnel. Save and apply to use it.');
				});
			});
		}

		poll.add(tick, 2);
		tick();
	}

	ui.showModal(_('Request a certificate'), [
		statusLine,
		field(_('Contact email'), emailInput,
			_('The authority requires one, and uses it for expiry warnings.')),
		field(_('DNS account'), accountSelect, wildcard
			? _('A wildcard can only be proved through DNS.')
			: _('Leave unset to prove the name by serving a file, which the ' +
			    'tunnel server answers for you.')),
		output,
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			E('button', { 'class': 'btn', 'click': function () {
				poll.remove(); ui.hideModal();
			} }, _('Close')), ' ',
			E('button', { 'class': 'btn cbi-button-positive', 'click': start },
				_('Request'))
		])
	]);
}

// issuedCertificates lists what a tunnel may be bound to. Only issued ones:
// binding to an order that has never been issued would produce a tunnel that
// looks configured and cannot serve TLS.
function issuedCertificates() {
	return callCert('orders', {}, '').then(function (res) {
		if (!res || res.error || res.ok === false)
			return [];
		return (res.data || []).filter(function (order) {
			return order.state === 'issued';
		});
	}).catch(function () { return []; });
}

// A "*" label matches exactly one level and may appear only leftmost.
function validateDomainPattern(section_id, value) {
	if (!value || value === '')
		return true;

	if (value === '*')
		return true; // the catch-all

	var labels = value.split('.');

	if (labels.length < 2)
		return _('A domain needs at least two labels, for example aaa.com');

	for (var i = 0; i < labels.length; i++) {
		var label = labels[i];

		if (label === '')
			return _('Empty label in %s').format(value);

		if (label === '*') {
			if (i !== 0)
				return _('"*" is only allowed as the leftmost label');
			continue;
		}

		if (label.indexOf('*') !== -1)
			return _('"*" must be a whole label, not part of one');
	}

	if (labels[0] === '*' && labels.length < 3)
		return _('%s is too broad; a wildcard needs a domain beneath it').format(value);

	return true;
}

// stylesheet returns the app's shared presentation, loaded as part of the view
// so it applies while one of these pages is open and nowhere else. Dialogs
// render outside this node, but the link is in the document for as long as the
// page is, so they pick it up too.
function stylesheet() {
	return E('link', {
		'rel': 'stylesheet',
		'href': L.resource('openfrp/openfrp.css')
	});
}

return view.extend({
	load: function () {
		return Promise.all([
			uci.load('openfrp'),
			issuedCertificates(),
			callDNS('accounts', {}, '').then(function (res) {
				return (res && res.ok !== false && res.data) || [];
			}).catch(function () { return []; })
		]);
	},

	render: function (data) {
		var m, s, o;
		var certificates = data[1] || [];
		var accounts = data[2] || [];

		// Reuse the contact address from a previous order rather than asking
		// for it again; it is the same person every time.
		var lastEmail = (certificates[0] || {}).email || '';

		m = new form.Map('openfrp', _('Tunnels'),
			_('Each tunnel exposes one local service through the server.') + ' ' +
			_('A "*" label matches exactly one level and may appear at any depth: ' +
			  '*.aaa.com matches www.aaa.com but not x.bb.aaa.com. ' +
			  'Exact names win over wildcards, and deeper wildcards win over shallower ones.'));

		s = m.section(form.GridSection, 'tunnel', null);
		s.addremove = true;
		s.anonymous = true;
		s.sortable = true;
		s.nodescriptions = true;

		s.modaltitle = function (section_id) {
			return _('Tunnel') + ' » ' + (uci.get('openfrp', section_id, 'name') || _('unnamed'));
		};

		o = s.option(form.Flag, 'enabled', _('Enabled'));
		o.editable = true;
		o.default = '0';

		o = s.option(form.Button, '_certificate', _('Certificate'));
		o.modalonly = false;
		// A grid cell is plain text unless the option says it is editable, so
		// without this the column renders the button's value instead of the
		// button — which is to say, nothing.
		o.editable = true;
		o.inputtitle = _('Request a certificate');
		o.inputstyle = 'apply';
		o.depends({ type: 'http', https: '1' });
		o.cfgvalue = function (section_id) {
			// Only offered where it is the missing piece: an HTTPS tunnel that
			// terminates TLS and has nothing bound cannot serve a request.
			var https = uci.get('openfrp', section_id, 'https') === '1' ||
				uci.get('openfrp', section_id, 'type') === 'https';
			var mode = uci.get('openfrp', section_id, 'tls_mode') || 'terminate';
			var bound = uci.get('openfrp', section_id, 'cert_id');

			if (!https || mode !== 'terminate' || bound)
				return false;
			return '';
		};
		// The column is a control, not a setting. Being editable puts it in
		// front of the parser, which would otherwise write the button's own
		// value out as a tunnel option named _certificate.
		o.parse = function () {
			return Promise.resolve();
		};
		o.onclick = function (ev, section_id) {
			var domains = L.toArray(uci.get('openfrp', section_id, 'domains'));
			if (!domains.length) {
				ui.addNotification(null, E('p', {},
					_('Add a domain to this tunnel first.')), 'warning');
				return false;
			}
			requestCertificate(section_id, domains, accounts, lastEmail);
			return false;
		};

		o = s.option(form.Value, 'name', _('Name'));
		o.rmempty = false;
		o.placeholder = 'nas-web';
		o.validate = function (section_id, value) {
			if (!value)
				return _('A name is required');
			if (!/^[A-Za-z0-9._-]+$/.test(value))
				return _('Use letters, digits, dot, dash or underscore only');

			// Names identify a tunnel to the server, so they have to be unique.
			var clash = null;
			uci.sections('openfrp', 'tunnel', function (section) {
				if (section['.name'] !== section_id && section.name === value)
					clash = section['.name'];
			});
			if (clash)
				return _('Another tunnel already uses the name %s').format(value);

			return true;
		};

		o = s.option(form.ListValue, 'server', _('Server'),
			_('Which server publishes this tunnel. Leave unset for the first one.'));
		o.value('', _('The first server'));
		uci.sections('openfrp', 'server', function (server) {
			var addr = server.addr || _('no address');
			o.value(server['.name'], server['.name'] + ' (' + addr + ')');
		});

		// HTTP and HTTPS are one kind here. A tunnel is HTTP, and HTTPS is
		// something you turn on for it — which is how an operator thinks about
		// a website, and it avoids the question of what happens to port 80
		// when you pick the other one. The daemon's two types are resolved
		// when the configuration is rendered.
		o = s.option(form.ListValue, 'type', _('Type'));
		o.value('tcp', 'TCP');
		o.value('udp', 'UDP');
		o.value('http', 'HTTP');
		o.value('stcp', _('Secret TCP'));
		o.default = 'tcp';
		o.cfgvalue = function (section_id) {
			// An existing https tunnel reads back as http with HTTPS on.
			var stored = uci.get('openfrp', section_id, 'type');
			return stored === 'https' ? 'http' : (stored || 'tcp');
		};

		o = s.option(form.Flag, 'https', _('Serve HTTPS'),
			_('Adds HTTPS on port 443. The name stays reachable over plain ' +
			  'HTTP as well.'));
		o.depends('type', 'http');
		o.default = '0';
		o.cfgvalue = function (section_id) {
			if (uci.get('openfrp', section_id, 'https') !== null)
				return uci.get('openfrp', section_id, 'https');
			// A tunnel written before the two were merged.
			return uci.get('openfrp', section_id, 'type') === 'https' ? '1' : '0';
		};

		o = s.option(form.Value, 'local_ip', _('Local address'),
			_('The LAN host running the service.'));
		o.datatype = 'or(host,ipaddr)';
		o.default = '127.0.0.1';
		o.placeholder = '192.168.1.100';

		o = s.option(form.Value, 'local_port', _('Local port'));
		o.datatype = 'port';
		o.rmempty = false;

		o = s.option(form.Value, 'remote_port', _('Remote port'),
			_('Leave empty to let the server allocate one.'));
		o.datatype = 'port';
		o.depends('type', 'tcp');
		o.depends('type', 'udp');

		o = s.option(form.DynamicList, 'domains', _('Domains'),
			_('Patterns routed to this tunnel over the shared HTTP and HTTPS ports.'));
		o.depends('type', 'http');
		o.depends('type', 'https');
		o.placeholder = '*.aaa.com';
		o.validate = validateDomainPattern;

		o = s.option(form.ListValue, 'tls_mode', _('TLS handling'));
		o.depends({ type: 'http', https: '1' });
		o.value('passthrough', _('Passthrough — the server does not decrypt'));
		o.value('terminate', _('Terminate at the server (needs a pushed certificate)'));
		o.default = 'passthrough';
		o.description = _('Passthrough forwards the encrypted stream untouched, so the ' +
			'local service owns the certificate. Termination requires a certificate ' +
			'issued here and pushed to the server.');

		o = s.option(form.ListValue, 'cert_id', _('Certificate'),
			_('Pushed to the server and hot-loaded, without dropping a ' +
			  'connection. Only a bound tunnel has its certificate pushed.'));
		o.depends({ type: 'http', https: '1', tls_mode: 'terminate' });
		o.modalonly = true;
		o.value('', _('None — TLS will not work until one is bound'));

		certificates.forEach(function (order) {
			// The expiry is worth showing at the point of choosing: two
			// certificates often cover the same names and only one is current.
			o.value(String(order.id), order.domains.join(', ') +
				' (' + order.ca_label + ', ' +
				_('%d days left').format(order.days_remaining) + ')');
		});

		if (!certificates.length)
			o.description = _('No certificates have been issued yet. Request one ' +
				'on the Certificates page first.');

		o = s.option(form.ListValue, 'proxy_protocol', _('Client IP'),
			_('Without this the local service records every visitor as this ' +
			  'router, because that is what connects to it.'));
		o.modalonly = true;
		o.value('', _('Not announced'));
		o.value('v1', _('PROXY protocol v1 (text)'));
		o.value('v2', _('PROXY protocol v2 (binary)'));
		o.depends('type', 'tcp');
		o.depends('type', 'http');
		o.depends('type', 'https');
		o.depends('type', 'stcp');
		o.description = _('Without this the local service records every visitor as ' +
			'this router, because that is what connects to it. The service must be ' +
			'configured to expect the header — it arrives where a request is ' +
			'expected, so one that is not looking for it will refuse the connection. ' +
			'For nginx: listen 80 proxy_protocol; set_real_ip_from <this router>; ' +
			'real_ip_header proxy_protocol;');

		o = s.option(form.Value, 'secret_key', _('Secret key'),
			_('Visitors must present this to reach the tunnel.'));
		o.depends('type', 'stcp');
		o.modalonly = true;
		o.password = true;

		// The map renders one node; the stylesheet rides along with it.
		return m.render().then(function (node) {
			return E('div', {}, [stylesheet(), node]);
		});
	}
});
