package lake

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/gorilla/websocket"
)

// Provider reuses Bridge's Wyze login and refresh logic. Credentials never pass
// through go2rtc configuration, SDP responses or protocol logs.
type Provider interface {
	GetLakeSession(context.Context, string, string) (*wyzeapi.LakeSession, error)
	RenewLakeToken(context.Context, string, string, int) (string, error)
}

type cameraOperation struct{ cancel context.CancelFunc }

type Broker struct {
	OnError  func(string, error)
	OnEvent  func(string, Event)
	ctx      context.Context
	provider Provider
	mu       sync.Mutex
	sessions map[string]*Session
	locks    map[string]*sync.Mutex
	pending  map[string]*cameraOperation
	epochs   map[string]uint64
	stops    map[string]uint64
	cancel   context.CancelFunc
}

func NewBroker(ctx context.Context, p Provider) *Broker {
	ctx, cancel := context.WithCancel(ctx)
	b := &Broker{ctx: ctx, cancel: cancel, provider: p, sessions: map[string]*Session{}, locks: map[string]*sync.Mutex{}, pending: map[string]*cameraOperation{}, epochs: map[string]uint64{}, stops: map[string]uint64{}}
	if ctx.Done() != nil {
		go func() { <-ctx.Done(); b.Close() }()
	}
	return b
}
func (b *Broker) Close() {
	b.cancel()
	b.mu.Lock()
	sessions := b.sessions
	pending := b.pending
	b.pending = map[string]*cameraOperation{}
	b.sessions = map[string]*Session{}
	b.mu.Unlock()
	for _, operation := range pending {
		operation.cancel()
	}
	for _, s := range sessions {
		s.Close()
	}
}
func (b *Broker) CloseCamera(name string) {
	b.mu.Lock()
	b.epochs[name]++
	b.stops[name]++
	s := b.sessions[name]
	operation := b.pending[name]
	delete(b.pending, name)
	delete(b.sessions, name)
	b.mu.Unlock()
	if operation != nil {
		operation.cancel()
	}
	if s != nil {
		s.Close()
	}
}

// CloseIdleCamera is called only after go2rtc reports no consumers. A pending
// negotiation has not acquired its consumers yet, and a newly established
// session needs time for ICE and the first keyframe; neither is truly idle.
func (b *Broker) CloseIdleCamera(name string) {
	b.mu.Lock()
	s := b.sessions[name]
	if s == nil || time.Since(s.started) < time.Minute {
		b.mu.Unlock()
		return
	}
	b.epochs[name]++
	b.stops[name]++
	operation := b.pending[name]
	delete(b.pending, name)
	delete(b.sessions, name)
	b.mu.Unlock()
	if operation != nil {
		operation.cancel()
	}
	s.Close()
}

// ServeCamera speaks the existing go2rtc websocket offer/answer protocol.
// The caller must enforce loopback-only access and validate the camera model.
// active is rechecked under ownership; it must not acquire caller locks that
// can call back into Broker.
func (b *Broker) ServeCamera(w http.ResponseWriter, r *http.Request, name, mac, model, quality string, active func() bool) {
	b.mu.Lock()
	stopEpoch := b.stops[name]
	b.mu.Unlock()
	if b.ctx.Err() != nil || b.provider == nil {
		http.Error(w, "Lake unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "websocket GET required", http.StatusMethodNotAllowed)
		return
	}
	upgrader := websocket.Upgrader{HandshakeTimeout: 5 * time.Second, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(65536)
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var offer string
	for i := 0; i < 16; i++ {
		var m struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		if conn.ReadJSON(&m) != nil {
			return
		}
		if m.Type == "webrtc/offer" {
			offer = m.Value
			break
		}
		if m.Type != "webrtc/candidate" {
			return
		}
	}
	if offer == "" {
		return
	}
	fail := func() {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = conn.WriteJSON(object{"type": "error", "value": "Lake negotiation failed"})
	}
	// Reject invalid SDP before provisioning a real camera session.
	if _, err := parseOffer(offer); err != nil {
		if b.OnError != nil {
			b.OnError(name, err)
		}
		fail()
		return
	}
	b.mu.Lock()
	// An already accepted socket may still be waiting for its offer when a
	// stop arrives. A later valid offer cannot acquire a new ownership epoch
	// in that case. Other offers may still supersede each other normally.
	if b.stops[name] != stopEpoch {
		b.mu.Unlock()
		fail()
		return
	}
	b.epochs[name]++
	epoch := b.epochs[name]
	lock := b.locks[name]
	if lock == nil {
		lock = &sync.Mutex{}
		b.locks[name] = lock
	}
	b.mu.Unlock()
	lock.Lock()
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()
	cameraCtx, cameraCancel := context.WithCancel(b.ctx)
	operation := &cameraOperation{cancel: cameraCancel}
	adopted := false
	b.mu.Lock()
	// A stop or a newer offer may arrive while this request waits for the
	// previous negotiation. Adopt ownership atomically with the check so
	// a queued request cannot recreate a stopped camera session.
	if b.epochs[name] != epoch || cameraCtx.Err() != nil || (active != nil && !active()) {
		b.mu.Unlock()
		cameraCancel()
		fail()
		return
	}
	previous := b.sessions[name]
	previousOperation := b.pending[name]
	delete(b.sessions, name)
	b.pending[name] = operation
	b.mu.Unlock()
	if previousOperation != nil {
		previousOperation.cancel()
	}
	if previous != nil {
		previous.Close()
	}
	defer func() {
		if !adopted {
			cameraCancel()
			b.mu.Lock()
			if b.pending[name] == operation {
				delete(b.pending, name)
			}
			b.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(cameraCtx, 60*time.Second)
	initial, err := b.provider.GetLakeSession(ctx, mac, model)
	cancel()
	if err != nil {
		if b.OnError != nil {
			b.OnError(name, err)
		}
		fail()
		return
	}
	session, answer, err := Open(cameraCtx, initial, offer, quality, func(ctx context.Context, uid int) (string, error) {
		return b.provider.RenewLakeToken(ctx, mac, model, uid)
	})
	if err != nil {
		if b.OnError != nil {
			b.OnError(name, err)
		}
		fail()
		return
	}
	b.mu.Lock()
	if cameraCtx.Err() != nil {
		b.mu.Unlock()
		session.Close()
		return
	}
	old := b.sessions[name]
	b.sessions[name] = session
	adopted = true
	b.mu.Unlock()
	if old != nil {
		old.Close()
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if conn.WriteJSON(object{"type": "webrtc/answer", "value": answer}) != nil {
		session.Close()
		b.release(name, session, operation)
		cameraCancel()
		return
	}
	lock.Unlock()
	locked = false
	// go2rtc closes this socket after ICE connects. Its r.Context is therefore
	// deliberately not the parent of the persistent gateway session.
	go func() {
		for {
			select {
			case event := <-session.Events():
				if b.OnEvent != nil {
					b.OnEvent(name, event)
				}
			case <-session.Done():
				goto finished
			}
		}
	finished:

		b.release(name, session, operation)
		cameraCancel()
	}()
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	for {
		var m object
		if conn.ReadJSON(&m) != nil {
			return
		}
	}
}

func (b *Broker) release(name string, session *Session, operation *cameraOperation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions[name] == session {
		delete(b.sessions, name)
	}
	if b.pending[name] == operation {
		delete(b.pending, name)
	}
}

func (b *Broker) Active() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.sessions) }
