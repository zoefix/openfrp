'use strict';
'require baseclass';
'require ui';

/*
 * A renderer for schema-driven provider forms.
 *
 * Nineteen DNS providers each need a credential form, and nineteen hand-written
 * forms would be nineteen chances to drift out of step with the daemon. Instead
 * each provider declares its fields in Go, the local API serves them as JSON,
 * and this file draws all of them. Adding a provider touches no UI code at all.
 *
 * The condition grammar mirrors pkg/schema/condition.go exactly. Keeping the
 * two in step matters: the daemon is the authority, so a field this renderer
 * shows but the daemon considers hidden would be silently discarded on save.
 */

// Evaluate a ShowIf condition. Grammar, in full:
//   expr := term ("||" term)*
//   term := atom ("&&" atom)*
//   atom := field ("==" | "!=") literal
function evaluateCondition(condition, values) {
	if (!condition || !condition.trim())
		return true;

	return splitTop(condition, '||').some(function (term) {
		return splitTop(term, '&&').every(function (atom) {
			return evaluateAtom(atom, values);
		});
	});
}

function evaluateAtom(atom, values) {
	var ops = ['!=', '=='];

	for (var i = 0; i < ops.length; i++) {
		var idx = atom.indexOf(ops[i]);
		if (idx < 0)
			continue;

		var name = atom.slice(0, idx).trim();
		var want = unquote(atom.slice(idx + 2).trim());
		var got = values[name] != null ? String(values[name]) : '';

		return ops[i] === '==' ? got === want : got !== want;
	}

	// An unparseable condition hides the field rather than showing it: a
	// field shown by mistake invites a value the daemon will reject.
	return false;
}

// Split on a separator, skipping over quoted literals that may contain it.
function splitTop(input, sep) {
	var parts = [], start = 0, quote = null;

	for (var i = 0; i < input.length; i++) {
		var c = input[i];

		if (quote) {
			if (c === quote) quote = null;
			continue;
		}
		if (c === '"' || c === "'") {
			quote = c;
			continue;
		}
		if (input.startsWith(sep, i)) {
			parts.push(input.slice(start, i));
			i += sep.length - 1;
			start = i + 1;
		}
	}

	parts.push(input.slice(start));
	return parts;
}

function unquote(value) {
	if (value.length >= 2) {
		var first = value[0], last = value[value.length - 1];
		if ((first === "'" && last === "'") || (first === '"' && last === '"'))
			return value.slice(1, -1);
	}
	return value;
}

return baseclass.extend({
	evaluateCondition: evaluateCondition,

	/*
	 * Render a form and return { node, values, validate }.
	 *
	 * `values` is live: reading it after the user edits reflects the current
	 * state. `validate` returns null when the form is acceptable, or a message
	 * naming the offending field.
	 */
	render: function (schema, initial) {
		var values = Object.assign({}, initial || {});
		var rows = [];
		var self = this;

		(schema.fields || []).forEach(function (field) {
			if (values[field.name] == null && field.default != null)
				values[field.name] = field.default;
		});

		function refresh() {
			rows.forEach(function (row) {
				var visible = evaluateCondition(row.field.show_if, values);
				row.node.style.display = visible ? '' : 'none';
				row.visible = visible;
			});
		}

		(schema.fields || []).forEach(function (field) {
			var input;

			switch (field.kind) {
				case 'select':
					input = E('select', { 'class': 'cbi-input-select' },
						(field.options || []).map(function (option) {
							return E('option', {
								'value': option.value,
								'selected': values[field.name] === option.value ? '' : null
							}, option.label || option.value);
						}));
					break;

				case 'bool':
					input = E('input', {
						'type': 'checkbox',
						'class': 'cbi-input-checkbox',
						'checked': values[field.name] === '1' ? '' : null
					});
					break;

				case 'textarea':
					input = E('textarea', {
						'class': 'cbi-input-textarea',
						'rows': 4,
						'placeholder': field.placeholder || ''
					}, values[field.name] || '');
					break;

				default:
					input = E('input', {
						'type': field.kind === 'password' ? 'password'
							: field.kind === 'number' ? 'number' : 'text',
						'class': 'cbi-input-text',
						'placeholder': field.placeholder || '',
						'value': values[field.name] || ''
					});
			}

			input.addEventListener('change', function () {
				values[field.name] = field.kind === 'bool'
					? (input.checked ? '1' : '0')
					: input.value;
				refresh();
			});
			input.addEventListener('input', function () {
				if (field.kind !== 'bool') values[field.name] = input.value;
			});

			var help = [];
			if (field.help)
				help.push(E('div', { 'class': 'cbi-value-description' }, field.help));

			// A stored secret is never sent back to the browser, so the field
			// arrives blank on an edit. Say so, rather than letting the user
			// think the credential was lost.
			if (field.secret && initial && initial[field.name] === undefined)
				help.push(E('div', { 'class': 'cbi-value-description' },
					E('em', {}, _('Stored credentials are never displayed. Leave blank to keep the current value.'))));

			var node = E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' },
					field.label + (field.required ? ' *' : '')),
				E('div', { 'class': 'cbi-value-field' }, [input].concat(help))
			]);

			rows.push({ field: field, node: node, input: input, visible: true });
		});

		refresh();

		return {
			node: E('div', { 'class': 'cbi-section' }, rows.map(function (r) { return r.node; })),
			values: values,

			validate: function () {
				for (var i = 0; i < rows.length; i++) {
					var row = rows[i];
					if (!row.visible)
						continue;

					var value = (values[row.field.name] || '').trim();

					if (!value) {
						// A blank secret on an edit means "keep what is stored".
						if (row.field.required && !(row.field.secret && initial))
							return _('%s is required').format(row.field.label);
						continue;
					}
					if (row.field.pattern && !new RegExp(row.field.pattern).test(value))
						return _('%s is not in the expected format').format(row.field.label);
				}
				return null;
			}
		};
	}
});
