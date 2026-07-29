'use strict';
'require view';
'require rpc';
'require ui';
'require dom';
'require poll';

/*
 * TLS certificates.
 *
 * Issuance is not an rpcd call. It talks to the CA and then waits for DNS
 * propagation, which takes minutes against rpcd's 30 second limit, so it runs
 * as a detached job and this page follows its log.
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

function unwrap(res) {
	if (!res || res.error)
		return Promise.reject(new Error((res && res.error) || _('no response')));
	if (res.ok === false)
		return Promise.reject(new Error(res.error || _('the request failed')));
	return res.data;
}

function call(action, params, payload) {
	return callCert(action, params || {}, payload ? JSON.stringify(payload) : '')
		.then(unwrap);
}

function notifyError(err) {
	ui.addNotification(null, E('p', {}, err.message || String(err)), 'error');
}

function button(label, style, onclick) {
	return E('button', { 'class': 'btn ' + (style || ''), 'click': onclick }, label);
}

var state = { orders: [], cas: [], keyTypes: [], accounts: [] };

/* ------------------------------------------------------------------ */

// stateBadge colours an order by what an operator would do about it.
function stateBadge(order) {
	var colour, label;

	switch (order.state) {
		case 'issued':
			// Expiry matters more than the state once issued: a certificate
			// that renewed months ago and is now expiring is still "issued".
			if (order.days_remaining < 0) {
				colour = '#d9534f';
				label = _('Expired');
			} else if (order.days_remaining <= 7) {
				colour = '#f0ad4e';
				label = _('%d days left').format(order.days_remaining);
			} else {
				colour = '#5bb75b';
				label = _('%d days left').format(order.days_remaining);
			}
			break;
		case 'issuing':
			colour = '#428bca'; label = _('Issuing…'); break;
		case 'failed':
			colour = '#d9534f'; label = _('Failed'); break;
		default:
			colour = '#999'; label = _('Not issued yet');
	}

	return E('span', {
		'style': 'padding:2px 8px;border-radius:3px;white-space:nowrap;color:#fff;' +
			'background:' + colour
	}, label);
}

function ordersTable() {
	if (!state.orders.length)
		return [E('p', {}, _('No certificates yet.'))];

	var head = E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, _('Domains')),
		E('th', { 'class': 'th' }, _('Authority')),
		E('th', { 'class': 'th' }, _('Key')),
		E('th', { 'class': 'th' }, _('Renewal')),
		E('th', { 'class': 'th' }, _('State')),
		E('th', { 'class': 'th' }, '')
	]);

	var rows = state.orders.map(function (order) {
		var detail = [E('div', {}, order.domains.join(', '))];
		if (order.last_error)
			detail.push(E('div', {
				'style': 'color:#d9534f;font-size:90%;word-break:break-word'
			}, order.last_error));

		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td', 'style': 'max-width:22em' }, detail),
			E('td', { 'class': 'td' }, order.ca_label),
			E('td', { 'class': 'td' }, order.key_type),
			E('td', { 'class': 'td' }, order.auto_renew
				? _('Automatic') : E('em', {}, _('Manual'))),
			E('td', { 'class': 'td' }, stateBadge(order)),
			E('td', { 'class': 'td', 'style': 'text-align:right;white-space:nowrap' }, [
				button(order.state === 'issued' ? _('Renew now') : _('Issue'),
					'cbi-button-action', function () { issue(order); }), ' ',
				button(_('History'), '', function () { showEvents(order); }), ' ',
				button(_('Delete'), 'cbi-button-negative', function () {
					deleteOrder(order);
				})
			])
		]);
	});

	return [E('table', { 'class': 'table' }, [head].concat(rows))];
}

/* ------------------------------------------------------------------ */

