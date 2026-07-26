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

		o = s.option(form.Value, 'key_path', _('Private key'),
			_('Path to a key on this router. Recommended over a password.'));
		o.placeholder = '/etc/openfrp/id_ed25519';

		o = s.option(form.Value, 'host_fingerprint', _('Host key fingerprint'),
			_('Recorded on first connection and checked afterwards.'));
		o.readonly = true;

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

			// The password, if one is used, is prompted for here and passed
			// straight through to the job. It is never written to UCI and
			// never appears in a command line.
			ui.showModal(_('Deploy server'), [
				E('p', {}, _('Deploying to %s. This can take a few minutes.').format(host)),
				E('div', { 'class': 'right' }, [
					E('button', {
						'class': 'btn',
						'click': ui.hideModal
					}, _('Cancel')),
					' ',
					E('button', {
						'class': 'btn cbi-button-positive',
						'click': function () {
							ui.hideModal();
							var args = JSON.stringify({
								host: host,
								port: uci.get('openfrp', 'deploy', 'port') || '22',
								user: uci.get('openfrp', 'deploy', 'user') || 'root',
								key_path: uci.get('openfrp', 'deploy', 'key_path') || ''
							});
							callJobStart('deploy', args).then(function (res) {
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
					}, _('Start'))
				])
			]);
		};

		return m.render();
	}
});
