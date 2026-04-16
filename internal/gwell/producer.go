package gwell

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// AuthProvider is the subset of *wyzeapi.Client the producer needs.
// Defined as an interface so tests can swap in a fake without bringing
// a real HTTP client online.
type AuthProvider interface {
	EnsureAuth() error
	Auth() *wyzeapi.AuthState
}

// Producer turns a Gwell CameraInfo into a go2rtc-friendly RTSP URL by
// registering the camera with the gwell-proxy subprocess. It is the
// single entry point that internal/camera/manager.go uses to wire a
// Gwell camera into the existing streaming pipeline.
type Producer struct {
	mgr    *Manager
	client *APIClient
	auth   AuthProvider
	log    zerolog.Logger

	// bridgeVersion is echoed to the proxy for logging/useragent.
	bridgeVersion string
}

// NewProducer wires a manager + auth source + logger together. The
// manager is only started (subprocess spawned) on first Connect.
func NewProducer(mgr *Manager, auth AuthProvider, bridgeVersion string, log zerolog.Logger) *Producer {
	return &Producer{
		mgr:           mgr,
		client:        NewAPIClient(mgr.Config().BaseURL(), log),
		auth:          auth,
		log:           log,
		bridgeVersion: bridgeVersion,
	}
}

// Enabled returns true if the Gwell integration is active.
func (p *Producer) Enabled() bool {
	return p.mgr.Config().Enabled
}

// Connect registers (or refreshes) a Gwell camera with the proxy and
// returns the rtsp:// URL that can be handed to go2rtc.AddStream.
//
// It is idempotent: calling it twice for the same camera is safe and
// will just refresh the cloud token / lan IP.
func (p *Producer) Connect(ctx context.Context, info wyzeapi.CameraInfo, camName, quality string, audio bool) (string, error) {
	if !p.Enabled() {
		return "", ErrDisabled
	}
	if !info.IsGwell() {
		return "", fmt.Errorf("gwell: camera %q (model %s) is not a Gwell model", info.Nickname, info.Model)
	}

	// Make sure we have a fresh Wyze cloud token, then grab it.
	if err := p.auth.EnsureAuth(); err != nil {
		return "", fmt.Errorf("gwell: ensure wyze auth: %w", err)
	}
	state := p.auth.Auth()
	if state == nil || state.AccessToken == "" {
		return "", ErrNoAuth
	}

	// Lazy-start the subprocess on the first Connect.
	if err := p.mgr.EnsureStarted(ctx); err != nil {
		return "", err
	}

	req := RegisterRequest{
		Name:        camName,
		MAC:         info.MAC,
		ENR:         info.ENR,
		Model:       info.Model,
		LanIP:       info.LanIP,
		AccessToken: state.AccessToken,
		PhoneID:     state.PhoneID,
		UserID:      state.UserID,
		AppVersion:  p.bridgeVersion,
		Quality:     quality,
		Audio:       audio,
	}

	resp, err := p.client.Register(ctx, req)
	if err != nil {
		return "", err
	}

	// Honor the server's RTSP URL if provided; otherwise use the
	// deterministic form from our config.
	rtsp := resp.RTSPURL
	if rtsp == "" {
		rtsp = p.mgr.Config().RTSPURL(camName)
	}

	p.log.Info().
		Str("cam", camName).
		Str("model", info.ModelName()).
		Str("rtsp", rtsp).
		Msg("gwell camera registered with proxy")

	return rtsp, nil
}

// Disconnect tells the proxy to tear down a camera's session.
// Unknown cameras are a no-op.
func (p *Producer) Disconnect(ctx context.Context, camName string) error {
	if !p.Enabled() || !p.mgr.Started() {
		return nil
	}
	return p.client.Unregister(ctx, camName)
}
