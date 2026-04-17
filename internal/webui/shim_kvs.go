package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// KVSStreamProvider mints the Wyze WebRTC signaling URL + ICE servers
// for a specific camera via /v4/camera/get_streams. Injected at startup
// so this package stays independent of internal/wyzeapi.
type KVSStreamProvider interface {
	GetCameraStream(ctx context.Context, mac, model string) (signalingURL string, iceServers []KVSIceServer, authToken string, err error)
}

// KVSIceServer matches the shape go2rtc's kinesis client deserializes.
type KVSIceServer struct {
	URL        string `json:"url"`
	Username   string `json:"username"`
	Credential string `json:"credential"`
}

// SetKVSProvider attaches the Wyze get_streams provider.
func (s *Server) SetKVSProvider(p KVSStreamProvider) {
	s.kvs = p
}

// handleShimKVSSignaling is the one-shot bootstrap go2rtc's #format=wyze
// source fetches when it's about to dial Wyze's KVS signaling server.
// The go2rtc YAML contains: webrtc:http://<loopback>/internal/wyze/webrtc/<cam>#format=wyze
// and go2rtc expects JSON back shaped like:
//
//	{"ClientId":"<phone_id>","cam":"<name>","result":"ok",
//	 "servers":[{"url":...,"username":...,"credential":...}],
//	 "signalingUrl":"wss://wyze-mars-webcsrv.wyzecam.com?token=..."}
//
// ClientId's capital I matters — that's what go2rtc's wyzeKVS struct
// tag wants. go2rtc handles everything downstream (WSS dial, SDP
// negotiation, track fanout).
func (s *Server) handleShimKVSSignaling(w http.ResponseWriter, r *http.Request) {
	if s.kvs == nil {
		http.Error(w, "kvs provider not wired", http.StatusServiceUnavailable)
		return
	}
	streamID := strings.TrimPrefix(r.URL.Path, "/internal/wyze/webrtc/")
	streamID = strings.TrimSpace(streamID)
	if streamID == "" || strings.Contains(streamID, "/") {
		http.Error(w, "streamID required", http.StatusBadRequest)
		return
	}

	cam := s.camMgr.GetCamera(streamID)
	if cam == nil {
		http.NotFound(w, r)
		return
	}
	info := cam.GetInfo()
	if !info.IsWebRTCStreamer() {
		http.Error(w, "camera does not use WebRTC streaming", http.StatusNotFound)
		return
	}

	signalingURL, iceServers, _, err := s.kvs.GetCameraStream(r.Context(), info.MAC, info.Model)
	if err != nil {
		s.log.Warn().Err(err).Str("cam", streamID).Msg("kvs get_streams failed")
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// ClientId is the Wyze account's phone_id — matches what the app
	// uses as recipientClientId when initiating its own WebRTC session.
	clientID := ""
	if s.authPhoneID != nil {
		clientID = s.authPhoneID()
	}

	payload := map[string]interface{}{
		"ClientId":     clientID,
		"cam":          streamID,
		"result":       "ok",
		"servers":      iceServers,
		"signalingUrl": signalingURL,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