function orderDialog() {
	function field(label, input, help) {
		var children = [input];
		if (help)
			children.push(E('div', { 'class': 'cbi-value-description' }, help));
		return E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, label),
			E('div', { 'class': 'cbi-value-field' }, children)
		]);
	}

	var domainsInput = E('textarea', {
		'class': 'cbi-input-textarea', 'rows': 3, 'style': 'width:100%',
		'placeholder': '*.aiqno.com\nacgshop.aiqno.com'
	});
	var emailInput = E('input', {
		'type': 'text', 'class': 'cbi-input-text', 'style': 'width:100%',
		'placeholder': 'ops@example.com'
	});
	var caSelect = E('select', { 'class': 'cbi-input-select' },
		state.cas.map(function (ca) {
			return E('option', { 'value': ca.key }, ca.label);
		}));
	var keySelect = E('select', { 'class': 'cbi-input-select' },
		state.keyTypes.map(function (k) {
			return E('option', { 'value': k.value }, k.label);
		}));
	// "None" means validate over HTTP, which the tunnel server answers on this
	// router's behalf and which needs no credentials for the zone. A wildcard
	// is the one case that cannot use it.
	var accountSelect = E('select', { 'class': 'cbi-input-select' },
		[E('option', { 'value': '' }, _('None — validate over HTTP'))].concat(
			state.accounts.map(function (a) {
				return E('option', { 'value': String(a.id) },
					a.name + ' (' + a.provider_label + ')');
			})));
	var autoRenew = E('input', {
		'type': 'checkbox', 'class': 'cbi-input-checkbox', 'checked': ''
	});

	// External account binding. ZeroSSL and Google Trust Services refuse to
	// issue without it; Let's Encrypt does not use it at all. The rows are
	// hidden rather than absent so switching authority does not relayout.
	var eabKeyInput = E('input', {
		'type': 'text', 'class': 'cbi-input-text', 'style': 'width:100%'
	});
	var eabHmacInput = E('input', {
		'type': 'password', 'class': 'cbi-input-password', 'style': 'width:100%'
	});
	var eabHint = E('div', { 'class': 'cbi-value-description' }, '');

	function selectedCA() {
		return state.cas.filter(function (ca) { return ca.key === caSelect.value; })[0];
	}

	// eabStored is set once the backend confirms credentials are already on
	// file for the chosen authority. They are entered once and reused for
	// every certificate from it, so asking again is both noise and misleading.
	var eabStored = false;

	function refreshEAB() {
		var ca = selectedCA();
		var needed = !!(ca && ca.requires_eab);

		eabRows.forEach(function (row) { row.style.display = needed ? '' : 'none'; });
		if (!needed)
			return;

		var help = ca.key === 'zerossl'
			? 'https://app.zerossl.com/developer'
			: 'https://console.cloud.google.com/security/publicca';

		// Ask before rendering: the answer decides whether these are fields
		// the operator must fill in or ones they can ignore.
		call('eab-status', { ca: ca.key })
			.then(function (status) {
				eabStored = !!(status && status.present);

				eabKeyInput.placeholder = eabStored ? _('Saved — leave blank to keep') : '';
				eabHmacInput.placeholder = eabStored ? _('Saved — leave blank to keep') : '';

				dom.content(eabHint, eabStored
					? [E('em', {}, _('Already saved for %s and reused automatically. ' +
						'Fill these in only to replace them.').format(ca.label))]
					: [
						_('%s needs a key pair from your %s account.')
							.format(ca.label, ca.label), ' ',
						E('a', {
							'href': help, 'target': '_blank', 'rel': 'noreferrer'
						}, _('Where to find it'))
					]);
			})
			.catch(function () {
				eabStored = false;
				dom.content(eabHint, _('%s needs external account binding credentials.')
					.format(ca.label));
			});
	}

	function save() {
		var domains = domainsInput.value.split(/[\s,]+/).filter(function (d) {
			return d.length > 0;
		});

		if (!domains.length) {
			notifyError(new Error(_('List at least one domain.')));
			return;
		}
		if (!accountSelect.value && domains.some(function (d) {
			return d.indexOf('*') === 0;
		})) {
			notifyError(new Error(
				_('A wildcard can only be proved through DNS. Select an account.')));
			return;
		}

		var ca = selectedCA();
		var wantsEAB = !!(ca && ca.requires_eab);
		var givingEAB = wantsEAB && (eabKeyInput.value || eabHmacInput.value);

		if (wantsEAB && !eabStored && !givingEAB) {
			notifyError(new Error(
				_('%s needs external account binding credentials.').format(ca.label)));
			return;
		}
		if (givingEAB && (!eabKeyInput.value || !eabHmacInput.value)) {
			notifyError(new Error(_('Both fields are required.')));
			return;
		}
		if (!emailInput.value) {
			notifyError(new Error(_('Enter a contact email address.')));
			return;
		}

		// Store the binding first, and only when new values were typed in.
		// Doing it after would leave an order that exists and cannot issue if
		// this call failed; doing it always would overwrite a stored pair with
		// the empty boxes the operator never touched.
		var prepared = givingEAB
			? call('eab', {}, {
				ca: caSelect.value, email: emailInput.value,
				key_id: eabKeyInput.value, hmac: eabHmacInput.value
			})
			: Promise.resolve();

		prepared.then(function () {
		call('order-add', {}, {
			domains: domains,
			key_type: keySelect.value,
			ca: caSelect.value,
			email: emailInput.value,
			account_id: parseInt(accountSelect.value, 10) || 0,
			auto_renew: autoRenew.checked
		})
			.then(function () { ui.hideModal(); refresh(); })
			.catch(notifyError);
		}).catch(notifyError);
	}

	var eabRows = [
		field(_('EAB key ID'), eabKeyInput),
		field(_('EAB HMAC key'), eabHmacInput, eabHint)
	];

	caSelect.addEventListener('change', refreshEAB);

	ui.showModal(_('Request a certificate'), [
		field(_('Domains'), domainsInput,
			_('One per line. \"*\" covers one level only: *.aiqno.com, not a.b.aiqno.com.')),
		field(_('Contact email'), emailInput,
			_('The authority requires one, and uses it for expiry warnings.')),
		field(_('Authority'), caSelect),
		field(_('Key type'), keySelect),
		field(_('DNS account'), accountSelect,
			_('How ownership is proved. HTTP needs the name already pointing here; wildcards need DNS.')),
		eabRows[0],
		eabRows[1],
		field(_('Renew automatically'), autoRenew,
			_('Renews once fewer than 30 days remain.')),
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Create'), 'cbi-button-positive', save)
		])
	]);

	refreshEAB();
}

