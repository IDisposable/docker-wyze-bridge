// Package main is the Wyze Gwell P2P proxy — the bridge-side sidecar
// that speaks Wyze's Gwell/IoTVideo protocol to GW_* cameras (Doorbell
// Pro, OG, OG 3X, Doorbell Duo) and publishes H.264 via RTSP PUSH to
// our loopback go2rtc.
//
// Ported (with import path swaps) from the `cmd/gwell-proxy/main.go`
// of wlatic/hacky-wyze-gwell at SHA bilbaraski/hacky-wyze-gwell@c930a32
// (that fork has the cmd/ directory upstream's main branch is missing
// at pinned SHA 9c1b99f8 — see internal/gwell/upstream/README.md).
//
// Architectural divergence from the upstream fork:
//   - pkg/wyze (upstream's client for a companion Python wyze-api
//     service) is replaced by a local HTTP client in wyzeshim.go that
//     hits our bridge's internal shim endpoints. Our bridge owns the
//     Wyze cloud auth — the shim just re-exposes it in the shape the
//     proxy expects so the port below stays verbatim.
//   - Default API URL points at our bridge's localhost shim, not a
//     separate macvlan-addressed wyze-api container.
//   - Default RTSP target is go2rtc on :8554 (RTSP PUSH compatible
//     with mediamtx's dialect).
//   - cacheFile respects STATE_DIR so token_cache.json persists
//     alongside wyze-bridge.state.json.
//
// v2 upstream notes preserved:
// - Serialized P2P session setup via connectMu to prevent cross-session interference
// - Fresh discovery on reconnect when both cameras are down
// - Proper 15s stagger between all session setups, not just initial start
// - DeviceName set to cameraID for correct device targeting
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/gwell/upstream/gwell"
	"github.com/IDisposable/docker-wyze-bridge/internal/gwell/upstream/stream"
)

const (
	tokenRefreshInterval = 1 * time.Hour
	cameraStagger        = 15 * time.Second
	reconnectDelay       = 10 * time.Second
)

// connectMu serializes P2P session setup across all cameras.
// The GWell P2P server gets confused when multiple sessions from the same
// account do InitInfoMsg simultaneously — frames leak between sessions,
// causing "bad count" decryption failures. Only one camera should be in
// the connect→certify→initInfo→subscribe→calling phase at a time.
var connectMu sync.Mutex

// tokenCache persists credentials across restarts.
type tokenCache struct {
	AccessID    string `json:"accessId"`
	AccessToken string `json:"accessToken"`
	ServerAddr  string `json:"serverAddr"`
	CachedAt    int64  `json:"cachedAt"`
	TTLSeconds  int64  `json:"ttlSeconds"`
}

func (tc *tokenCache) isValid() bool {
	if tc.AccessID == "" || tc.AccessToken == "" {
		return false
	}
	elapsed := time.Since(time.Unix(tc.CachedAt, 0))
	ttl := time.Duration(tc.TTLSeconds) * time.Second
	if ttl == 0 {
		ttl = 7 * 24 * time.Hour
	}
	return elapsed < ttl
}

// h264Filter strips IoTVideo/Gwell HDLC framing (0x7E delimiters)
// from the raw avPayload stream before it reaches ffmpeg. The camera
// sends ~320 bytes of framing before every SPS NAL; without stripping,
// ffmpeg receives non-H.264 garbage that can stall RTSP ANNOUNCE or
// corrupt RTP packets.
//
// Protocol background (Tencent IoTVideo AV layer):
//   - 0x7E is the HDLC-style frame delimiter used by the Gwell P2P SDK
//   - Framing blocks are ~320 bytes of 0x7E/0xFF padding, starting with
//     a 2-byte sub-header (typically 0x40 0x01 = video channel marker)
//   - They appear at every GOP boundary (before each SPS+PPS+IDR group)
//   - DecryptMTPPayload's fallback path returns these verbatim because
//     they lack the 0xFFFFFF88 magic header that marks extracted H.264
type h264Filter struct {
	inner  io.Writer // destination (ffmpeg stdin via writeTracker)
	synced bool      // true after first SPS found
	buf    []byte    // pre-sync buffer
	prefix string    // log prefix (camera ID)
}

