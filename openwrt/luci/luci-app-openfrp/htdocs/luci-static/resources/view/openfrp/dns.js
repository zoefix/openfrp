'use strict';
'require view';
'require rpc';
'require ui';
'require dom';
'require openfrp.schema-form as schemaForm';

var callDNS = rpc.declare({
	object: 'luci.openfrp',
	method: 'dns',
	params: ['action', 'params', 'payload'],
	expect: {}
});

function call(action, params, payload) {
	return callDNS(action, params || {}, payload ? JSON.stringify(payload) : '')
		.then(function (res) {
			if (!res || res.error)
				return Promise.reject(new Error((res && res.error) || _('no response')));
			if (res.ok === false)
				return Promise.reject(new Error(res.error || _('the request failed')));
			return res.data;
		});
}

function notifyError(err) {
	ui.addNotification(null, E('p', {}, err.message || String(err)), 'error');
}

function button(label, style, onclick) {
	return E('button', { 'class': 'btn ' + (style || ''), 'click': onclick }, label);
}

var state = {
	providers: [],
	accounts: [],

	account: null,
	zone: null,

	capabilities: {}
};

function providerByKey(key) {
	return state.providers.filter(function (p) { return p.key === key; })[0];
}

function accountDialog(existing) {
	var editing = !!existing;
	var selected = existing ? existing.provider : (state.providers[0] || {}).key;

	var nameInput = E('input', {
		'type': 'text', 'class': 'cbi-input-text', 'style': 'width:100%',
		'value': existing ? existing.name : '',
		'placeholder': _('A name you will recognise, for example aliyun-main')
	});

	var formHolder = E('div', {});
	var form = null;

	function renderForm() {
		var descriptor = providerByKey(selected);
		if (!descriptor) {
			dom.content(formHolder, E('p', {}, _('No DNS providers are available.')));
			return;
		}

		form = schemaForm.render(descriptor.form, existing ? existing.credentials : {});
		dom.content(formHolder, form.node);
	}

	var providerSelect = E('select', { 'class': 'cbi-input-select' },
		state.providers.map(function (p) {
			return E('option', {
				'value': p.key,
				'selected': p.key === selected ? '' : null
			}, p.label);
		}));

	providerSelect.addEventListener('change', function () {
		selected = providerSelect.value;
		renderForm();
	});

	renderForm();

	function save() {
		if (!nameInput.value) {
			notifyError(new Error(_('The account needs a name.')));
			return;
		}
		var problem = form && form.validate();
		if (problem) {
			notifyError(new Error(problem));
			return;
		}

		var payload = {
			name: nameInput.value,
			provider: selected,
			credentials: form ? form.values : {}
		};
		if (editing)
			payload.id = existing.id;

		call(editing ? 'account-update' : 'account-add', {}, payload)
			.then(function () { ui.hideModal(); refreshAccounts(); })
			.catch(notifyError);
	}

	ui.showModal(editing ? _('Edit DNS account') : _('Add DNS account'), [
		E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, _('Name')),
			E('div', { 'class': 'cbi-value-field' }, nameInput)
		]),
		E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, _('Provider')),
			E('div', { 'class': 'cbi-value-field' }, providerSelect)
		]),
		formHolder,
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Save'), 'cbi-button-positive', save)
		])
	]);
}

function testAccount(account) {
	ui.showModal(_('Testing credentials'), [
		E('p', { 'class': 'spinning' }, _('Asking %s to confirm the credentials…')
			.format(account.provider_label))
	]);

	call('account-test', { id: String(account.id) })
		.then(function () {
			ui.hideModal();
			ui.addNotification(null,
				E('p', {}, _('%s accepted the credentials.').format(account.provider_label)),
				'info');
		})
		.catch(function (err) {
			ui.hideModal();
			notifyError(err);
		});
}

function deleteAccount(account) {
	ui.showModal(_('Delete DNS account'), [
		E('p', {}, _('Delete %s?').format(account.name)),

		E('p', {}, _('Certificates issued through it are kept, but cannot renew until moved elsewhere.')),
		E('div', { 'class': 'right' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Delete'), 'cbi-button-negative', function () {
				call('account-delete', { id: String(account.id) })
					.then(function () { ui.hideModal(); refreshAccounts(); })
					.catch(function (err) { ui.hideModal(); notifyError(err); });
			})
		])
	]);
}

