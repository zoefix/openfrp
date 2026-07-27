'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';
'require poll';
'require dom';

/*
 * Servers.
 *
 * A router can connect to several, each with its own control connection,
 * token and transport settings; a tunnel names the one that publishes it.
 * They are UCI sections of type "server", so the single section named
 * "server" that older configurations have is simply the first of them and
 * needs no migration.
 *
 * Adding one is either describing a server that already runs OpenFrp, or
 * provisioning a fresh one over SSH — which fills the connection details in
 * by itself, because they are whatever it just installed.
 *
 * Deployment runs detached: rpcd kills any call past 30 seconds and an SSH
 * deploy takes longer, so job_start returns immediately and this view polls
 * job_status. Navigating away does not interrupt it.
 */

var callJobStart = rpc.declare({
	object: 'luci.openfrp', method: 'job_start',
	params: ['kind', 'args'], expect: {}
});

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

/* ------------------------------------------------------------------ */
/* Job log                                                             */

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

/* ------------------------------------------------------------------ */
/* Adding a server                                                     */

// sectionName makes a UCI section id from what the operator typed.
//
// The name is the identity a tunnel points at, so it has to survive as an
// anonymous-section-free UCI name: letters, digits and underscore only.
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

// addDialog asks which of the two ways a server is being added.
function addDialog(view) {
	ui.showModal(_('Add a server'), [
		E('p', {}, _('Either describe a server that already runs OpenFrp, or ' +
			'install one now over SSH.')),

		E('div', { 'class': 'cbi-section' }, [
			E('div', { 'style': 'margin-bottom:0.8em' }, [
				button(_('An existing server'), 'cbi-button-action', function () {
					ui.hideModal();
					existingDialog(view);
				}),
				E('div', { 'class': 'cbi-value-description' },
					_('You already have one running. Enter its address and token.'))
			]),
			E('div', {}, [
				button(_('Install over SSH'), 'cbi-button-positive', function () {
					ui.hideModal();
					deployDialog(view, null);
				}),
				E('div', { 'class': 'cbi-value-description' },
					_('A blank host will do. The connection details are filled in ' +
					  'from whatever the deployment installs.'))
			])
		]),

		E('div', { 'class': 'right', 'style': 'margin-top:1em' },
			button(_('Cancel'), '', ui.hideModal))
	]);
}

// existingDialog collects the details of a server that is already running.
function existingDialog(view) {
	var nameInput = input({ 'placeholder': 'main' });
	var addrInput = input({ 'placeholder': '203.0.113.10' });
	var portInput = input({ 'type': 'number' }, '7000');
	var tokenInput = input({ 'type': 'password', 'class': 'cbi-input-password' });

	function save() {
		if (!addrInput.value) {
			ui.addNotification(null, E('p', {}, _('Enter the server address.')), 'warning');
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

	ui.showModal(_('An existing server'), [
		row(_('Name'), nameInput,
			_('Local to this router — a tunnel points at it. The server never sees it.')),
		row(_('Server address'), addrInput),
		row(_('Control port'), portInput),
		row(_('Token'), tokenInput,
			_('The shared secret the server was configured with.')),
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Add'), 'cbi-button-positive', save)
		])
	]);
}

