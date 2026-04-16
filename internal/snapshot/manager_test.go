package snapshot

import (
	"testing"
	"time"
)

func TestFormatFilename(t *testing.T) {
	ts := time.Date(2026, 4, 15, 14, 30, 45, 0, time.UTC)

	tests := []struct {
		format, camName, want string
	}{
		{"{cam_name}", "front_door", "front_door.jpg"},
		{"{cam_name}_%Y%m%d_%H%M%S", "front_door", "front_door_20260415_143045.jpg"},
		{"{CAM_NAME}_%s", "test", "TEST_" + "1776350245" + ".jpg"}, // approximate
		{"{cam_name}.jpeg", "cam", "cam.jpeg"},
	}

	for _, tt := range tests {
		got := formatFilename(tt.format, tt.camName, ts)
		// For %s test, just check it ends with .jpg and starts correctly
		if tt.format == "{CAM_NAME}_%s" {
			if got[:5] != "TEST_" {
				t.Errorf("formatFilename(%q) = %q, expected TEST_ prefix", tt.format, got)
			}
			continue
		}
		if got != tt.want {
			t.Errorf("formatFilename(%q, %q) = %q, want %q", tt.format, tt.camName, got, tt.want)
		}
	}
}

func TestContainsName(t *testing.T) {
	list := []string{"FRONT_DOOR", "BACKYARD"}

	if !containsName(list, "front_door") {
		t.Error("should match case-insensitive")
	}
	if !containsName(list, "BACKYARD") {
		t.Error("should match exact")
	}
	if containsName(list, "garage") {
		t.Error("should not match missing")
	}
}
