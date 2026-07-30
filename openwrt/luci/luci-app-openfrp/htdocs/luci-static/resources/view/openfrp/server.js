'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';
'require poll';
'require dom';

var callStatus = rpc.declare({
	object: 'luci.openfrp', method: 'status', expect: {}
});

var callJobStart = rpc.declare({
	object: 'luci.openfrp', method: 'job_start',
	params: ['kind', 'args'], expect: {}
});

var liveStatus = null;

var callJobStatus = rpc.declare({
	object: 'luci.openfrp', method: 'job_status',
	params: ['id', 'offset'], expect: {}
});

var callJobCancel = rpc.declare({
	object: 'luci.openfrp', method: 'job_cancel',
	params: ['id'], expect: {}
});

function button(label, style, onclick) {
	return E('button', { 'class': 'btn ' + (style || ''), 'click': onclick }, label);
}

function row(label, field, help) {
	var children = [field];
	if (help)
		children.push(E('div', { 'class': 'cbi-value-description' }, help));
	return E('div', { 'class': 'cbi-value' }, [
		E('label', { 'class': 'cbi-value-title' }, label),
		E('div', { 'class': 'cbi-value-field' }, children)
	]);
}

function input(attrs, value) {
	return E('input', Object.assign({
		'type': 'text', 'class': 'cbi-input-text', 'style': 'width:100%',
		'value': value || ''
	}, attrs || {}));
}

function showJobModal(title, jobId, onFinished) {
	var offset = 0;
	var output = E('pre', {
		'style': 'max-height:26em;overflow:auto;white-space:pre-wrap;' +
			'font-size:90%;background:#1e1e1e;color:#ddd;padding:0.6em;border-radius:3px'
	}, _('Starting…'));

	var statusLine = E('p', {}, _('Running…'));
	var cancelButton = button(_('Cancel'), 'cbi-button-negative', function () {
		callJobCancel(jobId);
	});
	var closeButton = button(_('Close'), '', function () {
		poll.remove(tick);
		ui.hideModal();
	});

	function tick() {
		return callJobStatus(jobId, offset).then(function (res) {
			if (!res || res.error) {
				statusLine.textContent = _('Lost track of the job: %s')
					.format((res && res.error) || _('no response'));
				poll.remove(tick);
				return;
			}

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
			cancelButton.style.display = 'none';

			if (res.state === 'succeeded') {
				statusLine.textContent = _('Finished successfully.');
				if (onFinished)
					onFinished(true);
			} else if (res.state === 'failed') {
				statusLine.textContent = _('Failed — see the output above.');
				if (onFinished)
					onFinished(false);
			} else {
				statusLine.textContent = _('Job ended in state: %s').format(res.state);
			}
		});
	}

	ui.showModal(title, [
		statusLine, output,
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			cancelButton, ' ', closeButton
		])
	]);

	poll.add(tick, 1);
	tick();
}

function sectionName(label) {
	var name = String(label || '').toLowerCase()
		.replace(/[^a-z0-9_]+/g, '_')
		.replace(/^_+|_+$/g, '');
	return name || 'server';
}

function uniqueSectionName(base) {
	var name = base;
	var suffix = 2;
	while (uci.get('openfrp', name) != null)
		name = base + '_' + (suffix++);
	return name;
}

function radio(group, value, checked, label, help, onchange) {
	var control = E('input', {
		'type': 'radio', 'name': group, 'value': value,
		'class': 'cbi-input-radio',
		'checked': checked ? '' : null
	});
	control.addEventListener('change', onchange);

	return {
		control: control,
		node: E('div', { 'style': 'margin-bottom:0.6em' }, [
			E('label', { 'style': 'display:block;cursor:pointer' }, [
				control, ' ', E('strong', {}, label)
			]),
			E('div', {
				'class': 'cbi-value-description',
				'style': 'margin-left:1.6em'
			}, help)
		])
	};
}

