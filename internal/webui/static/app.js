// Wyze Bridge WebUI - Vanilla JS
(function() {
    'use strict';

    // Base path for HA ingress support. When running behind HA's ingress
    // proxy, all URLs must be prefixed with the ingress path. The server
    // injects this as a <script> tag before app.js loads.
    var B = window.__BASE_PATH || '';

    // SSE connection for real-time updates
    let eventSource = null;
    let reloadTimer = null;

    function connectSSE() {
        if (eventSource) {
            eventSource.close();
        }

        eventSource = new EventSource(B + '/events');

        eventSource.addEventListener('camera_state', function(e) {
            const data = JSON.parse(e.data);
            updateCameraState(data.name, data.state, data.quality);
        });

        eventSource.addEventListener('camera_added', function(e) {
            // Reload page when a new camera is discovered. A discovery
            // pass adds every camera within a few milliseconds, so
            // coalesce the burst into one reload.
            clearTimeout(reloadTimer);
            reloadTimer = setTimeout(function() { location.reload(); }, 1000);
        });

        eventSource.addEventListener('camera_removed', function(e) {
            const data = JSON.parse(e.data);
            const card = document.querySelector('[data-cam="' + data.name + '"]');
            if (card) card.remove();
            refreshChips();
        });

        eventSource.addEventListener('snapshot_ready', function(e) {
            // Only update placeholder <img> for non-streaming cards; streaming
            // cards show live video via <video-rtc> and don't need snapshots.
            const data = JSON.parse(e.data);
            const img = document.querySelector('.camera-card[data-cam="' + data.name + '"] .camera-preview img');
            if (img) {
                img.src = B + '/api/snapshot/' + data.name + '?t=' + Date.now();
                img.style.display = '';
            }
        });

        eventSource.addEventListener('bridge_status', function(e) {
            // Could update a status bar if desired
        });

        eventSource.addEventListener('recording_state', function(e) {
            const data = JSON.parse(e.data);
            updateRecordingUI(data.name, data.recording);
        });

        eventSource.addEventListener('camera_video', function(e) {
            // Whether frames are actually arriving: live / idle / no video.
            const data = JSON.parse(e.data);
            updateVideoUI(data);
            refreshChips();
        });

        eventSource.addEventListener('camera_lan', function(e) {
            // Resolver outcome for a camera: show the "not on this LAN?"
            // hint once the sweeps have given up on it, clear it when
            // it's found again.
            const data = JSON.parse(e.data);
            updateLanHint(data.name, data.absent, data.sweep_misses);
        });

        eventSource.addEventListener('camera_ip', function(e) {
            // The LAN resolver corrected a camera's address; the grid
            // and the detail page both show it via .cam-ip, and the
            // detail page's Network box says how it was found.
            const data = JSON.parse(e.data);
            document.querySelectorAll('[data-cam="' + data.name + '"] .cam-ip, .cam-ip[data-cam="' + data.name + '"]').forEach(function(el) {
                el.textContent = data.ip;
            });
            const box = document.querySelector('.network-box[data-cam="' + data.name + '"]');
            if (box) {
                const cloud = box.querySelector('.cam-cloud-ip');
                if (cloud) cloud.textContent = data.cloud_ip || 'no address';
                const status = box.querySelector('.cam-lan-status');
                if (status && data.source_label) status.textContent = 'just now via ' + data.source_label;
            }
        });

        eventSource.onerror = function() {
            // Reconnect after a delay
            setTimeout(connectSSE, 5000);
        };
    }

    // Video badge (grid + detail page) and the detail page's last video
    // / last error rows. The server sends the same shape /api/cameras
    // exposes: video, last_video, last_error, last_error_at.
    function updateVideoUI(data) {
        const labels = { live: 'live', idle: 'idle', no_video: 'no video' };
        document.querySelectorAll('.video-badge[data-cam="' + data.name + '"]').forEach(function(el) {
            el.className = 'video-badge ' + data.video;
            el.textContent = labels[data.video] || '';
            el.title = data.last_error ? data.last_error : '';
        });
        const err = document.querySelector('.cam-last-error[data-cam="' + data.name + '"]');
        if (err) err.textContent = data.last_error ? data.last_error + ' (just now)' : 'none';
        const lv = document.querySelector('.cam-last-video[data-cam="' + data.name + '"]');
        if (lv && data.video === 'live') lv.textContent = 'just now';
        if (data.video === 'live') {
            const hint = document.querySelector('.lan-hint[data-cam="' + data.name + '"]');
            if (hint) hint.hidden = true;
        }
    }

    // The hint stays hidden while video is flowing even if the resolver
    // can't see the camera (a VLAN'd camera that streams fine, say).
    function updateLanHint(name, absent, misses) {
        const hint = document.querySelector('.lan-hint[data-cam="' + name + '"]');
        if (!hint) return;
        const badge = document.querySelector('.video-badge[data-cam="' + name + '"]');
        const live = badge && badge.classList.contains('live');
        const n = hint.querySelector('.lan-hint-misses');
        if (n && typeof misses === 'number') n.textContent = misses;
        hint.hidden = !(absent && !live);
    }

    function updateCameraState(name, state, quality) {
        const card = document.querySelector('[data-cam="' + name + '"]');
        if (!card) return;

        card.setAttribute('data-state', state);

        const badge = card.querySelector('.state-badge');
        if (badge) {
            badge.textContent = state;
            badge.className = 'state-badge ' + state;
        }

        // Update quality on detail page
        const qualityEl = document.getElementById('quality');
        if (qualityEl && quality) {
            qualityEl.textContent = quality;
        }
    }

    // Camera actions (used on detail page)
    window.restartStream = function(name) {
        fetch(B + '/api/cameras/' + name + '/restart', { method: 'POST' })
            .then(function(resp) { return resp.json(); })
            .then(function(data) {
                if (data.status === 'ok') {
                    updateCameraState(name, 'connecting');
                }
            })
            .catch(function(err) { console.error('Restart failed:', err); });
    };

    window.setQuality = function(name, quality) {
        fetch(B + '/api/cameras/' + name + '/quality', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ quality: quality })
        })
            .then(function(resp) { return resp.json(); })
            .then(function(data) {
                if (data.status === 'ok') {
                    var el = document.getElementById('quality');
                    if (el) el.textContent = quality;
                }
            })
            .catch(function(err) { console.error('Quality change failed:', err); });
    };

    // Camera-side controls on the detail page. Each button POSTs one
    // property; the server replies with the value Wyze accepted. There
    // is no readback from Wyze for these, so the buttons are stateless
    // and just flash green (sent) or red (failed).
    function wirePropertyButtons() {
        document.querySelectorAll('.prop-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.preventDefault();
                if (btn.disabled) return;
                const cam = btn.getAttribute('data-cam');
                btn.disabled = true;
                fetch(B + '/api/cameras/' + cam + '/property', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ property: btn.getAttribute('data-property'), value: btn.getAttribute('data-value') })
                })
                    .then(function(resp) {
                        if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
                        btn.classList.add('sent');
                    })
                    .catch(function(err) {
                        console.error('Property change failed:', err);
                        btn.classList.add('failed');
                    })
                    .finally(function() {
                        setTimeout(function() {
                            btn.classList.remove('sent', 'failed');
                            btn.disabled = false;
                        }, 1500);
                    });
            });
        });
    }

    // Reboot the camera itself (Wyze cloud restart), after a confirm.
    function wireRebootButtons() {
        document.querySelectorAll('.reboot-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.preventDefault();
                if (btn.disabled) return;
                const cam = btn.getAttribute('data-cam');
                const nick = btn.getAttribute('data-nickname') || cam;
                if (!confirm('Reboot "' + nick + '"?\n\nThe camera itself restarts and the stream drops for about a minute.')) return;
                btn.disabled = true;
                const original = btn.textContent;
                btn.textContent = '⟲ Rebooting…';
                fetch(B + '/api/cameras/' + cam + '/reboot', { method: 'POST' })
                    .then(function(resp) {
                        if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
                        updateCameraState(cam, 'connecting');
                    })
                    .catch(function(err) {
                        console.error('Reboot failed:', err);
                        btn.textContent = '⚠ Failed';
                    })
                    .finally(function() {
                        setTimeout(function() { btn.textContent = original; btn.disabled = false; }, 3000);
                    });
            });
        });
    }

    // Click-to-copy for RTSP and other unsupported URL schemes.
    // Browsers can't navigate to rtsp://, so we copy to clipboard for VLC/ffmpeg use.
    function wireCopyButtons() {
        document.querySelectorAll('.copy-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.preventDefault();
                const url = btn.getAttribute('data-url');
                if (!url) return;
                const original = btn.textContent;
                const done = function() {
                    btn.textContent = 'Copied!';
                    btn.classList.add('copied');
                    setTimeout(function() {
                        btn.textContent = original;
                        btn.classList.remove('copied');
                    }, 1500);
                };
                if (navigator.clipboard && navigator.clipboard.writeText) {
                    navigator.clipboard.writeText(url).then(done).catch(function() {
                        fallbackCopy(url);
                        done();
                    });
                } else {
                    fallbackCopy(url);
                    done();
                }
            });
        });
    }

    function fallbackCopy(text) {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); } catch (e) {}
        document.body.removeChild(ta);
    }

    // Snapshot button: POST to /api/cameras/{name}/snapshot, flash "Saved!"
    // on the button. Capture is async on the server side — the 200 just
    // acknowledges the request was accepted, not that the file is on disk
    // yet. For snap-ready feedback we already have the SSE snapshot_ready
    // event which refreshes the card's preview.
    function wireSnapButtons() {
        document.querySelectorAll('.snap-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.preventDefault();
                const cam = btn.getAttribute('data-cam');
                if (!cam || btn.disabled) return;
                const original = btn.textContent;
                btn.disabled = true;
                fetch(B + '/api/cameras/' + cam + '/snapshot', { method: 'POST' })
                    .then(function(resp) {
                        if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
                        btn.textContent = '✓ Saved';
                        btn.classList.add('snapped');
                    })
                    .catch(function(err) {
                        console.error('Snapshot failed:', err);
                        btn.textContent = '⚠ Failed';
                    })
                    .finally(function() {
                        setTimeout(function() {
                            btn.textContent = original;
                            btn.classList.remove('snapped');
                            btn.disabled = false;
                        }, 1500);
                    });
            });
        });
    }

    // Record start/stop button. POSTs to /api/cameras/{name}/record with
    // action: "start" | "stop". The server flips the recorder and replies
    // with the resulting recording state; we drive the UI from that (not
    // from the click, which keeps the button honest if the server refuses).
    // SSE recording_state events update this from the other direction
    // when recording state changes out-of-band (OnChange from another
    // client, or via MQTT).
    function wireRecordButtons() {
        document.querySelectorAll('.record-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.preventDefault();
                const cam = btn.getAttribute('data-cam');
                const recording = btn.getAttribute('data-recording') === 'true';
                if (!cam || btn.disabled) return;
                btn.disabled = true;
                fetch(B + '/api/cameras/' + cam + '/record', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ action: recording ? 'stop' : 'start' })
                })
                    .then(function(resp) {
                        if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
                        return resp.json();
                    })
                    .then(function(data) {
                        updateRecordingUI(cam, !!data.recording);
                    })
                    .catch(function(err) {
                        console.error('Record toggle failed:', err);
                    })
                    .finally(function() { btn.disabled = false; });
            });
        });
    }

    // Reflect a new recording state in every record button on the page
    // (both the grid pill version and the detail-page labeled version).
    // Called by the click handler and by the SSE recording_state event.
    function updateRecordingUI(camName, recording) {
        document.querySelectorAll('.record-btn[data-cam="' + camName + '"]').forEach(function(btn) {
            btn.setAttribute('data-recording', recording ? 'true' : 'false');
            btn.classList.toggle('on', recording);
            if (btn.textContent.indexOf('Recording') >= 0) {
                btn.textContent = recording ? '⏹ Stop Recording' : '⏺ Start Recording';
            } else {
                btn.textContent = recording ? '⏹' : '⏺';
            }
            btn.setAttribute('title', recording ? 'Stop recording' : 'Start recording');
            btn.setAttribute('aria-label', recording ? 'Stop recording' : 'Start recording');
        });
    }

    // Ignore button: POST /api/cameras/{name}/ignore after a confirm.
    // The server drops the camera and persists its MAC; the SSE
    // camera_removed event takes the card off the grid, and the page
    // reloads so the "Ignored cameras" strip picks up the new row.
    function wireIgnoreButtons() {
        document.querySelectorAll('.ignore-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.preventDefault();
                const cam = btn.getAttribute('data-cam');
                const nick = btn.getAttribute('data-nickname') || cam;
                if (!cam || btn.disabled) return;
                if (!confirm('Ignore "' + nick + '"?\n\nThe bridge will stop connecting to it and hide it. You can restore it from the Ignored cameras list at the bottom of the dashboard.')) return;
                btn.disabled = true;
                fetch(B + '/api/cameras/' + cam + '/ignore', { method: 'POST' })
                    .then(function(resp) {
                        if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
                        location.reload();
                    })
                    .catch(function(err) {
                        console.error('Ignore failed:', err);
                        btn.disabled = false;
                    });
            });
        });
    }

    // Restore button on the ignored strip: POST /api/ignored/{mac}/restore.
    // The server rediscovers asynchronously; camera_added reloads the
    // page when the card is back, but drop the row right away so the
    // click visibly did something.
    function wireRestoreButtons() {
        document.querySelectorAll('.restore-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.preventDefault();
                const mac = btn.getAttribute('data-mac');
                if (!mac || btn.disabled) return;
                btn.disabled = true;
                btn.textContent = 'Restoring…';
                fetch(B + '/api/ignored/' + encodeURIComponent(mac) + '/restore', { method: 'POST' })
                    .then(function(resp) {
                        if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
                        const row = btn.closest('tr');
                        if (row) row.remove();
                    })
                    .catch(function(err) {
                        console.error('Restore failed:', err);
                        btn.textContent = 'Restore';
                        btn.disabled = false;
                    });
            });
        });
    }

    // Tile arrangement. The grid is either locked (default once you've
    // locked it) or arranging: in arranging mode each card shows a ⠿
    // handle, and dragging the handle moves the card. Pointer events
    // rather than HTML5 drag-and-drop so it works on touch screens.
    // The order is saved server-side (PUT /api/layout) after every
    // drop, and the lock button saves the lock flag, so every browser
    // sees the same grid.
    function currentOrder() {
        return Array.prototype.map.call(document.querySelectorAll('#camera-grid .camera-card'), function(card) {
            return card.getAttribute('data-cam');
        });
    }

    // Rotations are read back off the cards so one PUT carries the whole
    // layout: order, lock and every tile's rotation.
    function currentRotation() {
        const rot = {};
        document.querySelectorAll('#camera-grid .camera-card').forEach(function(card) {
            const preview = card.querySelector('.camera-preview');
            const deg = preview ? parseInt(preview.getAttribute('data-rot') || '0', 10) : 0;
            if (deg) rot[card.getAttribute('data-cam')] = deg;
        });
        return rot;
    }

    function saveLayout(locked) {
        return fetch(B + '/api/layout', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ order: currentOrder(), locked: locked, rotation: currentRotation() })
        }).then(function(resp) {
            if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
            return resp.json();
        });
    }

    // Rotate button (arranging mode only): quarter turn per click,
    // applied to the preview at once and saved with the layout.
    function wireRotateButtons() {
        document.querySelectorAll('.rotate-btn').forEach(function(btn) {
            btn.addEventListener('click', function(e) {
                e.preventDefault();
                e.stopPropagation();
                const card = btn.closest('.camera-card');
                const preview = card ? card.querySelector('.camera-preview') : null;
                if (!preview) return;
                const next = (parseInt(preview.getAttribute('data-rot') || '0', 10) + 90) % 360;
                preview.setAttribute('data-rot', String(next));
                saveLayout(false).catch(function(err) { console.error('Saving rotation failed:', err); });
            });
        });
    }

    function wireLockButton() {
        const btn = document.getElementById('lock-btn');
        const grid = document.getElementById('camera-grid');
        if (!btn || !grid) return;
        btn.addEventListener('click', function(e) {
            e.preventDefault();
            if (btn.disabled) return;
            const locked = btn.getAttribute('data-locked') !== 'true';
            btn.disabled = true;
            saveLayout(locked)
                .then(function() {
                    btn.setAttribute('data-locked', locked ? 'true' : 'false');
                    btn.textContent = locked ? '🔒 Locked' : '🔓 Arrange';
                    btn.title = locked ? 'Tiles are locked; click to arrange' : 'Drag the ⠿ handle to arrange; click to lock';
                    grid.classList.toggle('locked', locked);
                    grid.classList.toggle('arranging', !locked);
                })
                .catch(function(err) { console.error('Lock toggle failed:', err); })
                .finally(function() { btn.disabled = false; });
        });
    }

    function wireDragHandles() {
        const grid = document.getElementById('camera-grid');
        if (!grid) return;
        let dragging = null;

        // Find the card under the pointer, ignoring the one being dragged.
        function cardAt(x, y) {
            const el = document.elementFromPoint(x, y);
            const card = el && el.closest ? el.closest('.camera-card') : null;
            return (card && card !== dragging && card.parentNode === grid) ? card : null;
        }

        grid.addEventListener('pointerdown', function(e) {
            const handle = e.target.closest ? e.target.closest('.drag-handle') : null;
            if (!handle || !grid.classList.contains('arranging')) return;
            dragging = handle.closest('.camera-card');
            if (!dragging) return;
            e.preventDefault();
            dragging.classList.add('dragging');
            handle.setPointerCapture(e.pointerId);
        });

        grid.addEventListener('pointermove', function(e) {
            if (!dragging) return;
            e.preventDefault();
            const target = cardAt(e.clientX, e.clientY);
            if (!target) return;
            // Drop before the target when the pointer is in its first
            // half (reading order), after it otherwise.
            const rect = target.getBoundingClientRect();
            const before = (e.clientY < rect.top + rect.height / 2) ||
                (e.clientY < rect.bottom && e.clientX < rect.left + rect.width / 2);
            if (before) {
                grid.insertBefore(dragging, target);
            } else {
                grid.insertBefore(dragging, target.nextSibling);
            }
        });

        function endDrag(e) {
            if (!dragging) return;
            dragging.classList.remove('dragging');
            dragging = null;
            saveLayout(false).catch(function(err) { console.error('Saving layout failed:', err); });
        }
        grid.addEventListener('pointerup', endDrag);
        grid.addEventListener('pointercancel', endDrag);
    }

    // Light/dark toggle. The <head> applies the saved choice before
    // first paint; this just flips and remembers it. With nothing saved
    // the stylesheet follows the OS.
    function wireThemeButton() {
        const btn = document.getElementById('theme-btn');
        if (!btn) return;
        btn.addEventListener('click', function() {
            const root = document.documentElement;
            const current = root.getAttribute('data-theme') ||
                (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark');
            const next = current === 'light' ? 'dark' : 'light';
            root.setAttribute('data-theme', next);
            try { localStorage.setItem('wb-theme', next); } catch (e) { /* private mode: theme just won't persist */ }
        });
    }

    // Filter box: hides cards whose nickname / model / IP don't contain
    // the text. Purely client-side; nothing is sent to the server.
    function wireSearch() {
        const input = document.getElementById('search');
        if (!input) return;
        input.addEventListener('input', function() {
            const q = input.value.trim().toLowerCase();
            document.querySelectorAll('#camera-grid .camera-card').forEach(function(card) {
                const hay = (card.getAttribute('data-search') || '').toLowerCase();
                card.hidden = q !== '' && hay.indexOf(q) < 0;
            });
        });
    }

    // Header chips are counted from the badges on the page, so they
    // stay right as SSE events change cards without a reload.
    function refreshChips() {
        const set = function(key, n) {
            const chip = document.querySelector('.chip[data-chip="' + key + '"]');
            if (!chip) return;
            chip.querySelector('b').textContent = n;
            chip.classList.toggle('zero', n === 0);
        };
        set('cameras', document.querySelectorAll('#camera-grid .camera-card').length);
        set('live', document.querySelectorAll('#camera-grid .video-badge.live').length);
        set('no_video', document.querySelectorAll('#camera-grid .video-badge.no_video').length);
    }

    // Rediscover button: POST /api/discover to kick a Wyze API refresh.
    // The discovery pass is async on the server — camera state updates
    // arrive over SSE as cameras come online, so we just flash the
    // button and let the grid rewire itself.
    function wireDiscoverButton() {
        const btn = document.getElementById('discover-btn');
        if (!btn) return;
        btn.addEventListener('click', function(e) {
            e.preventDefault();
            if (btn.disabled) return;
            btn.disabled = true;
            const originalText = btn.textContent;
            btn.textContent = '↻ Discovering…';
            fetch(B + '/api/discover', { method: 'POST' })
                .then(function(resp) {
                    if (!resp.ok) throw new Error(resp.status + ' ' + resp.statusText);
                })
                .catch(function(err) {
                    console.error('Rediscover failed:', err);
                })
                .finally(function() {
                    setTimeout(function() {
                        btn.disabled = false;
                        btn.textContent = originalText;
                    }, 2000);
                });
        });
    }

    // Initialize
    function init() {
        connectSSE();
        wireCopyButtons();
        wireSnapButtons();
        wireRecordButtons();
        wireIgnoreButtons();
        wireRestoreButtons();
        wirePropertyButtons();
        wireRebootButtons();
        wireLockButton();
        wireDragHandles();
        wireRotateButtons();
        wireThemeButton();
        wireSearch();
        wireDiscoverButton();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
