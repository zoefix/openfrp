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

// What the thing is, stated once, in the one place where detail is the
// point. Form hints go the other way — short, because their reader is making
// a choice — but nobody arrives here except to find out what they are running.
var DESIGN = [
	[_('Data plane'),
	 _('splice(2) on Linux: payload moves kernel to kernel and never enters this process.')],
	[_('Connections'),
	 _('One TCP connection per tunnel by default, each with its own congestion window, so a lost packet stalls only the tunnel that lost it.')],
	[_('Concurrency'),
	 _('One accept loop per CPU over SO_REUSEPORT, and socket options set once at bind rather than on every connection.')],
	[_('Bursts'),
	 _('When the warm pool empties, visitors are served over a multiplexed carrier that is already open, instead of waiting for a new connection.')],
	[_('Routing'),
	 _('Wildcards at any depth, matched by prefix tree, with exact names taking precedence.')],
	[_('Certificates'),
	 _('Issued and renewed here, then pushed to the server and loaded without dropping a connection.')],
	[_('Servers'),
	 _('One client serves several at once; one going down does not disturb the rest.')],
	[_('Portability'),
	 _('A single static binary with no libc dependency, for x86_64, ARM, RISC-V, LoongArch and MIPS.')]
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
			E('p', {}, _('OpenFrp publishes a service on this network to the ' +
				'internet, through a server of your own. It is written in Go, ' +
				'and built so that the bytes it carries stay out of its way.')),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('How it works')),
				E('table', { 'class': 'table' },
					DESIGN.map(function (entry) {
						return row(entry[0], entry[1]);
					}))
			]),

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