function existingFields() {
	var nameInput = input({ 'placeholder': 'main' });
	var addrInput = input({ 'placeholder': '203.0.113.10' });
	var portInput = input({ 'type': 'number' }, '7000');
	var tokenInput = input({ 'type': 'password', 'class': 'cbi-input-password' });

	return {
		node: E('div', {}, [
			row(_('Name'), nameInput,
				_('Local to this router — a tunnel points at it. The server never sees it.')),
			row(_('Server address'), addrInput),
			row(_('Control port'), portInput),
			row(_('Token'), tokenInput,
				_('The shared secret the server was configured with.'))
		]),

		submit: function (view) {
			if (!addrInput.value) {
				ui.addNotification(null,
					E('p', {}, _('Enter the server address.')), 'warning');
				return;
			}

			var name = uniqueSectionName(sectionName(nameInput.value || addrInput.value));
			uci.add('openfrp', 'server', name);
			uci.set('openfrp', name, 'addr', addrInput.value);
			uci.set('openfrp', name, 'port', portInput.value || '7000');
			uci.set('openfrp', name, 'token', tokenInput.value);

			ui.hideModal();
			view.reload();
		}
	};
}

function cloudflareFields() {
	var nameInput = input({ 'placeholder': 'cloudflare' });

	return {
		node: E('div', {}, [
			row(_('Name'), nameInput,
				_('Local to this router — a tunnel points at it.')),
			E('div', { 'class': 'cbi-value-description' },
				_('No server of your own — cloudflared connects out to Cloudflare, which routes your domains back.\n\nHTTP only: Cloudflare answers HTTPS itself, so certificates, remote ports and the PROXY protocol do not apply.'))
		]),

		focus: function () { nameInput.focus(); },

		submit: function (view) {
			var name = uniqueSectionName(sectionName(nameInput.value || 'cloudflare'));

			uci.add('openfrp', 'server', name);
			uci.set('openfrp', name, 'kind', 'cloudflare');

			uci.save().then(function () {
				ui.hideModal();

				callJobStart('cf_setup', JSON.stringify({
					server: name,
					name: 'openfrp-' + name
				})).then(function (res) {
					if (!res || res.error || !res.id) {
						ui.addNotification(null, E('p', {},
							_('Could not start the setup: %s')
								.format((res && res.error) || _('no response'))), 'error');
						return;
					}

					showJobModal(_('Setting up a Cloudflare tunnel'), res.id,
						function (ok) {
							if (ok)
								view.reload();
						});
				});
			});
		}
	};
}