// eabDialog collects binding credentials for an order that cannot issue
// without them, so a failed order can be repaired rather than recreated.
function eabDialog(order, status, then) {
	var keyInput = E('input', {
		'type': 'text', 'class': 'cbi-input-text', 'style': 'width:100%'
	});
	var hmacInput = E('input', {
		'type': 'password', 'class': 'cbi-input-password', 'style': 'width:100%'
	});

	function row(label, input) {
		return E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, label),
			E('div', { 'class': 'cbi-value-field' }, input)
		]);
	}

	ui.showModal(_('%s needs an account binding').format(status.ca_label), [
		E('p', {}, _('Stored once and reused for every certificate from this authority.')
			.format(status.ca_label, status.ca_label)),
		status.how_to_get ? E('p', {}, E('a', {
			'href': status.how_to_get, 'target': '_blank', 'rel': 'noreferrer'
		}, _('Where to find it'))) : E('span', {}),
		row(_('EAB key ID'), keyInput),
		row(_('EAB HMAC key'), hmacInput),
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Save and issue'), 'cbi-button-positive', function () {
				if (!keyInput.value || !hmacInput.value) {
					notifyError(new Error(_('Both fields are required.')));
					return;
				}
				call('eab', {}, {
					ca: status.ca, email: status.email,
					key_id: keyInput.value, hmac: hmacInput.value
				}).then(function () { ui.hideModal(); then(); })
					.catch(notifyError);
			})
		])
	]);
}

/* ------------------------------------------------------------------ */

// issue checks the order can actually proceed, then runs the job.
//
// Starting a job that is certain to be refused wastes minutes and teaches the
// operator nothing they can act on — which is exactly what happened before
// there was anywhere to enter these credentials.
function issue(order) {
	call('eab-status', { id: String(order.id) })
		.then(function (status) {
			if (status && status.required && !status.present) {
				eabDialog(order, status, function () { startIssue(order); });
				return;
			}
			startIssue(order);
		})
		.catch(function () { startIssue(order); });
}