// Write filters IoTVideo framing from the H.264 stream.
// Before sync: buffers data until the first Annex B SPS NAL (00 00 00 01 x7)
// is found, then flushes from the SPS onward.
// After sync: scans each chunk for Annex B start codes; if the chunk has
// none and consists mostly of 0x7E/0xFF bytes, it's IoTVideo framing and
// gets dropped. Otherwise it's forwarded (could be a NAL continuation).
func (f *h264Filter) Write(p []byte) (int, error) {
	origLen := len(p)

	if !f.synced {
		f.buf = append(f.buf, p...)
		idx := findSPSStart(f.buf)
		if idx < 0 {
			if len(f.buf) > 64*1024 {
				// Safety valve: if we've buffered 64KB without
				// finding an SPS, flush everything and hope ffmpeg
				// can probe through it.
				log.Printf("%s h264filter: no SPS in %d bytes, flushing raw", f.prefix, len(f.buf))
				f.synced = true
				_, err := f.inner.Write(f.buf)
				f.buf = nil
				return origLen, err
			}
			return origLen, nil // keep buffering
		}
		f.synced = true
		log.Printf("%s h264filter: synced — found SPS at offset %d, discarded %d bytes of IoTVideo framing",
			f.prefix, idx, idx)
		data := f.buf[idx:]
		f.buf = nil
		if len(data) == 0 {
			return origLen, nil
		}
		_, err := f.inner.Write(data)
		return origLen, err
	}

	// Post-sync: drop pure-framing chunks (no start codes, mostly 0x7E/0xFF).
	if !hasAnnexBStartCode(p) && isIoTVideoFraming(p) {
		return origLen, nil // silently drop framing
	}

	// Chunk has H.264 data (or is a NAL continuation). If it starts with
	// framing bytes before the first start code, strip the prefix.
	if idx := findAnnexBStartCode(p); idx > 0 && isIoTVideoFraming(p[:idx]) {
		p = p[idx:]
	}

	_, err := f.inner.Write(p)
	return origLen, err
}

// findSPSStart returns the offset of the first Annex B SPS NAL
// (00 00 00 01 followed by NAL type 7) in buf, or -1.
func findSPSStart(buf []byte) int {
	for i := 0; i+4 < len(buf); i++ {
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 0 && buf[i+3] == 1 {
			if (buf[i+4] & 0x1F) == 7 { // SPS
				return i
			}
		}
	}
	return -1
}

// findAnnexBStartCode returns the offset of the first 4-byte Annex B
// start code (00 00 00 01) in buf, or -1.
func findAnnexBStartCode(buf []byte) int {
	for i := 0; i+3 < len(buf); i++ {
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 0 && buf[i+3] == 1 {
			return i
		}
	}
	return -1
}

// hasAnnexBStartCode returns true if buf contains 00 00 00 01.
func hasAnnexBStartCode(buf []byte) bool {
	return findAnnexBStartCode(buf) >= 0
}

// isIoTVideoFraming returns true if buf consists mostly of IoTVideo HDLC
// framing bytes (0x7E, 0xFF, 0xFE). Threshold: ≥80% framing bytes.
func isIoTVideoFraming(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	count := 0
	for _, b := range buf {
		if b == 0x7E || b == 0xFF || b == 0xFE {
			count++
		}
	}
	return count*5 >= len(buf)*4 // count/len >= 0.80
}

// writeTracker wraps the ffmpeg publisher's stdin to (a) track the
// last-write time atomically for the deadman switch, (b) optionally
// tee every byte to a local file so we can inspect the raw H.264
// Annex B stream offline with ffprobe (enable via --dump-h264 <dir>),
// and (c) filter IoTVideo HDLC framing via h264Filter before ffmpeg.
type writeTracker struct {
	inner     *stream.FFmpegPublisher
	filter    *h264Filter   // nil until wired in streamCamera
	dump      io.WriteCloser // nil when --dump-h264 isn't set
	lastWrite atomic.Int64   // unix nano
}

func (w *writeTracker) Write(p []byte) (int, error) {
	// Always tee the raw (unfiltered) bytes to the dump file first —
	// the dump captures exactly what DecryptMTPPayload returns so we
	// can analyze the IoTVideo framing offline.
	if w.dump != nil && len(p) > 0 {
		_, _ = w.dump.Write(p)
	}

	// Route through the H.264 filter which strips IoTVideo framing.
	var n int
	var err error
	if w.filter != nil {
		n, err = w.filter.Write(p)
	} else {
		n, err = w.inner.Write(p)
	}
	if n > 0 {
		w.lastWrite.Store(time.Now().UnixNano())
	}
	return n, err
}

