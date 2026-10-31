package webui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/lake"
)

// qualityProducerIDs rejects idle feeds: the probe must only consume the
// already-shared receive path, never keep an unused camera awake by itself.
func qualityProducerIDs(streams map[string]*go2rtcmgr.StreamInfo, name string) []int64 {
	var ids []int64
	for _, key := range []string{name + "_lake", name + "_4k"} {
		s := streams[key]
		if s == nil || len(s.Consumers) == 0 || len(s.Producers) != 1 || s.Producers[0].ID == 0 || s.Producers[0].BytesRecv <= 0 {
			return nil
		}
		ids = append(ids, s.Producers[0].ID)
	}
	return ids
}

func (s *Server) probeLakeQuality(ctx context.Context, name string) (lake.Resolution, error) {
	api := s.go2rtc()
	if api == nil || s.cfg.Go2RTCURL != "" {
		return lake.Resolution{}, errors.New("local receiver unavailable")
	}
	before, err := api.ListStreams(ctx)
	if err != nil {
		return lake.Resolution{}, errors.New("receiver status unavailable")
	}
	ids := qualityProducerIDs(before, name)
	if ids == nil {
		return lake.Resolution{}, nil
	}
	port := s.cfg.Go2RTCRTSPPort
	if port == 0 {
		port = 8554
	}
	source := &url.URL{Scheme: "rtsp", Host: fmt.Sprintf("127.0.0.1:%d", port), Path: "/" + name + "_4k"}
	auth := go2rtcmgr.ParseStreamAuth(s.cfg.StreamAuth)
	if len(auth) == 1 && len(auth[0].Cameras) == 0 {
		source.User = url.UserPassword(auth[0].Username, auth[0].Password)
	}
	w, h, err := go2rtcmgr.ProbeDecodedResolution(ctx, source.String())
	if err != nil {
		return lake.Resolution{}, err
	}
	after, err := api.ListStreams(ctx)
	if err != nil || !slices.Equal(ids, qualityProducerIDs(after, name)) {
		return lake.Resolution{}, errors.New("receiver changed during sample")
	}
	return lake.Resolution{Width: w, Height: h}, nil
}