// startIssue runs the job and follows its log.
function startIssue(order) {
	var offset = 0;
	var output = E('pre', {
		'style': 'max-height:24em;overflow:auto;white-space:pre-wrap;font-size:90%;' +
			'background:#1e1e1e;color:#ddd;padding:0.6em;border-radius:3px'
	}, _('Starting…'));
	var statusLine = E('p', {}, _('Takes a few minutes while the DNS record propagates.'));

	var closeButton = button(_('Close'), '', function () {
		poll.remove(tick);
		ui.hideModal();
		refresh();
	});

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
			statusLine.textContent = res.state === 'succeeded'
				? _('The certificate was issued.')
				: _('Issuance failed — see the output above.');
		});
	}

	var jobId = null;

	ui.showModal(_('Issuing certificate'), [
		statusLine, output,
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, closeButton)
	]);

	callJobStart('cert_issue', JSON.stringify({ id: order.id }))
		.then(function (res) {
			if (!res || res.error || !res.id) {
				statusLine.textContent = _('Could not start: %s')
					.format((res && res.error) || _('no response'));
				return;
			}
			jobId = res.id;
			poll.add(tick, 2);
			tick();
		});
}

function showEvents(order) {
	call('events', { id: String(order.id), limit: '50' })
		.then(function (events) {
			var body = (events && events.length)
				? E('table', { 'class': 'table' }, events.map(function (event) {
					return E('tr', { 'class': 'tr' }, [
						E('td', { 'class': 'td', 'style': 'white-space:nowrap' },
							new Date(event.CreatedAt * 1000).toLocaleString()),
						E('td', { 'class': 'td' }, event.Kind),
						E('td', { 'class': 'td', 'style': 'word-break:break-word' },
							event.Detail || '')
					]);
				}))
				: E('p', {}, _('Nothing has happened to this certificate yet.'));

			ui.showModal(order.domains.join(', '), [
				body,
				E('div', { 'class': 'right', 'style': 'margin-top:1em' },
					button(_('Close'), '', ui.hideModal))
			]);
		})
		.catch(notifyError);
}

function deleteOrder(order) {
	ui.showModal(_('Delete certificate'), [
		E('p', {}, _('Delete the certificate for %s?').format(order.domains.join(', '))),
		E('p', {}, _('Anything still serving it will keep the copy it already has ' +
			'until it restarts.')),
		E('div', { 'class': 'right' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Delete'), 'cbi-button-negative', function () {
				call('order-delete', { id: String(order.id) })
					.then(function () { ui.hideModal(); refresh(); })
					.catch(function (err) { ui.hideModal(); notifyError(err); });
			})
		])
	]);
}

/* ------------------------------------------------------------------ */

var ordersHolder = E('div', {});

function refresh() {
	return call('orders').then(function (orders) {
		state.orders = orders || [];
		dom.content(ordersHolder, ordersTable());
	}).catch(function (err) {
		dom.content(ordersHolder, E('div', { 'class': 'alert-message warning' },
			E('p', {}, err.message)));
	});
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
			call('orders').catch(function (err) { return err; }),
			call('cas').catch(function () { return []; }),
			call('keytypes').catch(function () { return []; }),
			callDNS('accounts', {}, '').then(unwrap).catch(function () { return []; })
		]);
	},

	render: function (data) {
		if (data[0] instanceof Error) {
			return E('div', { 'class': 'alert-message warning' }, [
				E('p', {}, _('Certificate management is unavailable.')),
				E('p', {}, data[0].message)
			]);
		}

		state.orders = data[0] || [];
		state.cas = data[1] || [];
		state.keyTypes = data[2] || [];
		state.accounts = data[3] || [];

		dom.content(ordersHolder, ordersTable());

		var warning = null;
		if (!state.accounts.length)
			warning = E('div', { 'class': 'alert-message warning' },
				E('p', {}, _('No DNS account configured. Wildcards need one — add it on the DNS page.')));

		return E('div', {}, [
			stylesheet(),
			E('h2', {}, _('Certificates')),
			E('p', {}, _('Pushed to the server and hot-loaded. Used by tunnels that terminate TLS there.'))
		].concat(warning ? [warning] : []).concat([
			E('div', { 'class': 'cbi-section' }, [
				ordersHolder,
				E('div', { 'style': 'margin-top:1em' },
					button(_('Request a certificate'), 'cbi-button-add', orderDialog))
			])
		]));
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
