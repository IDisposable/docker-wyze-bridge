package mqtt

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
	"github.com/rs/zerolog"
)

func TestParseCruisePointCommand(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    cruisePointCommand
		wantErr bool
	}{
		{name: "bare slot means goto", payload: "2", want: cruisePointCommand{action: "goto", slot: 2}},
		{name: "save JSON", payload: `{"action":"save","slot":1}`, want: cruisePointCommand{action: "save", slot: 1}},
		{name: "goto JSON", payload: `{"action":"goto","slot":4}`, want: cruisePointCommand{action: "goto", slot: 4}},
		{name: "trim and normalize action", payload: ` {"action":"SAVE","slot":3} `, want: cruisePointCommand{action: "save", slot: 3}},
		{name: "slot out of range", payload: "5", wantErr: true},
		{name: "missing slot", payload: `{"action":"save"}`, wantErr: true},
		{name: "invalid action", payload: `{"action":"delete","slot":1}`, wantErr: true},
		{name: "invalid payload", payload: "save 1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCruisePointCommand(tt.payload)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCruisePointCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseCruisePointCommand() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMarshalMQTTCommandResult(t *testing.T) {
	got, err := marshalMQTTCommandResult(mqttCommandResult{
		Command: "rotary_action", Direction: "stop", Status: "error", Delivery: "not_sent", Error: "discovery timeout",
	})
	if err != nil {
		t.Fatalf("marshalMQTTCommandResult() error = %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	want := map[string]string{
		"command":   "rotary_action",
		"direction": "stop",
		"status":    "error",
		"delivery":  "not_sent",
		"error":     "discovery timeout",
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestStopCommandOutcomeReportsDelivery(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     string
		wantSend string
	}{
		{name: "acknowledged", want: "ok", wantSend: string(wyzeapi.PTZDeliveryAcknowledged)},
		{
			name: "failed before send",
			err:  &wyzeapi.PTZError{Delivery: wyzeapi.PTZDeliveryNotSent, Err: errors.New("discovery timeout")},
			want: "error", wantSend: string(wyzeapi.PTZDeliveryNotSent),
		},
		{
			name: "acknowledgement not received",
			err:  &wyzeapi.PTZError{Delivery: wyzeapi.PTZDeliveryUnknown, Err: errors.New("send PTZ action: timeout")},
			want: "error", wantSend: string(wyzeapi.PTZDeliveryUnknown),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mqttCommandResult{Command: "rotary_action", Direction: "stop"}
			setPTZCommandOutcome(&result, tt.err)
			if result.Status != tt.want || result.Delivery != tt.wantSend {
				t.Fatalf("outcome = (%q, %q), want (%q, %q)", result.Status, result.Delivery, tt.want, tt.wantSend)
			}
		})
	}
}

func TestStopPrioritizesFullPTZQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	publishSem := make(chan struct{}, 1)
	publishSem <- struct{}{}
	client := &Client{
		log:        zerolog.Nop(),
		rootCtx:    ctx,
		publishSem: publishSem,
	}

	for i := 0; i < ptzCommandQueueSize; i++ {
		_, accepted := client.enqueuePTZCommand("pan", ptzCommand{
			result: mqttCommandResult{Command: "rotary_action", Direction: "left"},
		})
		if !accepted {
			t.Fatalf("movement command %d was unexpectedly rejected", i)
		}
	}

	busy, accepted := client.enqueuePTZCommand("pan", ptzCommand{
		result: mqttCommandResult{Command: "rotary_action", Direction: "stop"},
	})
	if !busy || !accepted {
		t.Fatalf("STOP result = (busy %v, accepted %v), want (true, true)", busy, accepted)
	}

	queue := client.ptzQueues["pan"]
	if got := len(queue.jobs); got != 1 {
		t.Fatalf("queued commands = %d, want only the prioritized STOP", got)
	}
	if got := queue.pending; got != 1 {
		t.Fatalf("pending commands = %d, want 1", got)
	}
	if got := (<-queue.jobs).result.Direction; got != "stop" {
		t.Fatalf("remaining command = %q, want stop", got)
	}
}