func (w *writeTracker) lastWriteTime() time.Time {
	ns := w.lastWrite.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Close releases the dump file if one was open. Called from the caller
// when the session tears down. Safe to call repeatedly.
func (w *writeTracker) Close() error {
	if w.dump != nil {
		err := w.dump.Close()
		w.dump = nil
		return err
	}
	return nil
}

// cacheFilePath resolves to $STATE_DIR/gwell/token_cache.json when
// STATE_DIR is set (our bridge's convention), otherwise ./data/.
func cacheFilePath() string {
	if d := os.Getenv("STATE_DIR"); d != "" {
		return filepath.Join(d, "gwell", "token_cache.json")
	}
	return "data/token_cache.json"
}

// shared state protected by sharedMu
var (
	sharedMu      sync.Mutex
	sharedToken   *gwell.AccessToken
	sharedServer  string
	sharedDevices []gwell.DeviceInfo
)

func main() {
	// Configuration flows from the parent wyze-bridge process as CLI
	// flags — the bridge already knows every value it needs to hand us
	// (its own webui host+port for the shim URL, its managed go2rtc's
	// RTSP listener). No env-var fallback: if anyone runs this binary
	// standalone they're opting into setting these explicitly.
	apiURL := flag.String("shim-url", "", "URL of the bridge's wyze-shim, e.g. http://127.0.0.1:5080/internal/wyze")
	rtspHost := flag.String("rtsp-host", "127.0.0.1", "host of the RTSP server we PUSH to (go2rtc)")
	rtspPort := flag.Int("rtsp-port", 8554, "port of the RTSP server we PUSH to")
	dumpDir := flag.String("dump-h264", "", "if set, tee raw H.264 bytes per-camera into <dir>/<cam>-<unix-ms>.h264 for offline analysis")
	ffmpegLogLevel := flag.String("ffmpeg-loglevel", "warning", "ffmpeg -loglevel (quiet/panic/fatal/error/warning/info/verbose/debug/trace); debug spams thousands of lines/second")
	deadmanTimeout := flag.Duration("deadman-timeout", 2*time.Minute, "max no-data interval before forcing reconnect")
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Println("[main] Wyze GWell P2P proxy starting")

	if *apiURL == "" {
		log.Fatalf("[main] --shim-url is required")
	}

	log.Printf("[main] shim=%s RTSP=%s:%d", *apiURL, *rtspHost, *rtspPort)

	client := newWyzeShimClient(*apiURL)

	// Poll the shim until at least one Gwell camera is registered.
	// Two independent reasons the list can be empty temporarily:
	//   1. Bridge is still doing its first Wyze discovery pass
	//   2. User has GWELL_ENABLED=true but no GW_* cameras on their
	//      account yet (e.g. they're buying one tomorrow)
	// Don't fatal in either case — the bridge's supervision loop
	// treats fatal-exit as a crash and restart-storms. Patient
	// polling is the right semantics.
	log.Println("[main] Waiting for Gwell cameras via shim...")
	var cameraIDs []string
	for {
		ids, err := client.GetCameraList()
		if err != nil {
			log.Printf("[main] shim GetCameraList: %v (retrying)", err)
		} else if len(ids) == 0 {
			log.Printf("[main] shim reports 0 Gwell cameras (retrying in 30s)")
		} else {
			cameraIDs = ids
			log.Printf("[main] Found %d camera(s): %v", len(ids), ids)
			break
		}
		time.Sleep(30 * time.Second)
	}

	// Initial discovery
	if err := refreshDiscovery(client, cameraIDs[0], false); err != nil {
		log.Fatalf("[main] Initial discovery failed: %v", err)
	}

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// Start per-camera goroutines with stagger
	var wg sync.WaitGroup
	stopCh := make(chan struct{})

	for i, camID := range cameraIDs {
		if i > 0 {
			log.Printf("[main] Staggering camera start (%s)...", cameraStagger)
			time.Sleep(cameraStagger)
		}

		wg.Add(1)
		go func(cameraID string) {
			defer wg.Done()
			runCamera(client, cameraID, *rtspHost, *rtspPort, *dumpDir, *ffmpegLogLevel, *deadmanTimeout, stopCh)
		}(camID)
	}

	// Token refresh loop
	go func() {
		ticker := time.NewTicker(tokenRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				log.Println("[main] Refreshing token...")
				if err := refreshDiscovery(client, cameraIDs[0], false); err != nil {
					log.Printf("[main] Token refresh failed: %v", err)
				} else {
					log.Println("[main] Token refreshed (will apply on next reconnect)")
				}
			case <-stopCh:
				return
			}
		}
	}()

	// Wait for signal
	sig := <-sigCh
	log.Printf("[main] Received signal %v, shutting down...", sig)
	close(stopCh)
	wg.Wait()
	log.Println("[main] Shutdown complete")
}

