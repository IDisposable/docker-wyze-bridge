// Wyze Bridge WebUI - Vanilla JS
(function() {
    'use strict';

    // SSE connection for real-time updates
    let eventSource = null;

    function connectSSE() {
        if (eventSource) {
            eventSource.close();
        }

        eventSource = new EventSource('/events');

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
                img.src = '/api/snapshot/' + data.name + '?t=' + Date.now();
                img.style.display = '';
            }
        });

        eventSource.addEventListener('bridge_status', function(e) {
            // Could update a status bar if desired
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
        fetch('/api/cameras/' + name + '/restart', { method: 'POST' })
            .then(function(resp) { return resp.json(); })
            .then(function(data) {
                if (data.status === 'ok') {
                    updateCameraState(name, 'connecting');
                }
            })
            .catch(function(err) { console.error('Restart failed:', err); });
    };

    window.setQuality = function(name, quality) {
        fetch('/api/cameras/' + name + '/quality', {
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

    // Initialize
    function init() {
        connectSSE();
        wireCopyButtons();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
