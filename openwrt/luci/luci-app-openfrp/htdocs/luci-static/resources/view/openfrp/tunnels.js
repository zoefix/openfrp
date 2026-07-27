'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';

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

return view.extend({
	load: function () {
		return Promise.all([
			uci.load('openfrp'),
			issuedCertificates()
		]);
	},

	render: function (data) {
		var m, s, o;
		var certificates = data[1] || [];

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

		o = s.option(form.ListValue, 'type', _('Type'));
		o.value('tcp', 'TCP');
		o.value('udp', 'UDP');
		o.value('http', 'HTTP');
		o.value('https', 'HTTPS');
		o.value('stcp', _('Secret TCP'));
		o.default = 'tcp';

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
		o.depends('type', 'https');
		o.value('passthrough', _('Passthrough — the server does not decrypt'));
		o.value('terminate', _('Terminate at the server (needs a pushed certificate)'));
		o.default = 'passthrough';
		o.description = _('Passthrough forwards the encrypted stream untouched, so the ' +
			'local service owns the certificate. Termination requires a certificate ' +
			'issued here and pushed to the server.');

		o = s.option(form.ListValue, 'cert_id', _('Certificate'),
			_('Pushed to the server and hot-loaded, without dropping a ' +
			  'connection. Only a bound tunnel has its certificate pushed.'));
		o.depends('tls_mode', 'terminate');
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
		o.password = true;

		return m.render();
	}
});
