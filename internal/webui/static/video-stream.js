// Registers go2rtc's VideoRTC class as the <video-rtc> custom element.
// video-rtc.js only exports the class and uses a `set src(value)` setter —
// it does NOT observe the `src` HTML attribute, so `<video-rtc src="...">`
// in HTML never triggers the setter. The subclass below observes it and
// forwards attribute changes to the setter so the element connects on mount.
//
// The `muted` attribute is also honored: video-rtc.js only mutes on
// autoplay failure, so once the browser grants an origin autoplay
// permission (after any user click), grid videos start unmuted on the
// next page load. Reading `muted` in oninit and applying it to the
// internal <video> keeps the grid quiet regardless of engagement state.
//
// Two dashboard fixes live here too, because they're about the player:
//
// One connection per tile. Grid players carry mode="mse", so each asks
// go2rtc for a single MSE consumer rather than WebRTC and MSE at once.
//
// Staggered start. The grid used to open every player in the same
// instant, and each one asks go2rtc for two consumers (WebRTC and MSE),
// so a dozen cameras were dialed at once. Some lost that race and came
// back with "discovery timeout" while the same camera opened instantly
// on its own detail page. Players now connect STAGGER_MS apart.
//
// Deeper buffer on the grid. video-rtc's MSE player sets the playback
// rate to however many seconds it has buffered, so it hovers about one
// second behind live. These cameras send a large keyframe every couple
// of seconds and little in between, which runs that buffer dry over and
// over: the tile freezes, then fast-forwards. Players with a buffer="N"
// attribute aim for N seconds instead and only nudge the rate (between
// RATE_MIN and RATE_MAX) to get there. The detail page has no buffer
// attribute and keeps go2rtc's low-latency behaviour.
//
// Seeks that land in data. When a player falls more than 5s behind,
// video-rtc trims what it has buffered to the last 5s and seeks to
// end - 5. The trim can only cut at a keyframe, so the data now starts
// a little after that point and the seek lands in a hole: the player
// waits, the next append does the same thing again, and the tile sits
// on the spinner (one camera here did 1500 seeks in 45s). For buffered
// players every seek is moved into the buffered data, at most N seconds
// behind its end, so a player that fell behind skips forward once and
// carries on.
//
// Skipping a hole. go2rtc drops a frame it didn't get every packet of
// (its log says "[DROP] ... INCOMPLETE"), and on Wi-Fi that is often a
// keyframe: 136 packets for one HD keyframe here, 44 of them lost. The
// frames after it can't be decoded, so the player sits on the spinner
// with seconds of video buffered behind the hole until video-rtc's 5s
// trim happens to jump past it. A buffered player that hasn't moved for
// HOLE_MS with at least HOLE_AHEAD seconds buffered skips to just short
// of the newest data, which starts after the next good keyframe.
//
// Socket closed by the bridge. When the bridge replaces a stream whose
// camera went silent it closes the players' sockets so they reconnect
// to the new one; a WebRTC player (the detail page) needs a restart for
// that, an MSE one reconnects by itself.
//
// Stall restart. When go2rtc reports a failed dial to a player, video-rtc
// logs it and then waits on the socket forever (it only reconnects when
// the socket closes, and not at all while a dead RTCPeerConnection is
// still attached). A watchdog restarts a player whose picture stopped
// moving, or whose dial go2rtc reported as failed, one restart at a
// time, backing off for a camera that keeps failing (another house's,
// say) so it isn't redialed every few seconds. A player still waiting
// for its first picture with no error is left alone for up to
// DIAL_MAX_MS: a dozing V3 can take 40 seconds to answer, and
// restarting it at 20 cancelled the dial that was about to work.
import { VideoRTC } from './video-rtc.js';

const STAGGER_MS = 400;        // gap between players starting
const STALL_MS = 20000;        // no picture movement for this long → restart
const STALL_MAX_MS = 300000;   // backoff ceiling for a camera that keeps failing
const RESTART_GAP_MS = 1500;   // at most one restart this often, page-wide
const CHECK_MS = 5000;         // how often the watchdog looks
const DIAL_MAX_MS = 90000;     // longest wait for a first picture without an error
const HOLE_MS = 750;           // stuck this long with data buffered → skip the hole
const HOLE_AHEAD = 1.0;        // ...when at least this many seconds are buffered
const HOLE_LAND = 0.5;         // land this far short of the newest data
const HOLE_CHECK_MS = 250;     // how often buffered players are checked
const RATE_MIN = 0.8;          // slowest a buffered player plays to refill
const RATE_MAX = 1.25;         // fastest it plays to catch up (go2rtc: up to 5x)