// refreshDiscovery fetches a fresh token and runs device discovery.
// Results are stored in shared state for all camera goroutines to use.
func refreshDiscovery(client *wyzeShimClient, anyCameraID string, forceDiscover bool) error {
	cred, err := client.GetCameraToken(anyCameraID)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	token, err := gwell.ParseAccessToken(cred.AccessID, cred.AccessToken)
	if err != nil {
		return fmt.Errorf("parse token: %w", err)
	}

	// Try cache first unless caller explicitly requests a fresh discovery.
	cache := loadCache()
	if !forceDiscover && cache != nil && cache.isValid() && cache.ServerAddr != "" {
		log.Println("[main] Using cached P2P server address:", cache.ServerAddr)
		sharedMu.Lock()
		sharedToken = token
		sharedServer = cache.ServerAddr
		// Keep existing devices if we have them
		sharedMu.Unlock()
	} else {
		if forceDiscover {
			log.Println("[main] Force discovery requested; bypassing cache")
		}
		log.Println("[main] Running device discovery...")
		result, err := gwell.DiscoverDevices(token)
		if err != nil {
			return fmt.Errorf("discovery: %w", err)
		}
		log.Printf("[main] Discovery complete: server=%s, %d device(s)", result.ServerAddr, len(result.Devices))

		sharedMu.Lock()
		sharedToken = token
		sharedServer = result.ServerAddr
		sharedDevices = result.Devices
		sharedMu.Unlock()
	}

	saveCache(&tokenCache{
		AccessID:    cred.AccessID,
		AccessToken: cred.AccessToken,
		ServerAddr:  sharedServer,
		CachedAt:    time.Now().Unix(),
		TTLSeconds:  int64((7 * 24 * time.Hour).Seconds()),
	})

	return nil
}

// getSharedState returns a snapshot of the current shared state.
func getSharedState() (*gwell.AccessToken, string, []gwell.DeviceInfo) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	return sharedToken, sharedServer, sharedDevices
}

// runCamera is the reconnect loop for a single camera.
func runCamera(client *wyzeShimClient, cameraID string,
	rtspHost string, rtspPort int, dumpDir string, ffmpegLogLevel string, deadmanTimeout time.Duration, stopCh chan struct{}) {

	for {
		select {
		case <-stopCh:
			log.Printf("[%s] Stop signal received", cameraID)
			return
		default:
		}

		// Get fresh token on each attempt
		cred, err := client.GetCameraToken(cameraID)
		if err != nil {
			log.Printf("[%s] Token fetch failed: %v, using shared token", cameraID, err)
		} else {
			newToken, err := gwell.ParseAccessToken(cred.AccessID, cred.AccessToken)
			if err != nil {
				log.Printf("[%s] Token parse failed: %v, using shared token", cameraID, err)
			} else {
				sharedMu.Lock()
				sharedToken = newToken
				sharedMu.Unlock()
			}
		}

		err = streamCamera(client, cameraID, rtspHost, rtspPort, dumpDir, ffmpegLogLevel, deadmanTimeout, stopCh)
		if err != nil {
			log.Printf("[%s] Stream error: %v", cameraID, err)
			log.Printf("[%s] reconnect reason: stream_error", cameraID)
			if derr := refreshDiscovery(client, cameraID, true); derr != nil {
				log.Printf("[%s] Re-discovery before reconnect failed: %v", cameraID, derr)
			}
		}

		select {
		case <-stopCh:
			return
		case <-time.After(reconnectDelay):
			log.Printf("[%s] Reconnecting...", cameraID)
		}
	}
}

