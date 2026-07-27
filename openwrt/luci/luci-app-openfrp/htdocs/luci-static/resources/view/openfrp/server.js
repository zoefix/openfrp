'use strict';
'require view';
'require form';
'require uci';
'require rpc';
'require ui';
'require poll';
'require dom';

/*
 * Server settings, plus one-click provisioning over SSH.
 *
 * The deployment itself runs detached: rpcd kills any call past 30 seconds and
 * an SSH deploy takes longer, so job_start returns a job id immediately and
 * this view polls job_status for incremental output. A user navigating away
 * does not interrupt the deployment.
 */

var callJobStart = rpc.declare({
	object: 'luci.openfrp',
	method: 'job_start',
	params: ['kind', 'args'],
	expect: {}
});

var callJobStatus = rpc.declare({
	object: 'luci.openfrp',
	method: 'job_status',
	params: ['id', 'offset'],
	expect: {}
});

var callJobCancel = rpc.declare({
	object: 'luci.openfrp',
	method: 'job_cancel',
	params: ['id'],
	expect: {}
});

// showJobModal opens a live log window and follows the job to completion.
function showJobModal(title, jobId) {
	var offset = 0;
	var output = E('pre', {
		'style': 'max-height:26em;overflow:auto;white-space:pre-wrap;' +
			'font-size:90%;background:#1e1e1e;color:#ddd;padding:0.6em;border-radius:3px'
	}, _('Starting…'));

	var statusLine = E('p', {}, _('Running…'));
	var closeButton = E('button', {
		'class': 'btn',
		'click': function () {
			poll.remove(tick);
			ui.hideModal();
		}
	}, _('Close'));

	var cancelButton = E('button', {
		'class': 'btn cbi-button-negative',
		'click': function () {
			callJobCancel(jobId);
		}
	}, _('Cancel'));

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

			if (res.state === 'succeeded')
				statusLine.textContent = _('Finished successfully.');
			else if (res.state === 'failed')
				statusLine.textContent = _('Failed — see the output above.');
			else
				statusLine.textContent = _('Job ended in state: %s').format(res.state);
		});
	}

	ui.showModal(title, [
		statusLine,
		output,
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			cancelButton, ' ', closeButton
		])
	]);

	poll.add(tick, 1);
	tick();
}