// deployDialog provisions a server over SSH.
//
// section is null when adding, or the name of an existing server being
// redeployed. Redeploying reuses the stored token, so the tunnels already
// pointing at it keep working.
function deployDialog(view, section) {
	var adding = !section;

	function stored(option, fallback) {
		return (section && uci.get('openfrp', section, option)) || fallback || '';
	}

	var nameInput = input({ 'placeholder': 'main' }, stored('_name'));
	var hostInput = input({ 'placeholder': '203.0.113.10' }, stored('ssh_host'));
	var portInput = input({ 'type': 'number' }, stored('ssh_port', '22'));
	var userInput = input({}, stored('ssh_user', 'root'));
	var passwordInput = input({
		'type': 'password', 'class': 'cbi-input-password', 'autocomplete': 'off'
	});
	var keyInput = input({ 'placeholder': '/etc/openfrp/id_ed25519' }, stored('ssh_key_path'));

	var authSelect = E('select', { 'class': 'cbi-input-select' }, [
		E('option', { 'value': 'password' }, _('Password')),
		E('option', { 'value': 'key' }, _('Private key'))
	]);
	authSelect.value = stored('ssh_auth', 'password');

	var passwordRow = row(_('SSH password'), passwordInput,
		_('Used for this deployment only. It is not saved.'));
	var keyRow = row(_('Private key'), keyInput,
		_('Path to a key on this router.'));

	function refreshAuth() {
		var usingPassword = authSelect.value === 'password';
		passwordRow.style.display = usingPassword ? '' : 'none';
		keyRow.style.display = usingPassword ? 'none' : '';
	}
	authSelect.addEventListener('change', refreshAuth);
	refreshAuth();

	var controlPort = input({ 'type': 'number' }, stored('port', '7000'));

	function start() {
		if (!hostInput.value) {
			ui.addNotification(null, E('p', {}, _('Enter the SSH host.')), 'warning');
			return;
		}
		if (authSelect.value === 'password' && !passwordInput.value) {
			ui.addNotification(null, E('p', {}, _('Enter the SSH password.')), 'warning');
			return;
		}

		// The section is created before the job runs, so the worker has
		// somewhere to write the token and fingerprint it produces.
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

		// Saved before deploying: the worker writes the results straight into
		// this section, and it has to exist on disk for that to land.
		uci.save().then(function () {
			var args = {
				server: name,
				host: hostInput.value,
				port: parseInt(portInput.value, 10) || 22,
				user: userInput.value || 'root',
				bind_port: parseInt(controlPort.value, 10) || 7000
			};

			if (authSelect.value === 'password')
				args.password = passwordInput.value;
			else if (keyInput.value)
				args.key_path = keyInput.value;

			// Redeploying keeps the existing token, so tunnels already pointing
			// here are not orphaned by a re-key.
			var token = section && uci.get('openfrp', section, 'token');
			if (token)
				args.token = token;

			var fingerprint = uci.get('openfrp', name, 'host_fingerprint');
			if (fingerprint)
				args.host_fingerprint = fingerprint;

			args.local_binary = uci.get('openfrp', name, 'binary_path') ||
				'/usr/lib/openfrp/openfrps';
			var release = uci.get('openfrp', name, 'release_url');
			if (release)
				args.release_url = release;

			ui.hideModal();

			callJobStart('deploy', JSON.stringify(args)).then(function (res) {
				passwordInput.value = '';

				if (!res || res.error || !res.id) {
					ui.addNotification(null, E('p', {},
						_('Could not start the deployment: %s')
							.format((res && res.error) || _('no response'))), 'error');
					return;
				}

				showJobModal(_('Deploying server'), res.id, function (ok) {
					// The worker wrote the token and fingerprint into UCI, so
					// the page has to re-read rather than trust what it holds.
					if (ok)
						view.reload();
				});
			});
		});
	}

	var body = [];
	if (adding)
		body.push(row(_('Name'), nameInput,
			_('Local to this router — a tunnel points at it.')));

	body = body.concat([
		row(_('SSH host'), hostInput),
		row(_('SSH port'), portInput),
		row(_('SSH user'), userInput),
		row(_('Authentication'), authSelect),
		passwordRow,
		keyRow,
		row(_('Control port'), controlPort,
			_('The port the server will listen on for this router.')),
		E('p', {}, _('Detects the distribution and init system, removes any ' +
			'previous installation, installs the server, opens the firewall and ' +
			'verifies the result.')),
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(adding ? _('Install') : _('Redeploy'), 'cbi-button-positive', start)
		])
	]);

	ui.showModal(adding ? _('Install over SSH') : _('Redeploy %s').format(section), body);

	if (authSelect.value === 'password')
		passwordInput.focus();
}

/* ------------------------------------------------------------------ */

