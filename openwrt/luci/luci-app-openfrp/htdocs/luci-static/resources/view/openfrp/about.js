'use strict';
'require view';
'require rpc';

/*
 * About.
 *
 * A static page: the project's links, and the version actually running. It
 * asks the backend for nothing but the status it already serves, so it works
 * on a router with no internet and renders the same whether or not the daemon
 * is up.
 */

var callStatus = rpc.declare({
	object: 'luci.openfrp',
	method: 'status',
	expect: {}
});

// Where the project lives, and where its author does. Ordered with the code
// first: someone who reached this page from a router is far likelier to want
// the repository than the videos.
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

// link renders an anchor, or plain text for the accounts that are handles
// rather than addresses.
//
// rel is not decoration: without noopener a new tab keeps a handle on this
// one through window.opener, and this page is served from the router's admin
// session.
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
		// A failure here is not worth an error page: the links are the point
		// and they do not depend on it.
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
