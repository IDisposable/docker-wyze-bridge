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
import { VideoRTC } from './video-rtc.js';

if (!customElements.get('video-rtc')) {
    customElements.define('video-rtc', class extends VideoRTC {
        static get observedAttributes() { return ['src']; }
        attributeChangedCallback(name, oldValue, newValue) {
            if (name === 'src' && newValue && newValue !== oldValue) {
                this.src = newValue;
            }
        }
        oninit() {
            super.oninit();
            if (this.hasAttribute('muted')) {
                this.video.muted = true;
            }
        }
    });
}
