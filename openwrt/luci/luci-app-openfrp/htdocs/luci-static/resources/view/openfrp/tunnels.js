'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';
'require dom';
'require poll';

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

function requestCertificate(view, section_id, domains, accounts, lastEmail) {
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

	var closeButton = E('button', { 'class': 'btn', 'click': function () {
		stopFollowing();
		ui.hideModal();
	} }, _('Close'));

	var requestButton = E('button',
		{ 'class': 'btn cbi-button-positive', 'click': start }, _('Request'));

	var following = null;

	function stopFollowing() {
		if (following) {
			poll.remove(following);
			following = null;
		}
	}

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
		requestButton.disabled = true;

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
			requestButton.disabled = false;
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

				stopFollowing();

				if (res.state !== 'succeeded') {
					statusLine.textContent = _('Failed — see the output above.');
					requestButton.disabled = false;
					return;
				}

				if (requestButton.parentNode)
					requestButton.parentNode.removeChild(requestButton);
				closeButton.classList.add('cbi-button-positive');

				uci.set('openfrp', section_id, 'cert_id', String(orderId));

				statusLine.textContent = _('Issued. Deploying it to the server…');

				uci.save().then(function () {
					return uci.apply();
				}).then(function () {
					statusLine.textContent =
						_('Issued, bound, and deployed to the server.');

					if (view && view.reload)
						view.reload();
				}).catch(function (err) {
					statusLine.textContent =
						_('Issued and bound, but applying it failed: %s')
							.format((err && err.message) || err);
				});
			});
		}

		following = tick;
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
		E('div', { 'class': 'right', 'style': 'margin-top:1em' },
			[closeButton, ' ', requestButton])
	]);
}

function issuedCertificates() {
	return callCert('orders', {}, '').then(function (res) {
		if (!res || res.error || res.ok === false)
			return [];
		return (res.data || []).filter(function (order) {
			return order.state === 'issued';
		});
	}).catch(function () { return []; });
}