function accountsTable() {
	if (!state.accounts.length)
		return [E('p', {}, _('No DNS accounts yet. Add one to manage records and ' +
			'to issue wildcard certificates.'))];

	var head = E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, _('Name')),
		E('th', { 'class': 'th' }, _('Provider')),
		E('th', { 'class': 'th' }, _('Credentials')),
		E('th', { 'class': 'th' }, '')
	]);

	var rows = state.accounts.map(function (account) {
		var configured = (account.secrets_set || []).length
			? _('%d stored').format(account.secrets_set.length)
			: E('em', {}, _('none stored'));

		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td' }, account.name),
			E('td', { 'class': 'td' }, account.provider_label),
			E('td', { 'class': 'td' }, configured),
			E('td', { 'class': 'td', 'style': 'text-align:right;white-space:nowrap' }, [
				button(_('Records'), '', function () { browseAccount(account); }), ' ',
				button(_('Test'), '', function () { testAccount(account); }), ' ',
				button(_('Edit'), 'cbi-button-action', function () { accountDialog(account); }), ' ',
				button(_('Delete'), 'cbi-button-negative', function () { deleteAccount(account); })
			])
		]);
	});

	return [E('table', { 'class': 'table' }, [head].concat(rows))];
}

var recordsHolder = E('div', {});

function browseAccount(account) {
	state.account = account;
	state.zone = null;
	state.capabilities = {};
	dom.content(recordsHolder, E('p', { 'class': 'spinning' }, _('Loading zones…')));

	Promise.all([
		call('domains', { id: String(account.id) }),
		call('capabilities', { id: String(account.id) }).catch(function () { return {}; })
	])
		.then(function (results) {
			state.capabilities = results[1] || {};
			renderZones(results[0] || []);
		})
		.catch(function (err) {
			dom.content(recordsHolder, E('div', { 'class': 'alert-message warning' },
				E('p', {}, err.message)));
		});
}

function renderZones(domains) {
	if (!domains.length) {
		dom.content(recordsHolder, [
			E('h3', {}, _('Zones')),
			E('p', {}, _('This account manages no zones.'))
		]);
		return;
	}

	var select = E('select', { 'class': 'cbi-input-select' },
		domains.map(function (domain) {
			return E('option', { 'value': domain.name }, domain.name);
		}));

	select.addEventListener('change', function () { loadRecords(select.value); });

	dom.content(recordsHolder, [
		E('h3', {}, _('Records in %s').format(state.account.name)),
		E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, _('Zone')),
			E('div', { 'class': 'cbi-value-field' }, [
				select, ' ',
				button(_('Add record'), 'cbi-button-add', function () {
					recordDialog(null);
				})
			])
		]),
		E('div', { 'id': 'openfrp-records' })
	]);

	loadRecords(domains[0].name);
}

function loadRecords(zone) {
	state.zone = zone;

	var holder = document.getElementById('openfrp-records');
	if (!holder)
		return;
	dom.content(holder, E('p', { 'class': 'spinning' }, _('Loading records…')));

	call('records', { id: String(state.account.id), zone: zone })
		.then(function (records) { dom.content(holder, recordsTable(records || [])); })
		.catch(function (err) {
			dom.content(holder, E('div', { 'class': 'alert-message warning' },
				E('p', {}, err.message)));
		});
}

function recordsTable(records) {
	if (!records.length)
		return [E('p', {}, _('This zone has no records.'))];

	var head = E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, _('Name')),
		E('th', { 'class': 'th' }, _('Type')),
		E('th', { 'class': 'th' }, _('Value')),
		E('th', { 'class': 'th' }, _('TTL')),
		E('th', { 'class': 'th' }, _('Line'))
	].concat(state.capabilities.proxy
		? [E('th', { 'class': 'th' }, _('Resolution'))] : []
	).concat([E('th', { 'class': 'th' }, '')]));

	var rows = records.map(function (record) {
		var proxyCell = [];
		if (state.capabilities.proxy) {
			proxyCell = [E('td', { 'class': 'td' }, record.proxied
				? E('span', {
					'style': 'padding:2px 8px;border-radius:3px;' +
						'background:#f38020;color:#fff;white-space:nowrap'
				}, _('Proxied'))
				: E('span', { 'style': 'opacity:0.7' }, _('DNS only')))];
		}

		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td' }, record.name || '@'),
			E('td', { 'class': 'td' }, record.type),
			E('td', {
				'class': 'td',
				'style': 'word-break:break-all;max-width:24em'
			}, record.value),
			E('td', { 'class': 'td' }, String(record.ttl || '')),
			E('td', { 'class': 'td' }, record.line || 'default')
		].concat(proxyCell).concat([
			E('td', { 'class': 'td', 'style': 'text-align:right;white-space:nowrap' }, [
				button(_('Edit'), 'cbi-button-action', function () {
					recordDialog(record);
				}), ' ',
				button(_('Delete'), 'cbi-button-negative', function () {
					deleteRecord(record);
				})
			])
		]));
	});

	return [E('table', { 'class': 'table' }, [head].concat(rows))];
}

var proxiableTypes = ['A', 'AAAA', 'CNAME'];

