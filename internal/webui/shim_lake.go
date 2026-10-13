package webui

import (
	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/lake"
	"net/http"
	"strings"
)

type LakeSessionProvider = lake.Provider

// Only the co-located go2rtc process can request Lake signaling. Browser origins
// are rejected by the broker; account tokens and media keys never leave Bridge.
func (s *Server) handleShimLakeStream(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/internal/wyze/lake-stream/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	cam := s.camMgr.GetCamera(name)
	if cam == nil || cam.GetInfo().Model != "HL_PAN4" || cam.GetState() == camera.StateOffline {
		http.NotFound(w, r)
		return
	}
	if s.lakeBroker == nil {
		http.Error(w, "Lake unavailable", http.StatusServiceUnavailable)
		return
	}
	info := cam.GetInfo()
	s.lakeBroker.ServeCamera(w, r, name, info.MAC, info.Model, "2k", func() bool {
		return cam.GetState() != camera.StateOffline
	})
}

func (s *Server) CloseLakeCamera(name string) {
	if s.lakeBroker != nil {
		s.lakeBroker.CloseCamera(name)
	}
}

func (s *Server) CloseIdleLakeCamera(name string) {
	if s.lakeBroker != nil {
		s.lakeBroker.CloseIdleCamera(name)
	}
}