function validateDomainPattern(section_id, value) {
	if (!value || value === '')
		return true;

	if (value === '*')
		return true; 

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

function publishedByCloudflare(section_id) {
	var owner = uci.get('openfrp', section_id, 'server');
	if (!owner) {
		uci.sections('openfrp', 'server', function (server) {
			if (!owner)
				owner = server['.name'];
		});
	}
	return owner ? uci.get('openfrp', owner, 'kind') === 'cloudflare' : false;
}

// showForServers restricts an option to a set of servers.
//
// An option with no dependencies is shown unconditionally, so an empty set has
// to become a condition nothing satisfies rather than no condition at all.
// Left as an empty list, a field meant for one kind of server appears for
// every kind — which is how the Cloudflare prefix showed up on an SSH server.
function showForServers(option, servers) {
	var existing = (option.deps && option.deps.length) ? option.deps : [{}];
	option.deps = [];

	if (!servers.length) {
		option.deps.push({ type: '\u0000none' });
		return option;
	}

	existing.forEach(function (dep) {
		servers.forEach(function (server) {
			var combined = Object.assign({}, dep);
			combined.server = server;
			option.deps.push(combined);
		});
	});
	return option;
}

function limitToOpenFrp(option, allowed) {
	return showForServers(option, allowed);
}

function openfrpServers() {
	var allowed = [];
	var first = null;

	uci.sections('openfrp', 'server', function (server) {
		if (first === null)
			first = server;
		if (server.kind !== 'cloudflare')
			allowed.push(server['.name']);
	});

	if (first && first.kind !== 'cloudflare')
		allowed.push('');
	return allowed;
}

function cloudflareServers() {
	var allowed = [];
	var first = null;

	uci.sections('openfrp', 'server', function (server) {
		if (first === null)
			first = server;
		if (server.kind === 'cloudflare')
			allowed.push(server['.name']);
	});

	if (first && first.kind === 'cloudflare')
		allowed.push('');
	return allowed;
}

function zoneOf(section_id) {
	var owner = uci.get('openfrp', section_id, 'server');
	if (!owner) {
		uci.sections('openfrp', 'server', function (server) {
			if (!owner)
				owner = server['.name'];
		});
	}
	var zone = owner ? uci.get('openfrp', owner, 'zone') : '';
	if (zone)
		return zone;

	var zones = [];
	uci.sections('openfrp', 'server', function (server) {
		if (server.kind === 'cloudflare' && server.zone)
			zones.push(server.zone);
	});
	return zones.length === 1 ? zones[0] : '';
}

function publishedNames(section_id) {
	if (!publishedByCloudflare(section_id))
		return L.toArray(uci.get('openfrp', section_id, 'domains'));

	var zone = zoneOf(section_id);
	if (!zone)
		return [];

	return L.toArray(uci.get('openfrp', section_id, 'cf_prefix'))
		.map(function (prefix) {
			return prefix === '@' ? zone : prefix + '.' + zone;
		});
}

function certificateCovers(san, domain) {
	san = String(san || '').toLowerCase();
	domain = String(domain || '').toLowerCase();

	if (!san || !domain)
		return false;
	if (san === domain)
		return true;
	if (san.indexOf('*.') !== 0 || domain.indexOf('*') === 0)
		return false;

	var suffix = san.slice(1);
	if (domain.length <= suffix.length ||
	    domain.slice(domain.length - suffix.length) !== suffix)
		return false;

	return domain.slice(0, domain.length - suffix.length).indexOf('.') < 0;
}

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

	reload: function () {
		var self = this;
		uci.unload('openfrp');
		return self.load().then(function (data) {
			return self.render(data).then(function (node) {
				var container = document.querySelector('#view');
				if (container)
					dom.content(container, node);
			});
		});
	},

	render: function (data) {
		var m, s, o;
		var self = this;
		var certificates = data[1] || [];
		var accounts = data[2] || [];

		var lastEmail = (certificates[0] || {}).email || '';

		var openfrp = openfrpServers();
		var cloudflare = cloudflareServers();

		m = new form.Map('openfrp', _('Tunnels'),
			_('Reach a service on this network from the internet.'));

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

		o.default = 'http';
		o.validate = function (section_id, value) {
			if (value !== 'http' && value !== 'https' &&
			    publishedByCloudflare(section_id))
				return _('A Cloudflare tunnel publishes over HTTP. Use a server of your own for %s.').format(value);
			return true;
		};
		o.cfgvalue = function (section_id) {
			var stored = uci.get('openfrp', section_id, 'type');
			return stored === 'https' ? 'http' : (stored || 'tcp');
		};

		o = s.option(form.Flag, 'https', _('Serve HTTPS'),
			_('Adds HTTPS on port 443. The name stays reachable over plain ' +
			  'HTTP as well.'));
		o.depends('type', 'http');
		o.default = '0';
		limitToOpenFrp(o, openfrp);
		o.cfgvalue = function (section_id) {
			var stored = uci.get('openfrp', section_id, 'https');
			if (stored === '1' || stored === '0')
				return stored;

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
		limitToOpenFrp(o, openfrp);

		o = s.option(form.DynamicList, 'domains', _('Domains'),
			_('Routed to this tunnel. Wildcards are supported.'));
		o.depends('type', 'http');
		o.depends('type', 'https');
		o.placeholder = '*.aaa.com';
		o.validate = validateDomainPattern;
		limitToOpenFrp(o, openfrp);
		o.textvalue = function (section_id) {
			var names = publishedNames(section_id);
			if (!names.length)
				return null;

			return E('span', {}, names.map(function (name) {
				return E('div', {}, name);
			}));
		};

		o = s.option(form.DynamicList, 'cf_prefix', _('Prefix'));
		o.modalonly = true;
		o.placeholder = 'nas';
		o.depends('type', 'http');
		showForServers(o, cloudflare);
		o.validate = function (section_id, value) {
			if (value && !/^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$/.test(value))
				return _('One label only: letters, digits and dashes.');
			return true;
		};
		o.renderWidget = function (section_id) {
			var zone = zoneOf(section_id);
			this.description = zone
				? _('Each is published with .%s after it. Use @ for %s itself.')
					.format(zone, zone)
				: _('This server has no domain yet — set it up again.');
			return form.DynamicList.prototype.renderWidget.apply(this, arguments);
		};

		o = s.option(form.Button, '_certificate', _('Certificate'));
		o.modalonly = false;

		o.editable = true;
		o.inputtitle = _('Request a certificate');
		o.inputstyle = 'apply';

		o.cfgvalue = function (section_id) {
			var type = uci.get('openfrp', section_id, 'type');
			var https = uci.get('openfrp', section_id, 'https') === '1' ||
				type === 'https';
			var mode = uci.get('openfrp', section_id, 'tls_mode') || 'terminate';
			var bound = uci.get('openfrp', section_id, 'cert_id');

			if (type !== 'http' && type !== 'https')
				return false;

			if (publishedByCloudflare(section_id))
				return false;
			if (!https || mode !== 'terminate' || bound)
				return false;
			return '';
		};

		o.parse = function () {
			return Promise.resolve();
		};

		o.renderWidget = function (section_id, option_index, cfgvalue) {
			var bound = uci.get('openfrp', section_id, 'cert_id');
			if (bound) {
				var match = certificates.filter(function (order) {
					return String(order.id) === String(bound);
				})[0];
				return E('span', {},
					match ? match.domains.join(', ')
						: _('Certificate %s (missing)').format(bound));
			}
			return form.Button.prototype.renderWidget.apply(this, arguments);
		};
		o.onclick = function (ev, section_id) {
			var domains = L.toArray(uci.get('openfrp', section_id, 'domains'));
			if (!domains.length) {
				ui.addNotification(null, E('p', {},
					_('Add a domain to this tunnel first.')), 'warning');
				return false;
			}
			requestCertificate(self, section_id, domains, accounts, lastEmail);
			return false;
		};

		o = s.option(form.Value, 'down_rate', _('Download limit'));
		o.datatype = 'uinteger';
		o.placeholder = '0';
		o.modalonly = true;
		o.description = _('Kilobytes per second toward visitors. 0 for no limit.');

		o = s.option(form.Value, 'up_rate', _('Upload limit'));
		o.datatype = 'uinteger';
		o.placeholder = '0';
		o.modalonly = true;
		o.description = _('Kilobytes per second from visitors. 0 for no limit.');

		o = s.option(form.Value, 'quota', _('Traffic cap'));
		o.datatype = 'uinteger';
		o.placeholder = '0';
		o.modalonly = true;
		o.description = _('Megabytes in total, both directions. The tunnel stops accepting connections once reached. 0 for no cap.');

		o = s.option(form.ListValue, 'tls_mode', _('TLS handling'));
		o.depends({ type: 'http', https: '1' });
		limitToOpenFrp(o, openfrp);
		o.value('passthrough', _('The local service handles HTTPS'));
		o.value('terminate', _('The remote server handles HTTPS'));
		o.default = 'passthrough';
		o.modalonly = true;
		o.description = _('Whichever end handles HTTPS needs the certificate. The server\'s is issued here and pushed to it.');

		o = s.option(form.ListValue, 'cert_id', _('Certificate'),
			_('Only certificates covering every domain are listed. Pushed without dropping connections.'));
		o.depends({ type: 'http', https: '1', tls_mode: 'terminate' });
		o.modalonly = true;
		limitToOpenFrp(o, openfrp);

		o.renderWidget = function (section_id, option_index, cfgvalue) {
			var domains = L.toArray(uci.get('openfrp', section_id, 'domains'));
			var bound = uci.get('openfrp', section_id, 'cert_id');

			this.keylist = [];
			this.vallist = [];
			this.value('', _('None — TLS will not work until one is bound'));

			certificates.forEach(function (order) {
				var fits = domains.length && domains.every(function (domain) {
					return (order.domains || []).some(function (san) {
						return certificateCovers(san, domain);
					});
				});

				if (!fits && String(order.id) !== String(bound))
					return;

				var label = order.domains.join(', ') +
					' (' + order.ca_label + ', ' +
					_('%d days left').format(order.days_remaining) + ')';
				if (!fits)
					label += ' — ' + _('does not cover this tunnel');

				this.value(String(order.id), label);
			}, this);

			return form.ListValue.prototype.renderWidget.apply(this, arguments);
		};

		if (!certificates.length)
			o.description = _('No certificates have been issued yet. Request one ' +
				'on the Certificates page first.');

		o = s.option(form.ListValue, 'proxy_protocol', _('Client IP'));
		o.modalonly = true;
		o.value('', _('Not announced'));
		o.value('v1', _('PROXY protocol v1 (text)'));
		o.value('v2', _('PROXY protocol v2 (binary)'));
		o.depends('type', 'tcp');
		o.depends('type', 'http');
		o.depends('type', 'https');
		limitToOpenFrp(o, openfrp);

		o.description = _('Without it the local service records every visitor as this router. Configure the service first, or every request fails.\n\nnginx:\n    listen PORT proxy_protocol;\n    set_real_ip_from THIS-ROUTER-LAN-ADDRESS;\n    real_ip_header proxy_protocol;');

		return m.render().then(function (node) {
			return E('div', {}, [stylesheet(), node]);
		});
	}
});