return view.extend({
	load: function () {
		return uci.load('openfrp');
	},

	render: function () {
		var m, s, o;

		m = new form.Map('openfrp', _('Server'),
			_('Where this router connects, and how to provision that server.'));

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

		s = m.section(form.NamedSection, 'server', 'server', _('Connection'));

		o = s.option(form.Value, 'addr', _('Server address'));
		o.datatype = 'or(host,ipaddr)';
		o.rmempty = false;
		o.placeholder = '203.0.113.10';

		o = s.option(form.Value, 'port', _('Control port'));
		o.datatype = 'port';
		o.default = '7000';

		o = s.option(form.Value, 'token', _('Token'),
			_('Shared secret. Stored in plaintext on this router.'));
		o.password = true;

		o = s.option(form.ListValue, 'protocol', _('Transport'));
		o.value('tcp', 'TCP');
		o.value('kcp', 'KCP');
		o.value('quic', 'QUIC');
		o.value('websocket', 'WebSocket');
		o.default = 'tcp';

		o = s.option(form.Value, 'pool_count', _('Warm connections'),
			_('Connections kept open so an arriving request does not wait for a dial.'));
		o.datatype = 'range(1,64)';
		o.default = '8';

		o = s.option(form.Flag, 'mux', _('Multiplex connections'));
		o.default = '0';
		o.description = _('Off by default, unlike frp. Multiplexing puts every tunnel ' +
			'behind one congestion window, so a single lost packet stalls them all, ' +
			'and it rules out the kernel zero-copy path. Enable only when the number ' +
			'of open sockets matters more than throughput.');

		o = s.option(form.Flag, 'tls_enable', _('TLS on the control connection'));
		o.default = '0';

		/* ---------------------------------------------------------------- */

		s = m.section(form.NamedSection, 'deploy', 'ssh', _('Provision the server'),
			_('Install and configure the server over SSH. The server needs nothing ' +
			  'installed beforehand.'));

		o = s.option(form.Value, 'host', _('SSH host'));
		o.datatype = 'or(host,ipaddr)';
		o.placeholder = '203.0.113.10';

		o = s.option(form.Value, 'port', _('SSH port'));
		o.datatype = 'port';
		o.default = '22';

		o = s.option(form.Value, 'user', _('SSH user'));
		o.default = 'root';

		o = s.option(form.ListValue, 'auth', _('Authentication'));
		o.value('password', _('Password'));
		o.value('key', _('Private key'));
		o.default = 'password';
		o.description = _('A password is typed in when you deploy and is never ' +
			'stored on this router. A key is stronger, and the deployment can ' +
			'install one for you.');

		o = s.option(form.Value, 'key_path', _('Private key'),
			_('Path to a key on this router.'));
		o.placeholder = '/etc/openfrp/id_ed25519';
		o.depends('auth', 'key');

		o = s.option(form.Value, 'host_fingerprint', _('Host key fingerprint'),
			_('Recorded on first connection and checked afterwards.'));
		o.readonly = true;

		o = s.option(form.Value, 'binary_path', _('Server binary'),
			_('Uploaded to the server. Works without outbound internet there, ' +
			  'and installs the exact bytes this router checksummed.'));
		o.placeholder = '/usr/lib/openfrp/openfrps';

		o = s.option(form.Value, 'release_url', _('Download URL'),
			_('Used when the file above is missing. {arch} and {os} are ' +
			  'substituted with what the server turns out to be.'));
		o.placeholder = 'https://example.com/openfrps_{os}_{arch}';

		o = s.option(form.Button, '_deploy', _('Deploy'));
		o.inputtitle = _('Deploy now');
		o.inputstyle = 'apply';
		o.description = _('Detects the distribution and init system, installs the ' +
			'server, writes its configuration, opens the firewall and verifies the ' +
			'result. Safe to run again to upgrade in place.');
		o.onclick = function () {
			var host = uci.get('openfrp', 'deploy', 'host');
			if (!host) {
				ui.addNotification(null,
					E('p', {}, _('Set the SSH host first.')), 'warning');
				return;
			}

			// The password is prompted for here and handed straight to the job.
			// It is deliberately not a UCI option: LuCI's o.password only masks
			// a field on screen, and the value behind it sits in /etc/config in
			// plain text where it survives backups and firmware upgrades. Asked
			// for each deployment, it never reaches disk at all.
			var usesPassword = (uci.get('openfrp', 'deploy', 'auth') || 'password') === 'password';

			var passwordInput = E('input', {
				'type': 'password',
				'class': 'cbi-input-password',
				'style': 'width:100%',
				'autocomplete': 'off',
				'placeholder': _('SSH password for %s@%s')
					.format(uci.get('openfrp', 'deploy', 'user') || 'root', host)
			});

			function start() {
				var password = passwordInput.value;

				if (usesPassword && !password) {
					ui.addNotification(null,
						E('p', {}, _('Enter the SSH password.')), 'warning');
					return;
				}

				ui.hideModal();

				var args = {
					host: host,
					port: parseInt(uci.get('openfrp', 'deploy', 'port') || '22', 10),
					user: uci.get('openfrp', 'deploy', 'user') || 'root'
				};

				if (usesPassword)
					args.password = password;
				else if (uci.get('openfrp', 'deploy', 'key_path'))
					args.key_path = uci.get('openfrp', 'deploy', 'key_path');

				// Sent back so a repeat deployment authenticates the server
				// instead of trusting it afresh.
				if (uci.get('openfrp', 'deploy', 'host_fingerprint'))
					args.host_fingerprint = uci.get('openfrp', 'deploy', 'host_fingerprint');

				// Keep the provisioned server consistent with what this client
				// is configured to expect. Re-deploying with the existing token
				// is what makes the operation an in-place upgrade rather than a
				// re-key that silently orphans the client.
				if (uci.get('openfrp', 'server', 'token'))
					args.token = uci.get('openfrp', 'server', 'token');
				if (uci.get('openfrp', 'server', 'port'))
					args.bind_port = parseInt(uci.get('openfrp', 'server', 'port'), 10);

				args.local_binary = uci.get('openfrp', 'deploy', 'binary_path') ||
					'/usr/lib/openfrp/openfrps';
				if (uci.get('openfrp', 'deploy', 'release_url'))
					args.release_url = uci.get('openfrp', 'deploy', 'release_url');

				// The backend rejects unknown fields, so nothing beyond this
				// set may be added here without a matching change in
				// cmd/openfrpc/cmd/deploy.go.
				callJobStart('deploy', JSON.stringify(args)).then(function (res) {
					// Drop the password as soon as it has been handed over.
					password = null;
					passwordInput.value = '';

					if (!res || res.error) {
						ui.addNotification(null, E('p', {},
							_('Could not start the deployment: %s')
								.format((res && res.error) || _('no response'))),
							'error');
						return;
					}
					showJobModal(_('Deploying server'), res.id);
				});
			}

			var body = [
				E('p', {}, _('Deploying to %s. This can take a few minutes.').format(host))
			];

			if (usesPassword) {
				body.push(E('div', { 'class': 'cbi-value' }, [
					E('label', { 'class': 'cbi-value-title' }, _('SSH password')),
					E('div', { 'class': 'cbi-value-field' }, [
						passwordInput,
						E('div', { 'class': 'cbi-value-description' },
							_('Used for this deployment only. It is not saved.'))
					])
				]));
				// Enter should submit, as it would in any login prompt.
				passwordInput.addEventListener('keydown', function (ev) {
					if (ev.key === 'Enter')
						start();
				});
			}

			body.push(E('div', { 'class': 'right' }, [
				E('button', { 'class': 'btn', 'click': ui.hideModal }, _('Cancel')),
				' ',
				E('button', { 'class': 'btn cbi-button-positive', 'click': start }, _('Start'))
			]));

			ui.showModal(_('Deploy server'), body);

			if (usesPassword)
				passwordInput.focus();
		};

		return m.render();
	}
});