return view.extend({
	load: function () {
		return uci.load('openfrp');
	},

	// reload re-reads UCI and re-renders, which is what the deploy job needs:
	// it writes the token and fingerprint itself.
	reload: function () {
		var self = this;
		uci.unload('openfrp');
		return uci.load('openfrp').then(function () {
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
			_('Every server this router connects to. Each has its own control ' +
			  'connection, and a tunnel names the one that publishes it.'));

		/* ---------------------------------------------------------------- */

		s = m.section(form.NamedSection, 'global', 'global', _('Service'));

		o = s.option(form.Flag, 'enabled', _('Enable OpenFrp'));
		o.rmempty = false;

		o = s.option(form.ListValue, 'log_level', _('Log level'));
		o.value('debug', 'debug');
		o.value('info', 'info');
		o.value('warn', 'warn');
		o.value('error', 'error');
		o.default = 'info';

		/* ---------------------------------------------------------------- */

		s = m.section(form.GridSection, 'server', _('Servers'));
		s.addremove = true;
		s.anonymous = false;
		s.nodescriptions = true;

		// Adding goes through the dialog, which asks whether this is an
		// existing server or one to install. The built-in "add" would create a
		// blank section with neither path taken.
		s.renderSectionAdd = function (extra_class) {
			return E('div', { 'class': 'cbi-section-create' },
				button(_('Add a server'), 'cbi-button-add', function () {
					addDialog(self);
				}));
		};

		s.modaltitle = function (section_id) {
			return _('Server') + ' » ' + section_id;
		};

		o = s.option(form.Value, 'addr', _('Address'));
		o.datatype = 'or(host,ipaddr)';
		o.rmempty = false;

		o = s.option(form.Value, 'port', _('Control port'));
		o.datatype = 'port';
		o.default = '7000';

		o = s.option(form.DummyValue, '_tunnels', _('Tunnels'));
		o.modalonly = false;
		o.cfgvalue = function (section_id) {
			// Which tunnels this server publishes. A tunnel naming no server
			// belongs to the first one, which is the rule the daemon applies.
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

		o = s.option(form.DummyValue, '_deploy', _('Provisioning'));
		o.modalonly = false;
		o.cfgvalue = function (section_id) {
			return uci.get('openfrp', section_id, 'host_fingerprint')
				? _('Deployed from here') : _('Not deployed from here');
		};

		o = s.option(form.Button, '_redeploy', _('Deploy'));
		o.modalonly = false;
		o.inputtitle = _('Deploy over SSH');
		o.inputstyle = 'apply';
		o.onclick = function (ev, section_id) {
			deployDialog(self, section_id);
			return false;
		};

		o = s.option(form.Value, 'token', _('Token'),
			_('Shared secret. Stored in plaintext on this router.'));
		o.password = true;
		o.modalonly = true;

		o = s.option(form.ListValue, 'protocol', _('Transport'));
		o.value('tcp', 'TCP');
		o.value('kcp', 'KCP');
		o.value('quic', 'QUIC');
		o.value('websocket', 'WebSocket');
		o.default = 'tcp';
		o.modalonly = true;

		o = s.option(form.Value, 'pool_count', _('Warm connections'),
			_('Connections kept open so an arriving request does not wait for a dial.'));
		o.datatype = 'range(1,64)';
		o.default = '8';
		o.modalonly = true;

		o = s.option(form.Flag, 'mux', _('Multiplex connections'));
		o.default = '0';
		o.modalonly = true;
		o.description = _('Off by default, unlike frp. Multiplexing puts every tunnel ' +
			'behind one congestion window, so a single lost packet stalls them all, ' +
			'and it rules out the kernel zero-copy path. Enable only when the number ' +
			'of open sockets matters more than throughput.');

		o = s.option(form.Flag, 'tls_enable', _('TLS on the control connection'));
		o.default = '0';
		o.modalonly = true;

		o = s.option(form.Value, 'host_fingerprint', _('Host key fingerprint'),
			_('Recorded on first connection and checked afterwards.'));
		o.readonly = true;
		o.modalonly = true;

		o = s.option(form.Value, 'binary_path', _('Server binary'),
			_('Uploaded to the server. Works without outbound internet there, ' +
			  'and installs the exact bytes this router checksummed.'));
		o.placeholder = '/usr/lib/openfrp/openfrps';
		o.modalonly = true;

		o = s.option(form.Value, 'release_url', _('Download URL'),
			_('Used when the file above is missing. {arch} and {os} are ' +
			  'substituted with what the server turns out to be.'));
		o.placeholder = 'https://example.com/openfrps_{os}_{arch}';
		o.modalonly = true;

		return m.render();
	}
});
