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
        });

        eventSource.addEventListener('camera_ip', function(e) {
            // The LAN resolver corrected a camera's address; the grid
            // and the detail page both show it via .cam-ip.
            const data = JSON.parse(e.data);
            document.querySelectorAll('[data-cam="' + data.name + '"] .cam-ip, .cam-ip[data-cam="' + data.name + '"]').forEach(function(el) {
                el.textContent = data.ip;
            });
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
        wireDiscoverButton();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
