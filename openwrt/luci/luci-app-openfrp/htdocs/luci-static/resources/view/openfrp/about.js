'use strict';
'require view';
'require rpc';

var callStatus = rpc.declare({
	object: 'luci.openfrp',
	method: 'status',
	expect: {}
});

var LINKS = [
	{ label: 'GitHub', href: 'https://github.com/zoefix/openfrp', text: 'zoefix/openfrp' },
	{ label: 'Bilibili', href: 'https://space.bilibili.com/17415536', text: 'space.bilibili.com/17415536' },
	{ label: 'YouTube', href: 'https://www.youtube.com/@zoefyx', text: '@zoefyx' },
	{ label: 'X', href: 'https://x.com/zoefech', text: '@zoefech' },
	{ label: _('Douyin'), text: 'zoefix' },
	{ label: _('Xiaohongshu'), text: 'zoefix' }
];

function row(label, value) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'style': 'width:30%;white-space:nowrap' }, label),
		E('td', { 'class': 'td left' }, value)
	]);
}

function link(entry) {
	if (!entry.href)
		return entry.text;

	return E('a', {
		'href': entry.href,
		'target': '_blank',
		'rel': 'noopener noreferrer'
	}, entry.text);
}

function stylesheet() {
	return E('link', {
		'rel': 'stylesheet',
		'href': L.resource('openfrp/openfrp.css')
	});
}

return view.extend({
	load: function () {
		return callStatus().catch(function () { return null; });
	},

	render: function (status) {
		var version = (status && status.client_version)
			? E('code', {}, status.client_version)
			: E('em', {}, _('unknown'));

		return E('div', {}, [
			stylesheet(),
			E('h2', {}, _('About')),
			E('p', {}, _('A high-performance NAT traversal service, written in Go ' +
				'on Linux zero-copy, with wildcard domain routing and automatic ' +
				'certificates.')),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('Version')),
				E('table', { 'class': 'table' }, [row(_('Client'), version)])
			]),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('Project')),
				E('table', { 'class': 'table' },
					LINKS.map(function (entry) {
						return row(entry.label, link(entry));
					}))
			])
		]);
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
