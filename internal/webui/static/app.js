// Wyze Bridge WebUI - Vanilla JS
(function() {
    'use strict';

    // Base path for HA ingress support. When running behind HA's ingress
    // proxy, all URLs must be prefixed with the ingress path. The server
    // injects this as a <script> tag before app.js loads.
    var B = window.__BASE_PATH || '';

    // SSE connection for real-time updates
    let eventSource = null;

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
            // Reload page when a new camera is discovered
            location.reload();
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

        eventSource.onerror = function() {
            // Reconnect after a delay
            setTimeout(connectSSE, 5000);
        };
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
        wireDiscoverButton();
        initPTZ();
        initSpeak();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

    // ── Pan / tilt (camera detail page, TUTK pan cams only) ──────────
    // Press-and-hold: "move" on press, "stop" on release. The motor
    // keeps turning until stopped or it reaches a travel limit.
    function initPTZ() {
        var box = document.querySelector('.ptz[data-cam]');
        if (!box) return;
        var cam = box.getAttribute('data-cam');
        var posEl = box.querySelector('.ptz-pos');
        var speedEl = box.querySelector('.ptz-speed input');
        var moving = null;

        function api(body) {
            return fetch(B + '/api/cameras/' + cam + '/ptz', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body)
            }).then(function(r) {
                return r.json().then(function(j) {
                    if (!r.ok) throw new Error(j.error || r.statusText);
                    return j;
                });
            });
        }

        function refresh() {
            fetch(B + '/api/cameras/' + cam + '/ptz').then(function(r) {
                return r.ok ? r.json() : null;
            }).then(function(j) {
                if (j && j.position) {
                    posEl.textContent = 'pan ' + j.position.horizontal + '\u00b0 \u00b7 tilt ' + j.position.vertical + '\u00b0';
                }
            }).catch(function() {});
        }

        function showError(err) {
            posEl.textContent = err.message;
        }

        function start(dir) {
            if (moving === dir) return;
            moving = dir;
            api({ action: 'move', direction: dir, speed: parseInt(speedEl.value, 10) }).catch(showError);
        }

        function stop() {
            if (!moving) return;
            moving = null;
            api({ action: 'move', direction: 'stop' }).then(function() {
                setTimeout(refresh, 400);
            }).catch(showError);
        }

        // Tap = fixed 15-degree step (one request, latency-proof);
        // hold past HOLD_MS = continuous move until release.
        var HOLD_MS = 300;
        box.querySelectorAll('.ptz-btn[data-dir]').forEach(function(btn) {
            var dir = btn.getAttribute('data-dir');
            var holdTimer = null;
            btn.addEventListener('pointerdown', function(e) {
                e.preventDefault();
                if (btn.setPointerCapture) btn.setPointerCapture(e.pointerId);
                btn.classList.add('active');
                holdTimer = setTimeout(function() {
                    holdTimer = null;
                    start(dir);
                }, HOLD_MS);
            });
            function release() {
                btn.classList.remove('active');
                if (holdTimer) {
                    clearTimeout(holdTimer);
                    holdTimer = null;
                    api({ action: 'step', direction: dir }).then(function() {
                        setTimeout(refresh, 1500);
                    }).catch(showError);
                    return;
                }
                stop();
            }
            btn.addEventListener('pointerup', release);
            ['pointercancel', 'lostpointercapture'].forEach(function(ev) {
                btn.addEventListener(ev, function() {
                    if (holdTimer) { clearTimeout(holdTimer); holdTimer = null; btn.classList.remove('active'); return; }
                    btn.classList.remove('active');
                    stop();
                });
            });
            btn.addEventListener('contextmenu', function(e) { e.preventDefault(); });
        });

        var home = box.querySelector('[data-ptz-home]');
        if (home) {
            home.addEventListener('click', function() {
                posEl.textContent = 'recalibrating\u2026';
                api({ action: 'home' }).then(function() {
                    setTimeout(refresh, 20000);
                }).catch(showError);
            });
        }

        var keys = { ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down' };
        document.addEventListener('keydown', function(e) {
            var dir = keys[e.key];
            if (!dir || e.target.closest('input, textarea, select')) return;
            e.preventDefault();
            start(dir);
        });
        document.addEventListener('keyup', function(e) {
            if (keys[e.key]) stop();
        });
        window.addEventListener('blur', stop);

        refresh();
    }


    // ── Voice message (camera detail page) ───────────────────────────
    // Record with the browser microphone (requires https or localhost) or
    // pick a file; the bridge plays it out of the camera speaker.
    function initSpeak() {
        var box = document.querySelector('.speak[data-cam]');
        if (!box) return;
        var cam = box.getAttribute('data-cam');
        var status = box.querySelector('.speak-status');
        var recBtn = box.querySelector('.speak-rec');
        var fileIn = box.querySelector('.speak-upload input');
        var recorder = null, chunks = [], timer = null;

        function send(blob, filename) {
            var fd = new FormData();
            fd.append('audio', blob, filename);
            status.textContent = 'sending\u2026';
            fetch(B + '/api/cameras/' + cam + '/speak', { method: 'POST', body: fd })
                .then(function(r) { return r.json().then(function(j) {
                    if (!r.ok) throw new Error(j.error || r.statusText);
                    return j;
                }); })
                .then(function(j) {
                    status.textContent = 'playing (' + j.seconds.toFixed(1) + 's)';
                    setTimeout(function() { status.textContent = ''; }, (j.seconds + 2) * 1000);
                })
                .catch(function(err) { status.textContent = err.message; });
        }

        function stopRec() {
            if (recorder && recorder.state === 'recording') recorder.stop();
            clearTimeout(timer);
        }

        recBtn.addEventListener('click', function() {
            if (recorder && recorder.state === 'recording') { stopRec(); return; }
            if (!navigator.mediaDevices || !window.MediaRecorder) {
                status.textContent = 'recording needs https (use the public URL) or a file';
                return;
            }
            navigator.mediaDevices.getUserMedia({ audio: true }).then(function(stream) {
                chunks = [];
                recorder = new MediaRecorder(stream);
                recorder.ondataavailable = function(e) { if (e.data.size) chunks.push(e.data); };
                recorder.onstop = function() {
                    stream.getTracks().forEach(function(t) { t.stop(); });
                    recBtn.classList.remove('on');
                    recBtn.innerHTML = '&#127908; Record';
                    send(new Blob(chunks, { type: recorder.mimeType }), 'message.webm');
                };
                recorder.start();
                recBtn.classList.add('on');
                recBtn.innerHTML = '&#9209; Stop &amp; send';
                status.textContent = 'recording\u2026 (max 60s)';
                timer = setTimeout(stopRec, 60000);
            }).catch(function(err) { status.textContent = 'microphone: ' + err.message; });
        });

        fileIn.addEventListener('change', function() {
            if (fileIn.files.length) send(fileIn.files[0], fileIn.files[0].name);
            fileIn.value = '';
        });
    }

})();
