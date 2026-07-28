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

	// The poller has to be stopped by identity, and only the run that started
	// it knows which function that is. Calling poll.remove() with nothing is a
	// TypeError, and it threw before hiding the modal — leaving a dialog whose
	// close button did nothing at all.
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

				// There is nothing left to request. Leaving the button would
				// invite a second click that orders another certificate for
				// the same name — which the authority rate-limits, and which
				// would replace a binding that is already correct.
				if (requestButton.parentNode)
					requestButton.parentNode.removeChild(requestButton);
				closeButton.classList.add('cbi-button-positive');

				// Bind it here rather than making the operator go and pick it:
				// this certificate was requested for this tunnel and nothing
				// else, so leaving them unlinked would be busywork with a
				// chance of choosing the wrong one.
				uci.set('openfrp', section_id, 'cert_id', String(orderId));

				// Issued is not the same as serving. The server is handed a
				// certificate by the daemon, and the daemon only knows which
				// tunnel uses which one after it has reloaded — so a binding
				// left staged is a certificate that exists and protects
				// nothing. Applying is the rest of the one click.
				statusLine.textContent = _('Issued. Deploying it to the server…');

				uci.save().then(function () {
					return uci.apply();
				}).then(function () {
					statusLine.textContent =
						_('Issued, bound, and deployed to the server.');
					// The row still offers to request one until it re-reads
					// the binding that now exists.
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

// publishedByCloudflare reports whether a tunnel is carried by Cloudflare.
//
// A tunnel that names no server belongs to the first one, which is the rule
// the daemon applies, so the same rule has to be applied here or the form
// would offer settings for a server the tunnel does not use.
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

// limitToOpenFrp hides an option unless the tunnel names an OpenFrp server.
//
// Expressed as a dependency rather than a check at render time, because the
// server is chosen after the form is drawn — a check that ran once, while the
// field was still empty, decided every option was applicable and then never
// looked again. That is why a Cloudflare tunnel was still being offered a
// remote port and the PROXY protocol.
//
// Cloudflare terminates TLS at its edge, publishes hostnames rather than
// ports, and passes the visitor's address in CF-Connecting-IP without being
// asked. So a certificate, a TLS mode, a remote port and the PROXY protocol
// are settings it would accept, save, and then ignore.
function limitToOpenFrp(option, allowed) {
	// Dependencies are ANDed within one entry and ORed across entries, so an
	// option that already has some is multiplied by the servers it may appear
	// for rather than having one appended to it.
	var existing = (option.deps && option.deps.length) ? option.deps : [{}];
	option.deps = [];

	existing.forEach(function (dep) {
		allowed.forEach(function (server) {
			var combined = Object.assign({}, dep);
			combined.server = server;
			option.deps.push(combined);
		});
	});
	return option;
}

// openfrpServers lists the values of the server field that are not Cloudflare.
//
// The empty value means the first server, which is the rule the daemon
// applies, so it belongs on the list only when that first one is an OpenFrp
// server.
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

// cloudflareServers lists the values of the server field that mean Cloudflare.
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

// zoneOf is the domain a Cloudflare server publishes under.
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

	// A tunnel being added has no server recorded yet — the choice is in the
	// form, not in the configuration — so the fallback is the Cloudflare
	// server itself. With one, which is the ordinary case, that is the answer.
	var zones = [];
	uci.sections('openfrp', 'server', function (server) {
		if (server.kind === 'cloudflare' && server.zone)
			zones.push(server.zone);
	});
	return zones.length === 1 ? zones[0] : '';
}

// certificateCovers reports whether one of a certificate's names serves a
// tunnel's domain.
//
// A certificate wildcard stands for exactly one label and only at the front —
// that is what an authority will issue and what a browser will accept — so
// *.example.com serves www.example.com and nothing deeper. A tunnel serving a
// wildcard needs a certificate for that same wildcard: one issued for a single
// name underneath it leaves every other name unserved.
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

	// reload re-reads UCI and re-renders. Issuing a certificate binds it to a
	// tunnel, so the row that offered to request one is out of date the moment
	// it succeeds — and left alone it goes on offering, which is an invitation
	// to order a duplicate the authority will refuse.
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

		// Reuse the contact address from a previous order rather than asking
		// for it again; it is the same person every time.
		var lastEmail = (certificates[0] || {}).email || '';

		// Which values of the server field mean an OpenFrp server. Computed
		// once here and handed to every option that only applies to one.
		var openfrp = openfrpServers();
		var cloudflare = cloudflareServers();

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
		// HTTP by default. It is what a Cloudflare tunnel can publish at all,
		// and what most of the rest are: a port forward is the exception.
		o.default = 'http';
		o.validate = function (section_id, value) {
			if (value !== 'http' && value !== 'https' &&
			    publishedByCloudflare(section_id))
				return _('A Cloudflare tunnel publishes hostnames over HTTP. ' +
					'Point this tunnel at a server of your own for %s.').format(value);
			return true;
		};
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
		limitToOpenFrp(o, openfrp);
		o.cfgvalue = function (section_id) {
			// Tested against what a stored flag looks like rather than against
			// null: an option that was never written reads back as undefined,
			// so a null check accepts it and the tunnel below reads as plain
			// HTTP — which then overwrites its own type on the next save.
			var stored = uci.get('openfrp', section_id, 'https');
			if (stored === '1' || stored === '0')
				return stored;
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
		limitToOpenFrp(o, openfrp);

		o = s.option(form.DynamicList, 'domains', _('Domains'),
			_('Patterns routed to this tunnel over the shared HTTP and HTTPS ports.'));
		o.depends('type', 'http');
		o.depends('type', 'https');
		o.placeholder = '*.aaa.com';
		o.validate = validateDomainPattern;
		limitToOpenFrp(o, openfrp);

		// A Cloudflare tunnel names a prefix. The suffix is the domain chosen
		// while authorising, which is already known — asking for it again per
		// tunnel is asking someone to retype a decision, and a typo there
		// publishes a name that resolves nowhere.
		// A list, like the domains for an OpenFrp server: one tunnel commonly
		// serves a service under more than one name, and there is no reason
		// Cloudflare should be the exception.
		o = s.option(form.DynamicList, 'cf_prefix', _('Names under the domain'));
		o.placeholder = 'nas';
		cloudflare.forEach(function (server) {
			o.depends({ type: 'http', server: server });
		});
		o.validate = function (section_id, value) {
			if (value && !/^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$/.test(value))
				return _('One label only: letters, digits and dashes.');
			return true;
		};
		o.renderWidget = function (section_id) {
			// The suffix is shown beside the field rather than described, so
			// what will be published is on screen while it is being typed.
			var zone = zoneOf(section_id);
			this.description = zone
				? _('Each is published with .%s after it. Use @ for %s itself.')
					.format(zone, zone)
				: _('This server has no domain yet — set it up again.');
			return form.DynamicList.prototype.renderWidget.apply(this, arguments);
		};

		o = s.option(form.Button, '_certificate', _('Certificate'));
		o.modalonly = false;
		// A grid cell is plain text unless the option says it is editable, so
		// without this the column renders the button's value instead of the
		// button — which is to say, nothing.
		o.editable = true;
		o.inputtitle = _('Request a certificate');
		o.inputstyle = 'apply';
		// No depends. A dependency is resolved against the other options'
		// widgets, and in a grid row only an editable option has one — the
		// rest are plain text — so a dependency on the type could never be
		// satisfied here and the button silently never appeared. The whole
		// condition is decided below, against the configuration itself.
		o.cfgvalue = function (section_id) {
			// Only offered where it is the missing piece: an HTTPS tunnel that
			// terminates TLS and has nothing bound cannot serve a request.
			var type = uci.get('openfrp', section_id, 'type');
			var https = uci.get('openfrp', section_id, 'https') === '1' ||
				type === 'https';
			var mode = uci.get('openfrp', section_id, 'tls_mode') || 'terminate';
			var bound = uci.get('openfrp', section_id, 'cert_id');

			if (type !== 'http' && type !== 'https')
				return false;
			// Cloudflare issues and holds the certificate for its own edge.
			if (publishedByCloudflare(section_id))
				return false;
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
		// A bound certificate is shown by name. The button only has something
		// to offer when there is nothing bound, and rendering a dash in every
		// other row left a column headed Certificate that never named one.
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

		o = s.option(form.ListValue, 'tls_mode', _('TLS handling'));
		o.depends({ type: 'http', https: '1' });
		limitToOpenFrp(o, openfrp);
		o.value('passthrough', _('Passthrough — the remote server does not decrypt'));
		o.value('terminate', _('Decrypted by the remote server'));
		o.default = 'passthrough';
		o.description = _('Passthrough forwards the encrypted stream untouched, so ' +
			'the local service owns the certificate. Letting the remote server ' +
			'decrypt needs a certificate issued here and pushed to it.');

		o = s.option(form.ListValue, 'cert_id', _('Certificate'),
			_('Only certificates covering every domain of this tunnel are ' +
			  'listed. Pushed to the server and hot-loaded, without dropping a ' +
			  'connection. Only a bound tunnel has its certificate pushed.'));
		o.depends({ type: 'http', https: '1', tls_mode: 'terminate' });
		o.modalonly = true;
		limitToOpenFrp(o, openfrp);

		// The choices depend on the tunnel, so they are built per section
		// rather than once. Offering a certificate that does not cover this
		// tunnel's names is worse than offering nothing: it produces a tunnel
		// that looks configured and answers with the wrong name, which a
		// browser reports to the visitor as an impersonation attempt.
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

				// A certificate already bound stays on the list even when it
				// does not fit, because a select whose value is not among its
				// options shows the first one instead — and then saving the
				// form quietly rebinds the tunnel to whatever that was.
				if (!fits && String(order.id) !== String(bound))
					return;

				// The expiry is worth showing at the point of choosing: two
				// certificates often cover the same names and only one is
				// current.
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
		o.depends('type', 'stcp');
		// After the dependencies above, not before: this multiplies the ones
		// that exist, and any added afterwards would carry no server
		// constraint at all.
		limitToOpenFrp(o, openfrp);

		// No angle brackets in the placeholders: a description is inserted as
		// markup, so <port> would be parsed as a tag and vanish, leaving a
		// directive that looks complete and is not.
		o.description = _('The local service otherwise records every visitor as ' +
			'this router.\n\n' +
			'Configure the service first: until both ends agree, every request ' +
			'fails.\n\n' +
			'nginx, on the port this tunnel points at:\n' +
			'    listen PORT proxy_protocol;\n' +
			'    set_real_ip_from THIS-ROUTER-LAN-ADDRESS;\n' +
			'    real_ip_header proxy_protocol;');

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