function deployFields(section) {
	var adding = !section;

	function stored(option, fallback) {
		return (section && uci.get('openfrp', section, option)) || fallback || '';
	}

	var nameInput = input({ 'placeholder': 'main' });
	var hostInput = input({ 'placeholder': '203.0.113.10' }, stored('ssh_host'));
	var portInput = input({ 'type': 'number' }, stored('ssh_port', '22'));
	var userInput = input({}, stored('ssh_user', 'root'));

	var storedPassword = stored('ssh_pass');
	var passwordInput = input({
		'type': 'password', 'class': 'cbi-input-password', 'autocomplete': 'off'
	}, storedPassword);
	var keyInput = input({ 'placeholder': '/etc/openfrp/id_ed25519' }, stored('ssh_key_path'));
	var controlPort = input({ 'type': 'number' }, stored('port', '7000'));

	var authSelect = E('select', { 'class': 'cbi-input-select' }, [
		E('option', { 'value': 'password' }, _('Password')),
		E('option', { 'value': 'key' }, _('Private key'))
	]);
	authSelect.value = stored('ssh_auth', 'password');

	var revealButton = E('button', {
		'class': 'btn',
		'style': 'margin-left:0.5em;white-space:nowrap',
		'click': function (ev) {
			ev.preventDefault();
			var masked = passwordInput.getAttribute('type') === 'password';
			passwordInput.setAttribute('type', masked ? 'text' : 'password');
			ev.currentTarget.textContent = masked ? _('Hide') : _('Show');
		}
	}, _('Show'));

	var passwordField = E('div',
		{ 'style': 'display:flex;align-items:center' },
		[passwordInput, revealButton]);

	var passwordRow = row(_('SSH password'), passwordField,
		_('Saved here in plain text, so an update can deploy without asking.'));
	var keyRow = row(_('Private key'), keyInput,
		_('Path to a key on this router.'));

	function refreshAuth() {
		var usingPassword = authSelect.value === 'password';
		passwordRow.style.display = usingPassword ? '' : 'none';
		keyRow.style.display = usingPassword ? 'none' : '';
	}
	authSelect.addEventListener('change', refreshAuth);
	refreshAuth();

	var rows = [];
	if (adding)
		rows.push(row(_('Name'), nameInput,
			_('Local to this router — a tunnel points at it.')));

	rows = rows.concat([
		row(_('SSH host'), hostInput),
		row(_('SSH port'), portInput),
		row(_('SSH user'), userInput),
		row(_('Authentication'), authSelect),
		passwordRow,
		keyRow,
		row(_('Control port'), controlPort,
			_('The port the server will listen on for this router.')),
		E('p', {}, _('Detects the system, installs the server, opens the firewall and verifies it.'))
	]);

	return {
		node: E('div', {}, rows),
		focus: function () {
			if (authSelect.value === 'password')
				passwordInput.focus();
		},

		submit: function (view) {
			if (!hostInput.value) {
				ui.addNotification(null, E('p', {}, _('Enter the SSH host.')), 'warning');
				return;
			}

			if (authSelect.value === 'password' && !passwordInput.value) {
				ui.addNotification(null, E('p', {}, _('Enter the SSH password.')), 'warning');
				return;
			}

			if (authSelect.value === 'key' && !keyInput.value) {
				ui.addNotification(null, E('p', {},
					_('Enter the path to the SSH key.')), 'warning');
				return;
			}

			var name = section;
			if (adding) {
				name = uniqueSectionName(sectionName(nameInput.value || hostInput.value));
				uci.add('openfrp', 'server', name);
			}

			uci.set('openfrp', name, 'addr', hostInput.value);
			uci.set('openfrp', name, 'port', controlPort.value || '7000');
			uci.set('openfrp', name, 'ssh_host', hostInput.value);
			uci.set('openfrp', name, 'ssh_port', portInput.value || '22');
			uci.set('openfrp', name, 'ssh_user', userInput.value || 'root');
			uci.set('openfrp', name, 'ssh_auth', authSelect.value);
			if (authSelect.value === 'key')
				uci.set('openfrp', name, 'ssh_key_path', keyInput.value);

			else if (passwordInput.value)
				uci.set('openfrp', name, 'ssh_pass', passwordInput.value);

			uci.save().then(function () {
				var args = {
					server: name,
					host: hostInput.value,
					port: parseInt(portInput.value, 10) || 22,
					user: userInput.value || 'root',
					bind_port: parseInt(controlPort.value, 10) || 7000
				};

				if (authSelect.value === 'password' && passwordInput.value)
					args.password = passwordInput.value;
				else if (authSelect.value === 'key' && keyInput.value)
					args.key_path = keyInput.value;

				ui.hideModal();

				callJobStart('deploy', JSON.stringify(args)).then(function (res) {
					if (!res || res.error || !res.id) {
						ui.addNotification(null, E('p', {},
							_('Could not start the deployment: %s')
								.format((res && res.error) || _('no response'))), 'error');
						return;
					}

					showJobModal(_('Deploying server'), res.id, function (ok) {
						if (ok)
							view.reload();
					});
				});
			});
		}
	};
}

function addDialog(view) {
	var mode = 'deploy';

	var existing = existingFields();
	var deploy = deployFields(null);

	var cloudflare = cloudflareFields();

	var groups = {
		deploy: { fields: deploy, action: _('Install') },
		existing: { fields: existing, action: _('Add') },
		cloudflare: { fields: cloudflare, action: _('Authorise') }
	};

	var confirm = button('', 'cbi-button-positive', function () {
		groups[mode].fields.submit(view);
	});

	function refresh() {
		Object.keys(groups).forEach(function (key) {
			groups[key].fields.node.style.display = key === mode ? '' : 'none';
		});
		dom.content(confirm, groups[mode].action);

		if (groups[mode].fields.focus)
			groups[mode].fields.focus();
	}

	var choices = [
		radio('openfrp-add-mode', 'deploy', true,
			_('Install over SSH'),
			_('Connection details are filled in from what the deployment installs.'),
			function () { mode = 'deploy'; refresh(); }),

		radio('openfrp-add-mode', 'existing', false,
			_('An existing server'),
			_('You already have one running. Enter its address and token.'),
			function () { mode = 'existing'; refresh(); }),

		radio('openfrp-add-mode', 'cloudflare', false,
			_('Cloudflare Tunnel'),
			_('Cloudflare carries the traffic. Authorise this router by opening a link.'),
			function () { mode = 'cloudflare'; refresh(); })
	];

	ui.showModal(_('Add a server'), [
		E('div', { 'class': 'cbi-section' },
			choices.map(function (choice) { return choice.node; })),

		E('hr'),
		deploy.node,
		existing.node,
		cloudflare.node,

		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			button(_('Cancel'), '', ui.hideModal), ' ', confirm
		])
	]);

	refresh();
}

