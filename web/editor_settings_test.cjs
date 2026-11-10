// Run with: node --test web/editor_settings_test.cjs
// Exercise the shipped event handlers without pretending to test browser layout
// or native undo support. Those require the manual browser checks.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

function editorHarness() {
    const handlers = {};
    const nodes = [];
    let active, inputs = 0;
    class Element extends EventTarget {
        constructor() {
            super();
            this.attrs = {};
            this.style = {};
            this.classes = new Set();
            this.classList = {
                add: name => this.classes.add(name),
                remove: name => this.classes.delete(name),
                toggle: (name, on) => on ? this.classes.add(name) : this.classes.delete(name)
            };
            this.value = '';
            this.selectionStart = this.selectionEnd = 0;
            this.selectionDirection = 'none';
            this.scrollTop = this.scrollLeft = 0;
        }
        setAttribute(name, value) { this.attrs[name] = value; }
        getAttribute(name) { return this.attrs[name]; }
        appendChild() {}
        after() {}
        setSelectionRange(start, end, direction = 'none') {
            this.selectionStart = start;
            this.selectionEnd = end;
            this.selectionDirection = direction;
        }
        setRangeText(text, start, end) {
            this.value = this.value.slice(0, start) + text + this.value.slice(end);
        }
        closest() { return this.entry; }
        dispatchEvent(event) {
            super.dispatchEvent(event);
            if (event.type === 'input') inputs++;
            fire(event.type, { target: this, isTrusted: false });
            return true;
        }
    }
    function fire(type, event) {
        for (const handler of handlers[type] || []) handler(event);
    }
    const document = {
        addEventListener(type, fn) { (handlers[type] ||= []).push(fn); },
        querySelectorAll() { return []; },
        querySelector() { return null; },
        createElement() { return new Element(); },
        getElementById(id) {
            if (id !== 'editorSettingsTemplate') return null;
            return { content: { cloneNode() {
                const button = new Element(), panel = new Element();
                button.setAttribute('aria-expanded', 'false');
                panel.classList.add('hidden');
                nodes.push({ button, panel });
                return { querySelector: selector => selector === '.editor-settings-toggle' ? button : panel };
            } } };
        },
        execCommand(command, ui, text) {
            assert.equal(command, 'insertText');
            active.setRangeText(text, active.selectionStart, active.selectionEnd);
            active.dispatchEvent(new Event('input'));
            return true;
        }
    };
    const observers = [];
    class Observer {
        constructor() { observers.push(this); }
        observe() {}
        disconnect() { this.disconnected = true; }
    }
    const window = {};
    vm.runInNewContext(readFileSync(`${__dirname}/assets/app.js`, 'utf8'), {
        document, window, Event, ResizeObserver: Observer, MutationObserver: Observer,
        requestAnimationFrame: () => 1, cancelAnimationFrame() {}
    });
    function add(value = '') {
        const ta = new Element();
        ta.tagName = 'TEXTAREA';
        ta.value = value;
        const entry = { _dropped: true, querySelector(selector) {
            if (selector.includes('textarea')) return ta;
            return new Element();
        } };
        ta.entry = entry;
        ta.parentNode = { appendChild() {}, querySelector: () => new Element() };
        window.gistInitEditor(entry);
        return { ta, entry, ...nodes.at(-1) };
    }
    function setting(editor, name, value) {
        fire('change', { target: { dataset: { editorSetting: name }, value, closest: () => editor.entry } });
    }
    function key(editor, overrides = {}) {
        active = editor.ta;
        let prevented = false;
        fire('keydown', { target: active, key: 'Tab', preventDefault() { prevented = true; }, ...overrides });
        return prevented;
    }
    return { add, setting, key, window, document, observers, inputs: () => inputs };
}

test('settings are independent, unnamed display changes do not emit input, panel IDs are unique', () => {
    const h = editorHarness(), a = h.add('abc'), b = h.add('xyz');
    assert.notEqual(a.panel.id, b.panel.id);
    assert.equal(a.button.getAttribute('aria-controls'), a.panel.id);
    h.setting(a, 'wrap', 'soft');
    h.setting(a, 'size', '8');
    assert.equal(a.ta.wrap, 'soft');
    assert.equal(a.ta.style.tabSize, 8);
    assert.equal(a.ta.value, 'abc');
    assert.equal(h.inputs(), 0);
    h.key(b);
    assert.equal(b.ta.value, '  xyz');
    h.window.gistDestroyEditor(a.entry);
    assert.ok(h.observers.slice(0, 2).every(o => o.disconnected));
});

test('spaces advance to the next stop and literal tabs use the chosen width', () => {
    const h = editorHarness(), a = h.add('\tx');
    h.setting(a, 'size', '4');
    a.ta.setSelectionRange(2, 2);
    h.key(a);
    assert.equal(a.ta.value, '\tx   ');
    assert.equal(a.ta.selectionStart, 5);
    assert.equal(a.entry._dropped, false);
    assert.equal(h.inputs(), 1);
    h.setting(a, 'indent', 'tabs');
    h.key(a);
    assert.equal(a.ta.value, '\tx   \t');
});

test('multiline selection excludes the line beginning at the selection end', () => {
    const h = editorHarness(), a = h.add('a\nb\nc');
    a.ta.setSelectionRange(0, 4, 'backward');
    h.key(a);
    assert.equal(a.ta.value, '  a\n  b\nc');
    assert.equal(a.ta.selectionStart, 2);
    assert.equal(a.ta.selectionEnd, 8);
    assert.equal(a.ta.selectionDirection, 'backward');
    h.key(a, { shiftKey: true });
    assert.equal(a.ta.value, 'a\nb\nc');
    assert.equal(a.ta.selectionStart, 0);
    assert.equal(a.ta.selectionEnd, 4);
});

test('outdent handles mixed indentation, partial prefixes, and a caret in the prefix', () => {
    const h = editorHarness(), a = h.add(' x\n\ty\n    z');
    a.ta.setSelectionRange(0, a.ta.value.length);
    h.key(a, { shiftKey: true });
    assert.equal(a.ta.value, 'x\ny\n  z');
    a.ta.setSelectionRange(5, 5);
    h.key(a, { shiftKey: true });
    assert.equal(a.ta.value, 'x\ny\nz');
    assert.equal(a.ta.selectionStart, 4);
});

test('Tab at the very start of an empty first line edits that line', () => {
    const h = editorHarness(), a = h.add('\nx');
    h.key(a);
    assert.equal(a.ta.value, '  \nx');
    a.ta.setSelectionRange(0, 0);
    h.key(a, { shiftKey: true });
    assert.equal(a.ta.value, '\nx');
});

test('composition and modified shortcuts pass through; Escape releases the next Tab', () => {
    const h = editorHarness(), a = h.add();
    for (const modifier of ['isComposing', 'ctrlKey', 'metaKey', 'altKey']) {
        assert.equal(h.key(a, { [modifier]: true }), false);
    }
    h.key(a, { key: 'Escape' });
    assert.equal(h.key(a), false);
    assert.equal(a.ta.value, '');
    assert.equal(h.key(a), true);
    assert.equal(a.ta.value, '  ');
});

test('unsupported native insertion falls back and still emits an input event', () => {
    const h = editorHarness(), a = h.add('x');
    h.document.execCommand = () => false;
    h.key(a);
    assert.equal(a.ta.value, '  x');
    assert.equal(h.inputs(), 1);
});
