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
	deadmanTimeout       = 120 * time.Second
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

// writeTracker wraps an io.Writer to track the last-write time atomically.
type writeTracker struct {
	inner     *stream.FFmpegPublisher
	lastWrite atomic.Int64 // unix nano
}

func (w *writeTracker) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
	if err := refreshDiscovery(client, cameraIDs[0]); err != nil {
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
			runCamera(client, cameraID, *rtspHost, *rtspPort, stopCh)
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
				if err := refreshDiscovery(client, cameraIDs[0]); err != nil {
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
func refreshDiscovery(client *wyzeShimClient, anyCameraID string) error {
	cred, err := client.GetCameraToken(anyCameraID)
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}

	token, err := gwell.ParseAccessToken(cred.AccessID, cred.AccessToken)
	if err != nil {
		return fmt.Errorf("parse token: %w", err)
	}

	// Try cache first
	cache := loadCache()
	if cache != nil && cache.isValid() && cache.ServerAddr != "" {
		log.Println("[main] Using cached P2P server address:", cache.ServerAddr)
		sharedMu.Lock()
		sharedToken = token
		sharedServer = cache.ServerAddr
		// Keep existing devices if we have them
		sharedMu.Unlock()
	} else {
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
	rtspHost string, rtspPort int, stopCh chan struct{}) {

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

		err = streamCamera(client, cameraID, rtspHost, rtspPort, stopCh)
		if err != nil {
			log.Printf("[%s] Stream error: %v", cameraID, err)
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
	rtspHost string, rtspPort int, stopCh chan struct{}) error {

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
	ffmpeg, err := stream.StartFFmpegPublisher(streamPath, rtspHost, rtspPort)
	if err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	defer ffmpeg.Close()

	// Wrap with write tracker for deadman switch
	tracker := &writeTracker{inner: ffmpeg}
	tracker.lastWrite.Store(time.Now().UnixNano())

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