function cloudflareSetup(view, section) {
	callJobStart('cf_setup', JSON.stringify({
		server: section,
		name: uci.get('openfrp', section, 'tunnel_name') || ('openfrp-' + section)
	})).then(function (res) {
		if (!res || res.error || !res.id) {
			ui.addNotification(null, E('p', {},
				_('Could not start the setup: %s')
					.format((res && res.error) || _('no response'))), 'error');
			return;
		}
		showJobModal(_('Setting up a Cloudflare tunnel'), res.id, function (ok) {
			if (ok)
				view.reload();
		});
	});
}

function deployDialog(view, section) {
	var deploy = deployFields(section);

	ui.showModal(_('Redeploy %s').format(section), [
		deploy.node,
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Redeploy'), 'cbi-button-positive', function () {
				deploy.submit(view);
			})
		])
	]);

	deploy.focus();
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
			callStatus().then(function (status) { liveStatus = status; },
				function () { liveStatus = null; })
		]);
	},

	reload: function () {
		var self = this;
		uci.unload('openfrp');
		return Promise.all([
			uci.load('openfrp'),
			callStatus().then(function (status) { liveStatus = status; },
				function () { liveStatus = null; })
		]).then(function () {
			return self.render().then(function (node) {
				var container = document.querySelector('#view');
				if (container)
					dom.content(container, node);
			});
		});
	},

	render: function () {
		var self = this;
		var m, s, o;

		m = new form.Map('openfrp', _('Servers'),
			_('Each has its own control connection; a tunnel names the one that publishes it.'));

		s = m.section(form.NamedSection, 'global', 'global', _('Service'));

		o = s.option(form.Flag, 'enabled', _('Enable OpenFrp'));
		o.rmempty = false;

		o = s.option(form.ListValue, 'log_level', _('Log level'));
		o.value('debug', 'debug');
		o.value('info', 'info');
		o.value('warn', 'warn');
		o.value('error', 'error');
		o.default = 'info';

		s = m.section(form.GridSection, 'server', _('Servers'));
		s.addremove = true;
		s.anonymous = false;
		s.nodescriptions = true;

		s.renderSectionAdd = function (extra_class) {
			return E('div', { 'class': 'cbi-section-create' },
				button(_('Add a server'), 'cbi-button-add', function () {
					addDialog(self);
				}));
		};

		s.renderRowActions = function (section_id, more_label, trEl) {
			var label = uci.get('openfrp', section_id, 'kind') === 'cloudflare'
				? null : _('Edit');
			return form.TableSection.prototype.renderRowActions.call(
				this, section_id, label, trEl);
		};

		s.modaltitle = function (section_id) {
			return _('Server') + ' » ' + section_id;
		};

		o = s.option(form.Value, 'addr', _('Address'));
		o.datatype = 'or(host,ipaddr)';
		o.rmempty = false;

		o.textvalue = function (section_id) {
			if (uci.get('openfrp', section_id, 'kind') === 'cloudflare')
				return _('Cloudflare Tunnel');
			return uci.get('openfrp', section_id, 'addr') || '';
		};

		o = s.option(form.Value, 'port', _('Control port'));
		o.datatype = 'port';
		o.default = '7000';
		o.textvalue = function (section_id) {
			if (uci.get('openfrp', section_id, 'kind') === 'cloudflare')
				return '—';
			return uci.get('openfrp', section_id, 'port') || '7000';
		};

		o = s.option(form.DummyValue, '_tunnels', _('Tunnels'));
		o.modalonly = false;
		o.cfgvalue = function (section_id) {
			var first = null;
			uci.sections('openfrp', 'server', function (server) {
				if (first === null)
					first = server['.name'];
			});

			var count = 0;
			uci.sections('openfrp', 'tunnel', function (tunnel) {
				var owner = tunnel.server || first;
				if (owner === section_id && tunnel.enabled === '1')
					count++;
			});
			return String(count);
		};

		o = s.option(form.DummyValue, '_version', _('Version'));
		o.modalonly = false;
		o.cfgvalue = function (section_id) {
			if (uci.get('openfrp', section_id, 'kind') === 'cloudflare')
				return '—';

			var info = liveStatus && liveStatus.servers &&
				liveStatus.servers[section_id];

			if (!info) {
				return (liveStatus && liveStatus.running)
					? _('not connected') : '—';
			}
			if (!info.version)
				return info.connected ? _('connected') : _('not connected');

			return info.connected
				? info.version
				: '%s (%s)'.format(info.version, _('not connected'));
		};

		o = s.option(form.DummyValue, '_deploy', _('Provisioning'));
		o.modalonly = false;
		o.cfgvalue = function (section_id) {
			if (uci.get('openfrp', section_id, 'kind') === 'cloudflare') {
				var tunnel = uci.get('openfrp', section_id, 'tunnel_id');
				return tunnel ? _('Tunnel %s').format(tunnel)
					: _('No tunnel yet — press Set up');
			}

			if (!uci.get('openfrp', section_id, 'token')) {
				return uci.get('openfrp', section_id, 'ssh_host')
					? _('Deployment did not finish — no token yet')
					: _('No token — this server cannot be reached');
			}
			return uci.get('openfrp', section_id, 'host_fingerprint')
				? _('Deployed from here') : _('Not deployed from here');
		};

		o = s.option(form.Button, '_redeploy', _('Deploy'));
		o.modalonly = false;

		o.editable = true;
		o.inputtitle = function (section_id) {
			return uci.get('openfrp', section_id, 'kind') === 'cloudflare'
				? _('Set up') : _('Deploy over SSH');
		};
		o.inputstyle = 'apply';
		o.onclick = function (ev, section_id) {
			if (uci.get('openfrp', section_id, 'kind') === 'cloudflare') {
				cloudflareSetup(self, section_id);
				return false;
			}
			deployDialog(self, section_id);
			return false;
		};

		o.parse = function () {
			return Promise.resolve();
		};

		o = s.option(form.Value, 'token', _('Token'),
			_('Shared secret. Stored in plaintext on this router.'));
		o.password = true;
		o.modalonly = true;

		o = s.option(form.Value, 'pool_count', _('Warm connections'),
			_('Connections kept open so an arriving request does not wait for a dial.'));
		o.datatype = 'range(1,64)';
		o.default = '8';
		o.modalonly = true;

		o = s.option(form.Flag, 'mux', _('Multiplex connections'));
		o.default = '0';
		o.modalonly = true;
		o.description = _('All tunnels share one congestion window; no kernel zero-copy. Rarely wanted.');

		o = s.option(form.Flag, 'tls_enable', _('TLS on the control connection'));
		o.default = '0';
		o.modalonly = true;

		o = s.option(form.Flag, 'direct_egress', _('Bypass the local proxy'));
		o.default = '0';
		o.modalonly = true;
		o.description = _('Skips any transparent proxy on this router. The log shows the source address the server saw.');

		o = s.option(form.Value, 'host_fingerprint', _('Host key fingerprint'),
			_('Recorded on first connection and checked afterwards.'));
		o.readonly = true;
		o.modalonly = true;

		o = s.option(form.Value, 'binary_path', _('Server binary'),
			_('Uploaded to the server, so it needs no outbound internet there.'));
		o.placeholder = '/usr/lib/openfrp/openfrps';
		o.modalonly = true;

		o = s.option(form.Value, 'release_url', _('Download URL'),
			_('Used when the file above is missing. {arch} and {os} are substituted.'));
		o.placeholder = 'https://example.com/openfrps_{os}_{arch}';
		o.modalonly = true;

		return m.render().then(function (node) {
			return E('div', {}, [stylesheet(), node]);
		});
	}
});