// streamCamera runs a single streaming session for one camera.
// It acquires connectMu to serialize the P2P handshake phase.
func streamCamera(client *wyzeShimClient, cameraID string,
	rtspHost string, rtspPort int, dumpDir string, ffmpegLogLevel string, deadmanTimeout time.Duration, stopCh chan struct{}) error {

	// Get device info for stream name and LAN IP
	info, err := client.GetDeviceInfo(cameraID)
	if err != nil {
		return fmt.Errorf("get device info: %w", err)
	}

	streamName := info.StreamName
	if streamName == "" {
		streamName = cameraID
	}
	streamPath := streamName
	// Bridge convention: stream paths land at rtsp://go2rtc:8554/<name>
	// directly (no "live/" prefix that upstream mediamtx convention used).
	// If your go2rtc is configured to expect a prefix, set GWELL_STREAM_PREFIX.
	if prefix := os.Getenv("GWELL_STREAM_PREFIX"); prefix != "" && !strings.HasPrefix(streamPath, prefix) {
		streamPath = prefix + streamPath
	}

	log.Printf("[%s] Starting stream: %s (LAN IP: %s)", cameraID, streamPath, info.LanIP)

	// Start ffmpeg publisher
	ffmpeg, err := stream.StartFFmpegPublisher(streamPath, rtspHost, rtspPort, ffmpegLogLevel)
	if err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	defer ffmpeg.Close()

	// Wrap with write tracker for deadman switch + H.264 framing filter
	tracker := &writeTracker{inner: ffmpeg}
	tracker.filter = &h264Filter{
		inner:  ffmpeg,
		prefix: fmt.Sprintf("[%s]", cameraID),
	}
	tracker.lastWrite.Store(time.Now().UnixNano())
	defer tracker.Close()

	// Optional raw-H.264 dump for offline ffprobe inspection. One file
	// per session, timestamped so successive reconnects don't clobber
	// each other — analyze any of them to confirm the camera stream is
	// well-formed before we blame ffmpeg/go2rtc downstream.
	if dumpDir != "" {
		if err := os.MkdirAll(dumpDir, 0755); err != nil {
			log.Printf("[%s] dump: mkdir %s: %v (continuing without dump)", cameraID, dumpDir, err)
		} else {
			path := filepath.Join(dumpDir, fmt.Sprintf("%s-%d.h264", cameraID, time.Now().UnixMilli()))
			f, err := os.Create(path)
			if err != nil {
				log.Printf("[%s] dump: create %s: %v (continuing without dump)", cameraID, path, err)
			} else {
				log.Printf("[%s] dump: writing raw H.264 to %s", cameraID, path)
				tracker.dump = f
			}
		}
	}

	// === SERIALIZED SECTION ===
	// Acquire the connect mutex so only one camera is doing the P2P
	// handshake at a time. This prevents cross-session interference
	// on the P2P server.
	log.Printf("[%s] Waiting for connect lock...", cameraID)
	connectMu.Lock()
	log.Printf("[%s] Acquired connect lock, starting P2P session", cameraID)

	token, serverAddr, devices := getSharedState()

	sess := gwell.NewSession(gwell.SessionConfig{
		Token:       token,
		ServerAddr:  serverAddr,
		CameraLanIP: info.LanIP,
		DeviceName:  cameraID,
		H264Writer:  tracker,
		Devices:     devices,
	})

	// Run the session — it will go through connect, certify, initInfo,
	// subscribe, calling, and then enter streamLoop. We release the
	// lock after a delay to let the handshake complete before the next
	// camera tries.
	errCh := make(chan error, 1)
	go func() {
		errCh <- sess.Run(cameraID)
	}()

	// Wait for either: streaming started (give it time), error, or stop
	// Release the lock after the stagger delay so the next camera can go
	go func() {
		time.Sleep(cameraStagger)
		connectMu.Unlock()
		log.Printf("[%s] Released connect lock", cameraID)
	}()
	// === END SERIALIZED SECTION ===

	// Monitor: deadman switch + ffmpeg health + stop signal
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-errCh:
			return fmt.Errorf("session ended: %w", err)

		case <-stopCh:
			sess.Close()
			return nil

		case <-ticker.C:
			// Check ffmpeg health
			if !ffmpeg.Alive() {
				sess.Close()
				return fmt.Errorf("ffmpeg process died")
			}

			// Deadman switch: no data for too long
			last := tracker.lastWriteTime()
			if !last.IsZero() && time.Since(last) > deadmanTimeout {
				sess.Close()
				return fmt.Errorf("deadman timeout: no stream data for %s", deadmanTimeout)
			}
		}
	}
}

func loadCache() *tokenCache {
	data, err := os.ReadFile(cacheFilePath())
	if err != nil {
		return nil
	}
	var tc tokenCache
	if err := json.Unmarshal(data, &tc); err != nil {
		return nil
	}
	return &tc
}

func saveCache(tc *tokenCache) {
	path := cacheFilePath()
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0755)
	data, err := json.MarshalIndent(tc, "", "  ")
	if err != nil {
		log.Printf("[cache] Failed to marshal: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("[cache] Failed to write %s: %v", path, err)
		return
	}
	log.Printf("[cache] Saved token cache to %s", path)
}
