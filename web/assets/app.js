// Shared copy behaviors for the view page. File-level buttons declare
// their source in the markup (data-copy-url, rendered by the filebox
// partial); fenced code blocks inside sanitized markdown get their button
// injected here, since user-submitted content carries no scripts or
// buttons of its own.
(function () {
    function copyText(text, done) {
        if (navigator.clipboard) {
            navigator.clipboard.writeText(text).then(done);
            return;
        }
        // http on a non-localhost host has no Clipboard API.
        var ta = document.createElement('textarea');
        ta.value = text;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        ta.remove();
        done();
    }

    function flash(btn) {
        // Swap only the label, never the whole button: its leading icon
        // must survive the "Copied" message.
        var label = btn.querySelector('.i-label') || btn;
        var old = label.textContent;
        label.textContent = 'Copied';
        setTimeout(function () { label.textContent = old; }, 1500);
    }

    // File Copy buttons: fetch the exact stored content, not the rendered DOM.
    document.addEventListener('click', function (e) {
        var btn = e.target.closest('[data-copy-url]');
        if (!btn) return;
        fetch(btn.getAttribute('data-copy-url'), { credentials: 'same-origin' })
            .then(function (resp) { return resp.ok ? resp.text() : null; })
            .then(function (text) {
                if (text === null) return;
                copyText(text, function () { flash(btn); });
            });
    });

    // Per-file settings live on the textarea, so reordering never changes
    // them and no editor preferences enter the submitted gist.
    var editors = new WeakMap();
    var nextEditorID = 0;
    function scheduleEditor(ta) {
        var state = editors.get(ta);
        if (!state || state.frame) return;
        state.frame = requestAnimationFrame(function () {
            state.frame = 0;
            layoutEditor(ta, state);
        });
    }
    function layoutEditor(ta, state) {
        var visible = ta.getClientRects().length > 0;
        state.button.classList.toggle('hidden', !visible);
        if (!visible) {
            state.button.setAttribute('aria-expanded', 'false');
            state.panel.classList.add('hidden');
            return;
        }
        var gutter = state.gutter;
        var lines = ta.value.split('\n');
        var style = getComputedStyle(ta);
        // Reserve a stable width before measuring wraps, including the change
        // from e.g. 99 to 100 lines. A ch is a monospace digit's width.
        var digits = String(lines.length).length;
        gutter.style.width = 'calc(' + Math.max(2, digits) + 'ch + .8rem + 1px)';
        var gutterWidth = gutter.getBoundingClientRect().width;
        ta.style.marginLeft = gutterWidth + 'px';
        ta.style.width = 'calc(100% - ' + gutterWidth + 'px)';
        var scrollbar = ta.offsetHeight - ta.clientHeight - parseFloat(style.borderTopWidth) - parseFloat(style.borderBottomWidth);
        gutter.style.bottom = Math.max(0, scrollbar) + 'px';
        var width = ta.getBoundingClientRect().width - (ta.offsetWidth - ta.clientWidth) -
            parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
        var key = [width, ta.clientHeight, style.font, style.lineHeight, style.letterSpacing, state.wrap, state.size].join('|');
        var content = state.wrap === 'soft' ? ta.value : lines.length;
        if (state.content === content && state.layoutKey === key) {
            gutter.scrollTop = ta.scrollTop;
            return;
        }
        state.content = content;
        state.layoutKey = key;
        var numbers = document.createDocumentFragment();
        var heights;
        if (state.wrap === 'soft') {
            var mirror = state.mirror;
            mirror.style.width = Math.max(0, width) + 'px';
            mirror.style.font = style.font;
            mirror.style.lineHeight = style.lineHeight;
            mirror.style.letterSpacing = style.letterSpacing;
            mirror.style.tabSize = state.size;
            var measured = document.createDocumentFragment();
            lines.forEach(function (line) {
                var row = document.createElement('div');
                row.className = 'editor-mirror-line';
                row.textContent = line || '\u200b';
                measured.appendChild(row);
            });
            mirror.replaceChildren(measured);
            // Read all heights together, after writing the measurement DOM.
            heights = Array.from(mirror.children, function (row) { return row.getBoundingClientRect().height; });
            mirror.replaceChildren();
        }
        lines.forEach(function (_, i) {
            var row = document.createElement('span');
            row.className = 'lnum-line';
            row.textContent = i + 1;
            row.style.height = (heights ? heights[i] : parseFloat(style.lineHeight)) + 'px';
            numbers.appendChild(row);
        });
        // Match the textarea's scrollable extent, including bottom padding,
        // so the gutter remains aligned at the very bottom of the document.
        var bottom = document.createElement('span');
        bottom.className = 'lnum-line';
        bottom.style.height = Math.max(0, ta.scrollHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom) -
            (heights ? heights.reduce(function (a, b) { return a + b; }, 0) : lines.length * parseFloat(style.lineHeight))) + 'px';
        numbers.appendChild(bottom);
        gutter.replaceChildren(numbers);
        gutter.scrollTop = ta.scrollTop;
    }
    function initEditor(entry) {
        var ta = entry.querySelector('.editor textarea');
        var template = document.getElementById('editorSettingsTemplate');
        if (!ta || !template || editors.has(ta)) return;
        var controls = template.content.cloneNode(true);
        var button = controls.querySelector('.editor-settings-toggle');
        var panel = controls.querySelector('.editor-settings');
        panel.id = 'editor-settings-' + (++nextEditorID);
        button.setAttribute('aria-controls', panel.id);
        entry.querySelector('.editor-settings-slot').appendChild(button);
        entry.querySelector('.file-entry-head').after(panel);
        var mirror = document.createElement('div');
        mirror.className = 'editor-mirror';
        mirror.setAttribute('aria-hidden', 'true');
        ta.parentNode.appendChild(mirror);
        var state = {
            button: button, panel: panel, mirror: mirror,
            gutter: ta.parentNode.querySelector('.lnum'),
            wrap: 'off', indent: 'spaces', size: 2, frame: 0
        };
        editors.set(ta, state);
        state.resize = new ResizeObserver(function () { scheduleEditor(ta); });
        state.resize.observe(ta);
        state.visibility = new MutationObserver(function () { scheduleEditor(ta); });
        state.visibility.observe(ta, { attributes: true, attributeFilter: ['class'] });
        scheduleEditor(ta);
    }
    window.gistInitEditor = initEditor;
    window.gistDestroyEditor = function (entry) {
        var ta = entry.querySelector('.editor textarea');
        var state = editors.get(ta);
        if (!state) return;
        cancelAnimationFrame(state.frame);
        state.resize.disconnect();
        state.visibility.disconnect();
        editors.delete(ta);
    };
    document.querySelectorAll('.file-entry').forEach(initEditor);
    if (document.fonts) document.fonts.ready.then(function () {
        document.querySelectorAll('.editor textarea').forEach(function (ta) {
            var state = editors.get(ta);
            if (state) state.layoutKey = null;
            scheduleEditor(ta);
        });
    });
    document.addEventListener('click', function (e) {
        var button = e.target.closest('.editor-settings-toggle');
        if (!button) return;
        var state = editors.get(button.closest('.file-entry').querySelector('textarea'));
        var open = button.getAttribute('aria-expanded') !== 'true';
        button.setAttribute('aria-expanded', String(open));
        state.panel.classList.toggle('hidden', !open);
    });
    document.addEventListener('change', function (e) {
        var setting = e.target.dataset.editorSetting;
        if (!setting) return;
        var ta = e.target.closest('.file-entry').querySelector('textarea');
        var state = editors.get(ta);
        state[setting] = setting === 'size' ? Number(e.target.value) : e.target.value;
        ta.wrap = state.wrap;
        ta.style.tabSize = state.size;
        if (state.wrap === 'soft') ta.scrollLeft = 0;
        scheduleEditor(ta);
    });
    document.addEventListener('input', function (e) { scheduleEditor(e.target); });
    document.addEventListener('focusin', function (e) { scheduleEditor(e.target); });
    // Scrolling needs no text measurement or line counting.
    document.addEventListener('scroll', function (e) {
        var state = editors.get(e.target);
        if (state) state.gutter.scrollTop = e.target.scrollTop;
    }, true);

    function indentEdit(ta, state, outdent) {
        var text = ta.value;
        var start = ta.selectionStart, end = ta.selectionEnd;
        var direction = ta.selectionDirection;
        var lineStart = text.slice(0, start).lastIndexOf('\n') + 1;
        if (!outdent && start === end) {
            var column = 0;
            for (var c of text.slice(lineStart, start)) {
                column += c === '\t' ? state.size - column % state.size : 1;
            }
            var insertion = state.indent === 'tabs' ? '\t' : ' '.repeat(state.size - column % state.size);
            return { start: start, end: end, text: insertion,
                selectionStart: start + insertion.length, selectionEnd: start + insertion.length, direction: direction };
        }
        var last = end > start && text[end - 1] === '\n' ? end - 1 : end;
        var lineEnd = text.indexOf('\n', last);
        if (lineEnd < 0) lineEnd = text.length;
        var lines = text.slice(lineStart, lineEnd).split('\n');
        var edits = [], offset = lineStart;
        var replacement = lines.map(function (line) {
            var remove = 0, insert = '';
            if (outdent) {
                if (line[0] === '\t') remove = 1;
                else {
                    while (remove < state.size && line[remove] === ' ') remove++;
                }
            } else insert = state.indent === 'tabs' ? '\t' : ' '.repeat(state.size);
            edits.push({ offset: offset, remove: remove, insert: insert.length });
            offset += line.length + 1;
            return insert + line.slice(remove);
        }).join('\n');
        function adjusted(position) {
            var delta = 0;
            edits.forEach(function (edit) {
                if (position >= edit.offset) delta += edit.insert - Math.min(edit.remove, position - edit.offset);
            });
            return position + delta;
        }
        return { start: lineStart, end: lineEnd, text: replacement,
            selectionStart: adjusted(start), selectionEnd: adjusted(end), direction: direction };
    }
    document.addEventListener('keydown', function (e) {
        var ta = e.target, state = editors.get(ta);
        if (!state || e.isComposing || e.ctrlKey || e.metaKey || e.altKey) return;
        if (e.key === 'Escape') { state.escapeTab = true; return; }
        if (e.key !== 'Tab') { state.escapeTab = false; return; }
        if (state.escapeTab) { state.escapeTab = false; return; }
        e.preventDefault();
        var edit = indentEdit(ta, state, e.shiftKey);
        if (ta.value.slice(edit.start, edit.end) === edit.text) return;
        var top = ta.scrollTop, left = ta.scrollLeft;
        ta.setSelectionRange(edit.start, edit.end);
        // insertText keeps textarea edits in the browser's native undo stack.
        // setRangeText is the compatibility fallback when the command is absent.
        var before = ta.value;
        var inputSeen = false;
        var onInput = function () { inputSeen = true; };
        ta.addEventListener('input', onInput);
        try { document.execCommand('insertText', false, edit.text); } catch (_) { /* fallback below */ }
        ta.removeEventListener('input', onInput);
        if (ta.value === before) ta.setRangeText(edit.text, edit.start, edit.end, 'end');
        ta.setSelectionRange(edit.selectionStart, edit.selectionEnd, edit.direction);
        ta.scrollTop = top;
        ta.scrollLeft = left;
        var entry = ta.closest('.file-entry');
        if (entry) entry._dropped = false;
        if (!inputSeen) ta.dispatchEvent(new Event('input', { bubbles: true }));
    });
    document.addEventListener('focusout', function (e) {
        var state = editors.get(e.target);
        if (state) state.escapeTab = false;
    });

    // Fenced code blocks: a button per block.
    document.querySelectorAll('.file-body .codeblock').forEach(function (block) {
        var pre = block.querySelector('pre');
        if (!pre) return;
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'copy-block';
        btn.innerHTML = '<svg class="i" aria-hidden="true"><use href="#i-copy"/></svg><span class="i-label">Copy</span>';
        btn.addEventListener('click', function () {
            copyText(pre.textContent, function () { flash(btn); });
        });
        block.appendChild(btn);
    });

    // --- Edit page: Edit | Preview tabs per file entry. Preview POSTs the
    // current draft (including a just-typed file name) to /preview and shows
    // the server-rendered result, so the preview is exactly what saving
    // produces. MathJax/Mermaid load lazily, only if a preview needs them.
    var vendorLoads = {};
    function loadScript(src) {
        if (!vendorLoads[src]) {
            vendorLoads[src] = new Promise(function (resolve, reject) {
                var s = document.createElement('script');
                s.src = src;
                s.defer = true;
                s.onload = resolve;
                s.onerror = function () { reject(new Error('failed to load ' + src)); };
                document.head.appendChild(s);
            });
        }
        return vendorLoads[src];
    }
    function ensureMath() {
        if (!window.MathJax) {
            window.MathJax = {
                tex: {
                    inlineMath: [['\\(', '\\)']],
                    displayMath: [['\\[', '\\]']]
                },
                svg: { fontCache: 'global' }
            };
        }
        return loadScript('/static/vendor/tex-svg.js');
    }
    // Preview diagrams are run explicitly: startOnLoad stays off because a
    // preview pane is re-rendered on every click of the Preview tab, and
    // mermaid.run takes the nodes to render. securityLevel 'strict' keeps the
    // diagram text, which is untrusted, from emitting html labels or links.
    // The view page needs no equivalent call: its script tag initializes
    // mermaid with startOnLoad on.
    function ensureMermaid() {
        return loadScript('/static/vendor/mermaid.min.js').then(function () {
            window.mermaid.initialize({ startOnLoad: false, securityLevel: 'strict' });
        });
    }

    // --- Edit page: Edit | Preview tabs per file entry, shown only for
    // Markdown names (keep this set in sync with models.markdownExts). The
    // server renders the initial visibility; typing a name updates it live.
    var MD_RE = /\.(md|markdown|mdown|mdwn)$/i;
    function applyTabs(entry) {
        var tabs = entry.querySelector('.tabs');
        var nameInput = entry.querySelector('[name$="_name"]');
        if (tabs && nameInput) {
            tabs.classList.toggle('hidden', !MD_RE.test(nameInput.value));
        }
    }
    document.querySelectorAll('.file-entry').forEach(applyTabs);
    document.addEventListener('input', function (e) {
        if (e.target.matches && e.target.matches('[name$="_name"]')) {
            applyTabs(e.target.closest('.file-entry'));
        }
    });

    document.addEventListener('click', function (e) {
        var tab = e.target.closest('.tab');
        if (!tab) return;
        var entry = tab.closest('.file-entry');
        var pane = entry.querySelector('.preview-pane');
        var ta = entry.querySelector('textarea');
        entry.querySelectorAll('.tab').forEach(function (t) {
            t.classList.toggle('is-active', t === tab);
        });
        if (tab.getAttribute('data-mode') === 'edit') {
            pane.classList.add('hidden');
            ta.classList.remove('hidden');
            return;
        }
        ta.classList.add('hidden');
        pane.classList.remove('hidden');
        var body = new URLSearchParams({
            csrf_token: document.querySelector('#gistForm [name=csrf_token]').value,
            name: entry.querySelector('[name$="_name"]').value,
            content: ta.value
        });
        pane.textContent = 'Rendering…';
        fetch('/preview', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: body
        })
            .then(function (resp) {
                if (!resp.ok) throw new Error('preview failed (HTTP ' + resp.status + ')');
                return resp.json();
            })
            .then(function (data) {
                pane.innerHTML = data.html;
                if (data.needsMath) {
                    ensureMath().then(function () { window.MathJax.typesetPromise([pane]); });
                }
                if (data.needsMermaid) {
                    ensureMermaid().then(function () {
                        window.mermaid.run({ nodes: pane.querySelectorAll('pre.mermaid') });
                    });
                }
            })
            .catch(function (err) {
                pane.textContent = err.message;
            });
    });
    // --- Reorder file entries by the # grip (edit page). Grabbing the
    // grip makes the row draggable; during a move the row follows the
    // pointer between siblings; on release the file{i}_* fields are
    // renumbered in DOM order, so saving stores exactly what you see.
    var filesWrap = document.getElementById('files');
    var moving = null;
    if (filesWrap) {
        document.querySelectorAll('.grip').forEach(function (g) {
            g.classList.remove('hidden');
        });
        var renumber = function () {
            Array.prototype.forEach.call(filesWrap.children, function (entry, i) {
                Array.prototype.forEach.call(entry.querySelectorAll('[name]'), function (el) {
                    el.name = el.name.replace(/^file\d+/, 'file' + i);
                });
            });
        };
        var clearDraggable = function () {
            Array.prototype.forEach.call(filesWrap.querySelectorAll('[draggable]'), function (el) {
                el.removeAttribute('draggable');
            });
        };
        document.addEventListener('mousedown', function (e) {
            var grip = e.target.closest && e.target.closest('.grip');
            if (grip) {
                var row = grip.closest('.file-entry');
                if (row) row.setAttribute('draggable', 'true');
            }
        });
        document.addEventListener('mouseup', clearDraggable);
        document.addEventListener('dragstart', function (e) {
            var entry = e.target.closest && e.target.closest('.file-entry');
            if (entry && entry.getAttribute('draggable') === 'true') {
                moving = entry;
                entry.classList.add('dragging');
                if (e.dataTransfer) {
                    e.dataTransfer.effectAllowed = 'move';
                    e.dataTransfer.setData('text/plain', ''); // Firefox
                }
            }
        });
        document.addEventListener('dragover', function (e) {
            if (!moving) return;
            e.preventDefault();
            var target = e.target.closest && e.target.closest('.file-entry');
            if (!target || target === moving) return;
            var rect = target.getBoundingClientRect();
            var after = e.clientY > rect.top + rect.height / 2;
            filesWrap.insertBefore(moving, after ? target.nextSibling : target);
        });
        document.addEventListener('drop', function (e) {
            if (moving) e.preventDefault();
        });
        document.addEventListener('dragend', function () {
            if (!moving) return;
            moving.classList.remove('dragging');
            moving = null;
            clearDraggable();
            renumber();
        });
        document.getElementById('gistForm').addEventListener('submit', renumber);
    }

    // --- Attachments: dropped files. Text-ish files
    // (no NUL byte in the first 8 KiB, <= 8 MiB) fill the textarea;
    // anything else goes straight to S3 via /upload + presigned PUT and
    // turns the entry into an attached blob. Requires JS; without it the
    // form is unchanged. Landing-row routing lives at the bottom of this
    // file (page-wide drops).
    function fireInput(el) {
        el.dispatchEvent(new Event('input', { bubbles: true }));
    }
    function nameInput(entry) { return entry.querySelector('[name$="_name"]'); }
    function contentArea(entry) { return entry.querySelector('textarea'); }
    function keyInput(entry) {
        var el = entry.querySelector('[name$="_key"]');
        if (!el) {
            el = document.createElement('input');
            el.type = 'hidden';
            el.name = contentArea(entry).name.replace('_content', '_key');
            entry.appendChild(el);
        }
        return el;
    }
    function attachBadge(entry) {
        var badge = entry.querySelector('.attach-badge');
        if (!badge) {
            badge = document.createElement('div');
            badge.className = 'attach-badge';
            // insert at the editor wrapper: the textarea is nested in it now
            entry.insertBefore(badge, contentArea(entry).closest('.editor') || contentArea(entry));
        }
        return badge;
    }
    function humanBytes(n) {
        if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
        if (n >= 1024) return Math.round(n / 1024) + ' KB';
        return n + ' B';
    }
    function sniffBinary(file) {
        if (file.size > 8 * 1024 * 1024) return Promise.resolve(true);
        return file.slice(0, 8192).arrayBuffer().then(function (buf) {
            var u8 = new Uint8Array(buf);
            for (var i = 0; i < u8.length; i++) {
                if (u8[i] === 0) return true;
            }
            return false;
        });
    }
    function markAttached(entry, file, key) {
        keyInput(entry).value = key;
        var ni = nameInput(entry);
        if (ni && !ni.value) { ni.value = file.name; fireInput(ni); }
        fireInput(keyInput(entry)); // unsaved-changes guard sees the attach
        var ta = contentArea(entry);
        ta.value = '';
        ta.classList.add('hidden');
        var badge = attachBadge(entry);
        badge.classList.remove('hidden', 'is-error');
        badge.textContent = 'Attached: ' + file.name + ' · ' + humanBytes(file.size) + ' — drop a new file here to replace';
    }
    function xhrPut(url, contentType, file, onProgress) {
        return new Promise(function (resolve, reject) {
            var x = new XMLHttpRequest();
            x.open('PUT', url);
            x.setRequestHeader('Content-Type', contentType);
            x.upload.onprogress = function (e) {
                if (e.lengthComputable) onProgress(e.loaded / e.total);
            };
            x.onload = function () {
                x.status === 200 ? resolve() : reject(new Error('upload failed (HTTP ' + x.status + ')'));
            };
            x.onerror = function () { reject(new Error('upload failed (network)')); };
            x.send(file);
        });
    }
    function uploadAndAttach(entry, file) {
        var csrf = document.querySelector('#gistForm [name=csrf_token]').value;
        var badge = attachBadge(entry);
        badge.classList.remove('hidden', 'is-error');
        badge.textContent = 'Uploading ' + file.name + '…';
        fetch('/upload', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: new URLSearchParams({ csrf_token: csrf, name: file.name })
        })
            .then(function (r) {
                if (!r.ok) throw new Error('upload rejected (HTTP ' + r.status + ')');
                return r.json();
            })
            .then(function (up) {
                return xhrPut(up.url, up.contentType, file, function (frac) {
                    badge.textContent = 'Uploading ' + file.name + '… ' + Math.round(frac * 100) + '%';
                }).then(function () { markAttached(entry, file, up.key); });
            })
            .catch(function (err) {
                badge.classList.add('is-error');
                badge.textContent = err.message + ' — drop again to retry';
            });
    }
    function attachAsText(entry, file) {
        file.text().then(function (text) {
            var ta = contentArea(entry);
            ta.value = text;
            fireInput(ta);
            var key = keyInput(entry);
            if (key.value) {
                key.value = '';
                fireInput(key);
            }
            var badge = entry.querySelector('.attach-badge');
            if (badge) badge.classList.add('hidden');
            ta.classList.remove('hidden');
            var ni = nameInput(entry);
            if (ni && !ni.value) { ni.value = file.name; fireInput(ni); }
        });
    }
    function handleDroppedFile(entry, file) {
        entry._dropped = true;
        sniffBinary(file).then(function (binary) {
            binary ? uploadAndAttach(entry, file) : attachAsText(entry, file);
        });
    }
    // The next gesture of the same name may then overwrite the row without
    // asking, but only while its content is what that gesture put there: typing
    // in the row takes the permission back. A drop's own input events are
    // programmatic, so only a trusted event counts as typing.
    document.addEventListener('input', function (e) {
        if (!e.isTrusted || e.target.tagName !== 'TEXTAREA') return;
        var entry = e.target.closest && e.target.closest('.file-entry');
        if (entry) entry._dropped = false;
    });

    // --- Page-wide file drops and the drag preview. -----------------------
    // Drop on an entry: that entry is replaced (extras become new entries).
    // Drop anywhere else: a file goes to the entry that already carries its
    // name, else to the first still-empty entry, else to a new entry.
    // While dragging, the exact same plan is previewed: a page overlay with a
    // one-line sentence, the planned landing entry (.dragtarget), the hovered
    // entry (.dragover).
    function isEmptyEntry(entry) {
        var ni = nameInput(entry);
        var ta = contentArea(entry);
        var key = entry.querySelector('[name$="_key"]');
        return !(ni && ni.value.trim()) && !(ta && ta.value.trim()) && !(key && key.value);
    }
    function emptyEntries() {
        if (!filesWrap) return [];
        return Array.prototype.filter.call(
            filesWrap.querySelectorAll('.file-entry'), isEmptyEntry);
    }
    function plural(n, word) { return n + ' ' + word + (n === 1 ? '' : 's'); }
    function hasContent(entry) {
        var ta = contentArea(entry);
        var key = entry.querySelector('[name$="_key"]');
        return !!((ta && ta.value.trim()) || (key && key.value));
    }
    function entryNamed(name) {
        var wanted = (name || '').trim().toLowerCase();
        if (!wanted || !filesWrap) return null;
        var entries = filesWrap.querySelectorAll('.file-entry');
        for (var i = 0; i < entries.length; i++) {
            var ni = nameInput(entries[i]);
            if (ni && ni.value.trim().toLowerCase() === wanted) return entries[i];
        }
        return null;
    }
    // A file whose name an entry already carries belongs to that entry, so a
    // second gesture carrying it updates the row instead of submitting two
    // files under one name, which the server rejects. Name matching runs
    // before the empty-row search, so a name on the page always wins over a
    // free row. 'confirm' hands the row to the replace chip, because it holds
    // content no gesture put there; 'replace' and 'fill' overwrite silently.
    function planFiles(files) {
        var empties = emptyEntries(); // snapshot: async fills must not re-hit a row
        return files.map(function (file) {
            var match = entryNamed(file.name);
            if (match) {
                var quiet = match._dropped || !hasContent(match);
                return { file: file, entry: match, kind: quiet ? 'replace' : 'confirm' };
            }
            if (empties.length) return { file: file, entry: empties.shift(), kind: 'fill' };
            return { file: file, entry: null, kind: 'new' };
        });
    }
    function dragFileList(e) {
        var files = Array.prototype.slice.call(e.dataTransfer.files || []);
        if (!files.length) { // no names to resolve: preview a bare count
            var n = e.dataTransfer.items ? e.dataTransfer.items.length : 1;
            for (var i = 0; i < n; i++) files.push({ name: '' });
        }
        return files;
    }
    function planSentence(files) {
        var counts = { replace: 0, fill: 0, fresh: 0 };
        var seen = [];
        planFiles(files).forEach(function (p) {
            if (!p.entry) { counts.fresh++; return; }
            if (seen.indexOf(p.entry) >= 0) return; // same name twice, one row
            seen.push(p.entry);
            counts[p.kind === 'fill' ? 'fill' : 'replace']++;
        });
        var parts = [];
        if (counts.replace) parts.push('replace ' + plural(counts.replace, 'file'));
        if (counts.fill) parts.push('fill ' + plural(counts.fill, 'empty row'));
        if (counts.fresh) parts.push('add ' + plural(counts.fresh, 'new file'));
        return 'Will ' + parts.join(', ');
    }
    var overlay = null;
    function dropOverlay() {
        if (!overlay) {
            overlay = document.createElement('div');
            overlay.id = 'dropOverlay';
            overlay.innerHTML = '<div class="drop-hint"></div>';
            document.body.appendChild(overlay);
        }
        return overlay;
    }
    function planFileDrag(e) { // files (not a row move) over a page with entries
        return !moving && filesWrap && e.dataTransfer &&
            Array.prototype.indexOf.call(e.dataTransfer.types || [], 'Files') >= 0;
    }
    var planned = null; // entry previewed as the landing row
    var hovered = null; // entry the pointer is over
    var dragDepth = 0;
    function distributeFiles(files) { // page resolver, see planFiles
        planFiles(files).forEach(function (p) {
            if (p.kind === 'new') newEntry(p.file);
            else if (p.kind === 'confirm') gestureOnEntry(p.entry, [p.file]);
            else handleDroppedFile(p.entry, p.file);
        });
    }
    function plannedRow(plan) { // replacing a row beats an empty row for the hint
        var row = null;
        var fill = null;
        plan.forEach(function (p) {
            if (!p.entry) return;
            if (p.kind === 'fill') { if (!fill) fill = p.entry; }
            else if (!row) row = p.entry;
        });
        return row || fill;
    }
    function markPlanned(entry) {
        if (planned === entry) return;
        if (planned) planned.classList.remove('dragtarget');
        planned = entry;
        if (planned) planned.classList.add('dragtarget');
    }
    function markHovered(entry) {
        if (hovered === entry) return;
        if (hovered) hovered.classList.remove('dragover');
        hovered = entry;
        if (hovered) hovered.classList.add('dragover');
    }
    function clearFileDragUI() {
        if (overlay) overlay.classList.remove('active');
        markPlanned(null);
        markHovered(null);
        dragDepth = 0;
    }

    document.addEventListener('dragenter', function (e) {
        if (planFileDrag(e)) dragDepth++;
    });
    document.addEventListener('dragleave', function (e) {
        if (dragDepth > 0) dragDepth--; // types are empty on dragleave: count blindly
        if (dragDepth === 0) clearFileDragUI();
    });
    document.addEventListener('dragover', function (e) {
        if (!e.dataTransfer ||
            Array.prototype.indexOf.call(e.dataTransfer.types || [], 'Files') < 0) return;
        e.preventDefault(); // the page accepts files everywhere; never navigate
        if (!planFileDrag(e)) return;
        e.dataTransfer.dropEffect = 'copy';
        var entry = e.target.closest && e.target.closest('.file-entry');
        markHovered(entry || null);
        var ov = dropOverlay();
        if (entry) {
            markPlanned(null);
            ov.querySelector('.drop-hint').textContent =
                'Will replace ' + (nameInput(entry).value.trim() || 'this file');
        } else {
            var files = dragFileList(e);
            markPlanned(plannedRow(planFiles(files)));
            ov.querySelector('.drop-hint').textContent = planSentence(files);
        }
        ov.classList.add('active');
    });
    document.addEventListener('drop', function (e) {
        e.preventDefault(); // a stray file drop must never navigate the page
        if (!filesWrap || !e.dataTransfer || !e.dataTransfer.files.length) {
            clearFileDragUI();
            return;
        }
        var files = Array.prototype.slice.call(e.dataTransfer.files);
        var entry = e.target.closest && e.target.closest('.file-entry');
        if (entry) {
            gestureOnEntry(entry, files); // chips itself if it would clobber
        } else {
            distributeFiles(files);
        }
        clearFileDragUI();
    });
    document.addEventListener('dragend', clearFileDragUI);

    // --- Paste and the replace chip. ---------------------------------------
    // A paste carrying one file behaves like a drop on a row: it targets the
    // focused row. A paste carrying several files behaves like a drop on the
    // page (pointer is unknowable for a paste): each file goes to the entry
    // that already carries its name, else to the first still-empty entry, else
    // to a new entry. A gesture that would clobber a row arms a chip (Replace
    // / Add as new / Cancel) and waits; the exception is a same-name row whose
    // content a previous gesture put there, which is overwritten quietly. The
    // newest gesture on a row re-arms its chip, and saving is blocked until
    // every chip on the page is answered. Plain text pastes are never
    // intercepted.
    function newEntry(file) {
        var en = window.gistAddEntry ? window.gistAddEntry() : null;
        if (en) handleDroppedFile(en, file);
    }
    var pasteSeq = 0;
    function extFor(file) {
        var m = /^([a-z]+)\/([a-z0-9.+-]+)/i.exec(file.type || '');
        if (!m) return 'bin';
        var top = m[1].toLowerCase();
        var sub = m[2].split(/[+;]/)[0].toLowerCase();
        var named = { jpeg: 'jpg', markdown: 'md', plaintext: 'txt' };
        if (named[sub]) return named[sub];
        if (top === 'text') return 'txt';
        return sub;
    }
    function normalizePasted(file) { // screenshots arrive nameless; canvas/image
        if (file.name && file.name !== 'blob') return file; // copies surface as "blob"
        pasteSeq++;
        return new File([file], 'pasted-' + pasteSeq + '.' + extFor(file), { type: file.type });
    }
    function removeChip(entry) {
        var chip = entry.querySelector('.replace-chip');
        if (chip) chip.remove();
        entry._pendingFiles = null;
    }
    // A pending chip is a question the form cannot answer for the user, so
    // the Save button stays disabled while any chip is on screen. Chips are
    // client-only: an answered-or-absent chip never blocks a real save.
    function updateSaveGate() {
        var save = document.querySelector('#gistForm [type=submit]');
        if (!save) return;
        var pending = document.querySelectorAll('.replace-chip').length;
        save.disabled = pending > 0;
        save.title = pending ? 'Resolve the dropped-file prompt first' : '';
    }
    window.gistUpdateSaveGate = updateSaveGate; // used by edit.html's remove handler
    var gistForm = document.getElementById('gistForm');
    if (gistForm) gistForm.addEventListener('submit', function (e) {
        // disabled buttons do not block implicit submission from Enter
        if (document.querySelector('.replace-chip')) e.preventDefault();
    });
    function applyGesture(entry, files, mode) {
        if (mode === 'add') {
            files.forEach(newEntry);
            return;
        }
        handleDroppedFile(entry, files[0]);
        files.slice(1).forEach(newEntry);
    }
    function gestureOnEntry(entry, files) {
        if (isEmptyEntry(entry)) {
            applyGesture(entry, files, 'replace');
            return;
        }
        removeChip(entry);
        entry._pendingFiles = files;
        var chip = document.createElement('div');
        chip.className = 'replace-chip';
        var label = document.createElement('span');
        label.textContent = (files[0].name || 'pasted file') +
            (files.length > 1 ? ' +' + (files.length - 1) + ' more' : '') + ':';
        chip.appendChild(label);
        [['replace', 'Replace', 'btn btn-sm btn-primary'],
         ['add', 'Add as new', 'btn btn-sm btn-secondary'],
         ['cancel', 'Cancel', 'chip-btn']]
            .forEach(function (a) {
                var b = document.createElement('button');
                b.type = 'button';
                b.className = a[2];
                b.dataset.chip = a[0];
                b.textContent = a[1];
                chip.appendChild(b);
            });
        entry.insertBefore(chip, contentArea(entry).closest('.editor') || contentArea(entry));
        updateSaveGate();
    }
    document.addEventListener('click', function (e) {
        var btn = e.target.closest && e.target.closest('[data-chip]');
        if (!btn) return;
        var entry = btn.closest('.file-entry');
        var files = entry && entry._pendingFiles;
        if (!entry || !files) return;
        removeChip(entry);
        updateSaveGate();
        if (btn.dataset.chip !== 'cancel') applyGesture(entry, files, btn.dataset.chip);
    });
    function pastedFiles(e) {
        var cd = e.clipboardData;
        if (!cd) return [];
        var files = cd.files && cd.files.length ? Array.prototype.slice.call(cd.files) : [];
        if (!files.length && cd.items) { // copied images can hide in items only
            Array.prototype.forEach.call(cd.items, function (it) {
                if (it.kind === 'file') {
                    var f = it.getAsFile();
                    if (f) files.push(f);
                }
            });
        }
        return files;
    }
    document.addEventListener('paste', function (e) {
        var files = pastedFiles(e);
        if (!files.length) return; // plain text pastes normally, untouched
        e.preventDefault(); // keep blob: junk out of the textareas
        files = files.map(normalizePasted);
        var entry = files.length > 1 ? null : e.target.closest && e.target.closest('.file-entry');
        if (entry) {
            gestureOnEntry(entry, files);
        } else if (filesWrap) {
            distributeFiles(files);
        }
    });

    // --- Relative timestamps: "3 days ago" for every .rel-time element
    // (list rows, gist header). The absolute time stays in the title
    // tooltip and as the text until this runs.
    function relTime(iso) {
        var s = (Date.now() - new Date(iso).getTime()) / 1000;
        var units = [[31536000, 'year'], [2592000, 'month'], [86400, 'day'], [3600, 'hour'], [60, 'minute']];
        for (var i = 0; i < units.length; i++) {
            var n = Math.floor(s / units[i][0]);
            if (n >= 1) return n + ' ' + units[i][1] + (n > 1 ? 's' : '') + ' ago';
        }
        return 'just now';
    }
    document.querySelectorAll('time.rel-time').forEach(function (t) {
        t.textContent = relTime(t.getAttribute('datetime'));
    });

    // --- Theme toggle: system -> light -> dark, remembered in localStorage.
    // The head script already applied the stored choice before paint, so this
    // only paints the button and wires the cycle. "system" removes the
    // attribute instead of writing a value: app.css then resolves every
    // light-dark() token through the plain color-scheme default, which follows
    // the OS.
    var THEMES = [
        ['system', 'i-theme-auto', 'System'],
        ['light', 'i-theme-light', 'Light'],
        ['dark', 'i-theme-dark', 'Dark']
    ];
    var themeBtn = document.querySelector('.theme-toggle');
    if (themeBtn) {
        var themeSpec = function (name) {
            for (var i = 0; i < THEMES.length; i++) {
                if (THEMES[i][0] === name) return THEMES[i];
            }
            return THEMES[0];
        };
        var paintTheme = function (name) {
            var s = themeSpec(name);
            if (s[0] === 'system') delete document.documentElement.dataset.theme;
            else document.documentElement.dataset.theme = s[0];
            themeBtn.querySelector('use').setAttribute('href', '#' + s[1]);
            themeBtn.querySelector('.i-label').textContent = s[2];
            themeBtn.setAttribute('aria-label', 'Theme: ' + s[2].toLowerCase());
            try {
                if (s[0] === 'system') localStorage.removeItem('theme');
                else localStorage.setItem('theme', s[0]);
            } catch (e) {} // storage blocked: the choice still applies this page
        };
        themeBtn.classList.remove('hidden');
        paintTheme(document.documentElement.dataset.theme || 'system');
        themeBtn.addEventListener('click', function () {
            var i = THEMES.indexOf(themeSpec(document.documentElement.dataset.theme || 'system'));
            paintTheme(THEMES[(i + 1) % THEMES.length][0]);
        });
    }
})();
