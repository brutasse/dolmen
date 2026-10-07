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

    // File line-number gutters. Textareas are wrap="off" so one logical
    // line is exactly one visual line and the numbers always align; the
    // gutter text is rebuilt only when the line count changes.
    function syncLnum(ta) {
        var box = ta.parentNode;
        if (!box || box.className !== 'editor') return; // clipboard-helper textarea
        var gutter = box.querySelector('.lnum');
        if (!gutter) return;
        var lines = (ta.value.match(/\n/g) || []).length + 1;
        if (ta._lnum !== lines) {
            ta._lnum = lines;
            var nums = '';
            for (var n = 1; n <= lines; n++) nums += n + '\n';
            gutter.textContent = nums;
            // the gutter overlays the wrapper; the textarea keeps clear of
            // it. width: auto cannot fill for form controls (UA sizes them
            // from cols under appearance: auto), so compute it explicitly.
            ta.style.marginLeft = gutter.offsetWidth + 'px';
            ta.style.width = 'calc(100% - ' + gutter.offsetWidth + 'px)';
        }
        gutter.scrollTop = ta.scrollTop;
    }
    document.addEventListener('input', function (e) {
        if (e.target.tagName === 'TEXTAREA') syncLnum(e.target);
    });
    document.addEventListener('focusin', function (e) {
        if (e.target.tagName === 'TEXTAREA') syncLnum(e.target);
    });
    // scroll does not bubble; capture catches every textarea
    document.addEventListener('scroll', function (e) {
        if (e.target.tagName === 'TEXTAREA') syncLnum(e.target);
    }, true);
    document.querySelectorAll('.editor textarea').forEach(syncLnum);

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
        sniffBinary(file).then(function (binary) {
            binary ? uploadAndAttach(entry, file) : attachAsText(entry, file);
        });
    }

    // --- Page-wide file drops and the drag preview. -----------------------
    // Drop on an entry: that entry is replaced (extras become new entries).
    // Drop anywhere else: each file lands in the first still-empty entry,
    // overflow files become new entries. While dragging, the exact same
    // plan is previewed: a page overlay with a one-line sentence, the
    // planned landing entry (.dragtarget), the hovered entry (.dragover).
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
    function planSentence(total) {
        var fill = Math.min(total, emptyEntries().length);
        var fresh = total - fill;
        if (!fresh) return 'Will fill ' + plural(fill, 'empty row');
        if (!fill) return 'Will add ' + plural(fresh, 'new file');
        return 'Will fill ' + plural(fill, 'empty row') + ', add ' + plural(fresh, 'new file');
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
    function distributeFiles(files) { // page resolver: empty rows first, then new
        var empties = emptyEntries(); // snapshot: async fills must not re-hit a row
        files.forEach(function (file) {
            if (empties.length) handleDroppedFile(empties.shift(), file);
            else newEntry(file);
        });
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
            markPlanned(emptyEntries()[0] || null);
            var n = e.dataTransfer.items ? e.dataTransfer.items.length : 1;
            ov.querySelector('.drop-hint').textContent = planSentence(n);
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
    // page (pointer is unknowable for a paste): each file lands in the first
    // still-empty entry, overflow files become new entries. A gesture that
    // would clobber a non-empty row arms a chip (Replace / Add as new /
    // Cancel) and waits; the newest gesture on a row re-arms it, and saving
    // is blocked until every chip on the page is answered. Plain text
    // pastes are never intercepted.
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
})();