const nativeRate = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, 'playbackRate');
const nativeTime = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, 'currentTime');

// bufferRate maps the rate video-rtc asks for (seconds buffered ahead)
// to one aiming for `target` seconds instead.
export function bufferRate(ahead, target) {
    return Math.min(RATE_MAX, Math.max(RATE_MIN, ahead / target));
}

// seekTarget moves a seek to `t` into the newest buffered range [lo, hi].
// A seek to before the data (video-rtc catching up) or past the last
// `target` seconds (the live end) goes to `target` seconds behind the
// end, or the start of the data if there's less than that. Nothing
// buffered: t as is.
export function seekTarget(t, lo, hi, target) {
    if (!(hi > lo)) return t;
    const settle = Math.max(lo, hi - target);
    return t < lo ? settle : Math.min(t, settle);
}

// holeSkip says where a stuck player should jump: `ahead` seconds are
// buffered past currentTime in [lo, hi] and it hasn't moved for
// `stuckMs`. Returns the new time, or -1 to leave it alone.
export function holeSkip(t, lo, hi, stuckMs) {
    if (stuckMs < HOLE_MS || !(hi > lo) || hi - t < HOLE_AHEAD) return -1;
    return Math.max(lo, t, hi - HOLE_LAND);
}

// keepBuffer makes video-rtc's rate updates on this <video> aim for
// `target` seconds of buffer, and its seeks land in buffered data.
// video-rtc only ever writes playbackRate from its MSE append handler,
// with the seconds buffered as the value, and only seeks there (to
// end - 5) and when the element is re-attached (to the live end).
export function keepBuffer(video, target) {
    Object.defineProperty(video, 'playbackRate', {
        configurable: true,
        get() { return nativeRate.get.call(this); },
        set(ahead) { nativeRate.set.call(this, bufferRate(ahead, target)); },
    });
    Object.defineProperty(video, 'currentTime', {
        configurable: true,
        get() { return nativeTime.get.call(this); },
        set(t) {
            const b = this.buffered;
            const n = b.length;
            nativeTime.set.call(this, n ? seekTarget(t, b.start(n - 1), b.end(n - 1), target) : t);
        },
    });
}

let nextStart = 0;             // earliest time the next player may start
let restartQueue = [];
let restartTimer = 0;

// startSlot returns how long a player should wait before connecting so
// that players starting together are spread STAGGER_MS apart.
function startSlot() {
    const now = Date.now();
    nextStart = Math.max(nextStart, now);
    const wait = nextStart - now;
    nextStart += STAGGER_MS;
    return wait;
}

function runRestartQueue() {
    restartTimer = 0;
    const next = restartQueue.shift();
    if (next) next.el.restart(next.soft);
    if (restartQueue.length) restartTimer = setTimeout(runRestartQueue, RESTART_GAP_MS);
}

// queueRestart lines a player up for a restart. soft marks one the
// bridge asked for (it closed the socket after replacing the stream),
// which doesn't count against the player's backoff.
function queueRestart(el, soft = false) {
    if (restartQueue.some(q => q.el === el)) return;
    restartQueue.push({ el, soft });
    if (!restartTimer) restartTimer = setTimeout(runRestartQueue, 0);
}

