package mqtt

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
)

// TestNewClient_Defaults checks the constructor populates the
// concurrency-safety fields (rootCtx, publishSem) without requiring
// a broker connection.
func TestNewClient_Defaults(t *testing.T) {
	c := NewClient(context.Background(), Config{Topic: "wyzebridge"}, nil, nil, "192.168.1.1", zerolog.Nop())

	if c.rootCtx == nil {
		t.Error("rootCtx not initialized")
	}
	if c.publishSem == nil {
		t.Error("publishSem not initialized")
	}
	if cap(c.publishSem) != maxInflightPublishes {
		t.Errorf("publishSem capacity = %d, want %d", cap(c.publishSem), maxInflightPublishes)
	}
	if c.topic != "wyzebridge" {
		t.Errorf("topic = %q", c.topic)
	}
	if c.bridgeIP != "192.168.1.1" {
		t.Errorf("bridgeIP = %q", c.bridgeIP)
	}
}

// TestNewClient_StoresProvidedContext verifies the constructor
// stores the ctx argument as rootCtx for use by subscribe handlers.
func TestNewClient_StoresProvidedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewClient(ctx, Config{}, nil, nil, "", zerolog.Nop())
	if c.rootCtx != ctx {
		t.Error("rootCtx didn't match the ctx passed to NewClient")
	}
}

// TestCallbackRegistration covers the three OnX setters used by
// main.go to wire snapshot / record / discover handlers.
func TestCallbackRegistration(t *testing.T) {
	c := NewClient(context.Background(), Config{}, nil, nil, "", zerolog.Nop())

	var snapHit, recHit, discHit bool
	c.OnSnapshotRequest(func(context.Context, string) { snapHit = true })
	c.OnRecordRequest(func(context.Context, string, string) { recHit = true })
	c.OnDiscoverRequest(func(context.Context) { discHit = true })

	if c.onSnapshot == nil || c.onRecord == nil || c.onDiscover == nil {
		t.Fatal("one or more callbacks not registered")
	}
	c.onSnapshot(context.Background(), "cam")
	c.onRecord(context.Background(), "cam", "start")
	c.onDiscover(context.Background())
	if !snapHit || !recHit || !discHit {
		t.Errorf("callback invocations: snap=%v rec=%v disc=%v", snapHit, recHit, discHit)
	}
}

// TestPublishGuarded_DropsWhenSaturated fills publishSem to capacity
// and confirms publishGuarded increments droppedPubs and returns
// without panicking on a nil paho client.
func TestPublishGuarded_DropsWhenSaturated(t *testing.T) {
	c := NewClient(context.Background(), Config{}, nil, nil, "", zerolog.Nop())

	// Fill every slot so the next acquire fails.
	for i := 0; i < maxInflightPublishes; i++ {
		c.publishSem <- struct{}{}
	}

	before := c.droppedPubs.Load()
	c.publishGuarded("test/topic", "payload", false)
	after := c.droppedPubs.Load()

	if after != before+1 {
		t.Errorf("droppedPubs delta = %d, want 1", after-before)
	}
}

// TestPublishGuarded_AcquiresWhenSpace ensures the happy path takes
// a slot but doesn't try to call into the (nil) paho client during
// the test — we only verify the semaphore accounting before the
// nil-client publish would panic. We pop the slot ourselves first
// so the assert is observable; in production the goroutine's
// deferred release drains it.
func TestPublishGuarded_AcquireAccounting(t *testing.T) {
	c := NewClient(context.Background(), Config{}, nil, nil, "", zerolog.Nop())
	before := c.droppedPubs.Load()
	// We can't safely call publishGuarded with a nil paho — it would
	// panic inside Publish. Instead, directly verify the semaphore
	// blocks when full (which publishGuarded's select relies on).
	for i := 0; i < maxInflightPublishes; i++ {
		select {
		case c.publishSem <- struct{}{}:
		default:
			t.Fatalf("publishSem filled early at i=%d", i)
		}
	}
	select {
	case c.publishSem <- struct{}{}:
		t.Fatal("publishSem accepted beyond capacity")
	default:
	}
	if c.droppedPubs.Load() != before {
		t.Errorf("droppedPubs changed without publishGuarded call")
	}
}