function recordDialog(existing) {
	var editing = !!existing;

	function field(label, input) {
		return E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, label),
			E('div', { 'class': 'cbi-value-field' }, input)
		]);
	}

	var nameInput = E('input', {
		'type': 'text', 'class': 'cbi-input-text',
		'value': existing ? existing.name : '', 'placeholder': '@'
	});
	var typeSelect = E('select', { 'class': 'cbi-input-select' },
		['A', 'AAAA', 'CNAME', 'TXT', 'MX', 'NS', 'SRV', 'CAA'].map(function (t) {
			return E('option', {
				'value': t, 'selected': existing && existing.type === t ? '' : null
			}, t);
		}));
	var valueInput = E('input', {
		'type': 'text', 'class': 'cbi-input-text', 'style': 'width:100%',
		'value': existing ? existing.value : ''
	});
	var ttlInput = E('input', {
		'type': 'number', 'class': 'cbi-input-text',
		'value': existing ? existing.ttl : 600
	});

	var proxySelect = E('select', { 'class': 'cbi-input-select' }, [
		E('option', { 'value': '0' }, _('DNS only — answer with this address')),
		E('option', { 'value': '1' }, _('Proxied — route through the provider'))
	]);
	if (existing && existing.proxied)
		proxySelect.value = '1';

	var proxyHint = E('div', { 'class': 'cbi-value-description' }, '');
	var proxyRow = E('div', { 'class': 'cbi-value' }, [
		E('label', { 'class': 'cbi-value-title' }, _('Resolution')),
		E('div', { 'class': 'cbi-value-field' }, [proxySelect, proxyHint])
	]);

	function refreshProxyRow() {
		var applicable = state.capabilities.proxy &&
			proxiableTypes.indexOf(typeSelect.value) !== -1;

		proxyRow.style.display = applicable ? '' : 'none';
		if (!applicable)
			return;

		dom.content(proxyHint, proxySelect.value === '1'
			? E('span', { 'style': 'color:#d9534f' },
				_('Do not proxy a tunnel\'s name: the proxy answers HTTPS itself, on its own ports.'))
			: _('The address is answered directly, which is what a tunnel needs.'));
	}

	typeSelect.addEventListener('change', refreshProxyRow);
	proxySelect.addEventListener('change', refreshProxyRow);
	refreshProxyRow();

	function save() {
		var record = {
			name: nameInput.value,
			type: typeSelect.value,
			value: valueInput.value,
			ttl: parseInt(ttlInput.value, 10) || 600,
			enabled: true
		};

		if (state.capabilities.proxy && proxiableTypes.indexOf(typeSelect.value) !== -1)
			record.proxied = proxySelect.value === '1';

		if (editing)
			record.id = existing.id;

		call(editing ? 'record-update' : 'record-add',
			{ id: String(state.account.id), zone: state.zone }, record)
			.then(function () { ui.hideModal(); loadRecords(state.zone); })
			.catch(notifyError);
	}

	ui.showModal(editing ? _('Edit record') : _('Add record'), [
		field(_('Name'), nameInput),
		field(_('Type'), typeSelect),
		field(_('Value'), valueInput),
		field(_('TTL'), ttlInput),
		proxyRow,
		E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Save'), 'cbi-button-positive', save)
		])
	]);
}

function deleteRecord(record) {
	ui.showModal(_('Delete record'), [
		E('p', {}, _('Delete the %s record for %s?')
			.format(record.type, record.name || '@')),
		E('div', { 'class': 'right' }, [
			button(_('Cancel'), '', ui.hideModal), ' ',
			button(_('Delete'), 'cbi-button-negative', function () {
				call('record-delete', {
					id: String(state.account.id), zone: state.zone, record: record.id
				})
					.then(function () { ui.hideModal(); loadRecords(state.zone); })
					.catch(function (err) { ui.hideModal(); notifyError(err); });
			})
		])
	]);
}

var accountsHolder = E('div', {});

function refreshAccounts() {
	return call('accounts').then(function (accounts) {
		state.accounts = accounts || [];
		dom.content(accountsHolder, accountsTable());
	}).catch(function (err) {
		dom.content(accountsHolder, E('div', { 'class': 'alert-message warning' },
			E('p', {}, err.message)));
	});
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
			call('providers').catch(function () { return []; }),
			call('accounts').catch(function (err) { return err; })
		]);
	},

	render: function (data) {
		state.providers = data[0] || [];

		if (data[1] instanceof Error) {
			return E('div', { 'class': 'alert-message warning' }, [
				E('p', {}, _('DNS management is unavailable.')),
				E('p', {}, data[1].message)
			]);
		}
		state.accounts = data[1] || [];

		dom.content(accountsHolder, accountsTable());

		return E('div', {}, [
			stylesheet(),
			E('h2', {}, _('DNS')),
			E('p', {}, _('Manages records and proves ownership when issuing certificates. Wildcards need one.')),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('Accounts')),
				accountsHolder,
				E('div', { 'style': 'margin-top:1em' },
					button(_('Add account'), 'cbi-button-add', function () {
						accountDialog(null);
					}))
			]),

			E('div', { 'class': 'cbi-section' }, recordsHolder)
		]);
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