if (!customElements.get('video-rtc')) {
    customElements.define('video-rtc', class extends VideoRTC {
        static get observedAttributes() { return ['src']; }

        attributeChangedCallback(name, oldValue, newValue) {
            if (name !== 'src' || !newValue || newValue === oldValue) return;
            const wait = startSlot();
            const apply = () => {
                // mode="mse" (the grid sets it) asks go2rtc for one MSE
                // consumer instead of WebRTC and MSE racing each other:
                // half the dials, no ICE per tile. The detail page keeps
                // video-rtc's default for its lower latency.
                if (this.hasAttribute('mode')) this.mode = this.getAttribute('mode');
                this.src = newValue;
                this.markProgress();
            };
            if (wait > 0) setTimeout(apply, wait); else apply();
        }

        oninit() {
            super.oninit();
            if (this.hasAttribute('muted')) {
                this.video.muted = true;
            }
            const target = parseFloat(this.getAttribute('buffer'));
            if (target > 0) {
                keepBuffer(this.video, target);
                this.buffered_ = true;
            }
            this.stallMs = STALL_MS;
            this.markProgress();
        }

        // markProgress records "the picture is fine as of now".
        markProgress() {
            this.lastT = this.video ? this.video.currentTime : 0;
            this.lastMove = Date.now();
        }

        // onconnect: every new socket starts a fresh dial, initial,
        // restarted or reconnected by video-rtc itself.
        onconnect() {
            const started = super.onconnect();
            if (started) {
                this.connectAt = Date.now();
                this.played = false;
                this.dialErrorAt = 0;
            }
            return started;
        }

        // onopen: also listen for go2rtc's error message, which is how a
        // failed dial reaches the player.
        onopen() {
            const modes = super.onopen();
            this.onmessage['watchdog'] = msg => {
                if (msg.type === 'error' && !this.dialErrorAt) this.dialErrorAt = Date.now();
            };
            return modes;
        }

        // stalled reports a player that needs a restart: it played and
        // then its picture stopped for stallMs, or it never played and
        // go2rtc said the dial failed stallMs ago, or it has waited
        // DIAL_MAX_MS for a first picture with no word at all. Players in
        // a background tab are left to video-rtc's own handling.
        stalled(now) {
            if (!this.video || !this.isConnected || document.hidden) {
                this.markProgress();
                return false;
            }
            const t = this.video.currentTime;
            if (t !== this.lastT && !this.video.paused) {
                this.lastT = t;
                this.lastMove = now;
                this.played = true;
                this.stallMs = STALL_MS; // healthy again: reset the backoff
                return false;
            }
            if (this.played) return now - this.lastMove >= this.stallMs;
            if (this.dialErrorAt) return now - this.dialErrorAt >= this.stallMs;
            return !!this.connectAt && now - this.connectAt >= DIAL_MAX_MS;
        }

        // onclose: the socket closed without us asking. video-rtc
        // reconnects on its own, except while a WebRTC peer connection is
        // still attached (the detail page), where its onconnect bails out
        // and the player would sit dead. The bridge closes sockets on
        // purpose after replacing a silent stream, so restart those.
        onclose() {
            const reconnecting = super.onclose();
            if (reconnecting && this.pc) queueRestart(this, true);
            return reconnecting;
        }

        // restart drops the socket and peer connection and connects again,
        // the same thing the browser does when the page is reloaded. A
        // soft restart leaves the stall backoff where it was.
        restart(soft = false) {
            if (this.reconnectTID) {
                clearTimeout(this.reconnectTID);
                this.reconnectTID = 0;
            }
            this.ondisconnect();
            this.onconnect();
            this.markProgress();
            if (!soft) this.stallMs = Math.min(this.stallMs * 2, STALL_MAX_MS);
        }
    });

    setInterval(() => {
        const now = Date.now();
        document.querySelectorAll('video-rtc').forEach(el => {
            if (typeof el.stalled === 'function' && el.stalled(now)) queueRestart(el);
        });
    }, CHECK_MS);

    // Hole check for buffered (grid) players; see holeSkip.
    setInterval(() => {
        const now = Date.now();
        document.querySelectorAll('video-rtc').forEach(el => {
            const v = el.video;
            if (!el.buffered_ || !v || v.paused || document.hidden) return;
            const t = nativeTime.get.call(v);
            if (t !== el.holeT) {
                el.holeT = t;
                el.holeSince = now;
                return;
            }
            const b = v.buffered;
            const n = b.length;
            if (!n) return;
            const to = holeSkip(t, b.start(n - 1), b.end(n - 1), now - (el.holeSince || now));
            if (to > t) {
                nativeTime.set.call(v, to);
                el.holeSince = now;
            }
        });
    }, HOLE_CHECK_MS);
}
