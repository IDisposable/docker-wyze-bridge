package webui

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
)

// MarsTokenMinter mints Gwell-specific credentials for a camera by
// calling Wyze's cloud /plugin/mars/v2/regist_gw_user/<deviceID>
// endpoint. Injected at startup (see Server.SetMarsMinter) so the
// webui package stays independent of internal/wyzeapi. A nil minter
// makes the CameraToken endpoint return 503 — useful during bring-up
// before Mars signing is ported.
type MarsTokenMinter interface {
	MarsRegisterGWUser(ctx context.Context, deviceID string) (accessID, accessToken string, err error)
}

// SetMarsMinter attaches the Wyze Mars credential minter used by the
// wyze-shim's CameraToken endpoint. Called from main.go after the
// wyzeapi.Client is constructed.
func (s *Server) SetMarsMinter(m MarsTokenMinter) {
	s.mars = m
}

// Shim endpoints live under /internal/wyze/Camera/* and re-expose our
// bridge's state in the shape the gwell-proxy subprocess expects
// (matches wlatic/hacky-wyze-gwell's Python wyze-api wire format so
// the upstream proxy can be ported verbatim). They are loopback-only;
// registerShimRoutes wraps every handler in requireLoopback.

// handleShimCameraList returns the list of Gwell camera IDs the bridge
// currently knows about. Uses the camera MAC as the canonical Wyze
// device_id since that's what Wyze's Mars endpoint keys on.
//
//	GET /internal/wyze/Camera/CameraList  ->  ["AABBCCDDEEFF", ...]
func (s *Server) handleShimCameraList(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0)
	// Diagnostic: log every camera's model + IsGwell verdict on the
	// first few polls. Pre-answer the question "why does the shim
	// return empty even though I have a Gwell camera" — without
	// this the only way to debug is to run the bridge under a
	// debugger.
	all := s.camMgr.Cameras()
	for _, cam := range all {
		if cam.Info.IsGwell() {
			ids = append(ids, cam.Info.MAC)
		}
	}
	s.log.Debug().
		Int("total_cameras", len(all)).
		Int("gwell_cameras", len(ids)).
		Msg("shim CameraList")
	if len(all) > 0 && len(ids) == 0 {
		// Spell out WHY every camera was excluded. Common cause:
		// Wyze ships a new model code that isn't in our gwellModels
		// map yet.
		for _, cam := range all {
			s.log.Debug().
				Str("cam", cam.Name()).
				Str("model", cam.Info.Model).
				Bool("is_gwell", cam.Info.IsGwell()).
				Msg("shim CameraList: skipped (not Gwell)")
		}
	}
	writeJSON(w, ids)
}

// handleShimDeviceInfo returns the per-camera metadata the Gwell
// handshake needs: the RTSP stream path (go2rtc's stream name —
// typically the normalized camera name) and LAN IP (may be empty;
// gwell's DiscoverDevices path recovers it over P2P).
//
//	GET /internal/wyze/Camera/DeviceInfo?deviceId=<MAC>
//	   -> {"cameraId":"AABBCCDDEEFF","streamName":"front_door","lanIp":"10.0.0.42"}
func (s *Server) handleShimDeviceInfo(w http.ResponseWriter, r *http.Request) {
	id := strings.ToUpper(r.URL.Query().Get("deviceId"))
	if id == "" {
		http.Error(w, "deviceId required", http.StatusBadRequest)
		return
	}
	cam := s.findGwellCameraByMAC(id)
	if cam == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]string{
		"cameraId":   cam.Info.MAC,
		"streamName": cam.Name(),
		"lanIp":      cam.Info.LanIP,
	})
}

// handleShimCameraToken mints a fresh Gwell-scoped accessId/accessToken
// for the given camera via the Wyze Mars endpoint. Returns 503 when the
// Mars minter is not yet attached (expected during bring-up before
// wyzeapi.MarsRegisterGWUser ports), 502 when the upstream call fails.
//
//	GET /internal/wyze/Camera/CameraToken?deviceId=<MAC>
//	   -> {"accessId":"123456","accessToken":"<128 hex chars>"}
func (s *Server) handleShimCameraToken(w http.ResponseWriter, r *http.Request) {
	if s.mars == nil {
		http.Error(w, "mars token minter not wired", http.StatusServiceUnavailable)
		return
	}
	id := strings.ToUpper(r.URL.Query().Get("deviceId"))
	if id == "" {
		http.Error(w, "deviceId required", http.StatusBadRequest)
		return
	}
	if s.findGwellCameraByMAC(id) == nil {
		http.NotFound(w, r)
		return
	}
	accessID, accessToken, err := s.mars.MarsRegisterGWUser(r.Context(), id)
	if err != nil {
		s.log.Warn().Err(err).Str("cam", id).Msg("mars token mint failed")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{
		"accessId":    accessID,
		"accessToken": accessToken,
	})
}

// findGwellCameraByMAC returns the camera matching the given
// uppercase MAC, or nil if no Gwell camera has that MAC.
func (s *Server) findGwellCameraByMAC(mac string) *camera.Camera {
	for _, cam := range s.camMgr.Cameras() {
		if cam.Info.IsGwell() && strings.EqualFold(cam.Info.MAC, mac) {
			return cam
		}
	}
	return nil
}

// requireLoopback rejects any request not originating from 127.0.0.1
// or ::1. The bridge uses host_network in HA / shared network in bare
// Docker, so a reachable /internal path would expose Mars-scoped
// credentials to anything on the LAN. Check is done on r.RemoteAddr
// which http.Server populates from the TCP peer — no way to spoof
// through our own proxies since those rewrite to 127.0.0.1 themselves.
func requireLoopback(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if host != "127.0.0.1" && host != "::1" {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}
}
