package camera

import (
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func makeCams() []wyzeapi.CameraInfo {
	return []wyzeapi.CameraInfo{
		{Nickname: "Front Door", Model: "HL_CAM4", MAC: "AABB11"},
		{Nickname: "Backyard", Model: "WYZE_CAKP2JFUS", MAC: "AABB22"},
		{Nickname: "Garage", Model: "HL_PAN3", MAC: "AABB33"},
	}
}

func TestFilter_NoFilter(t *testing.T) {
	f := &Filter{}
	cams := makeCams()
	result := f.Apply(cams)
	if len(result) != 3 {
		t.Errorf("no filter should pass all, got %d", len(result))
	}
}

func TestFilter_AllowByName(t *testing.T) {
	f := &Filter{Names: []string{"FRONT DOOR", "GARAGE"}}
	result := f.Apply(makeCams())
	if len(result) != 2 {
		t.Errorf("expected 2 cameras, got %d", len(result))
	}
}

func TestFilter_AllowByModel(t *testing.T) {
	f := &Filter{Models: []string{"HL_CAM4"}}
	result := f.Apply(makeCams())
	if len(result) != 1 {
		t.Errorf("expected 1 camera, got %d", len(result))
	}
	if result[0].Nickname != "Front Door" {
		t.Errorf("expected Front Door, got %s", result[0].Nickname)
	}
}

func TestFilter_AllowByMAC(t *testing.T) {
	f := &Filter{MACs: []string{"AABB22"}}
	result := f.Apply(makeCams())
	if len(result) != 1 {
		t.Errorf("expected 1 camera, got %d", len(result))
	}
}

func TestFilter_BlocksMode(t *testing.T) {
	f := &Filter{Names: []string{"BACKYARD"}, Block: true}
	result := f.Apply(makeCams())
	if len(result) != 2 {
		t.Errorf("expected 2 cameras (blocking Backyard), got %d", len(result))
	}
	for _, c := range result {
		if c.Nickname == "Backyard" {
			t.Error("Backyard should be blocked")
		}
	}
}

func TestFilter_AllowByModelName(t *testing.T) {
	f := &Filter{Models: []string{"V4"}}
	result := f.Apply(makeCams())
	if len(result) != 1 {
		t.Errorf("expected 1 camera by model name V4, got %d", len(result))
	}
}

func TestFilter_NeverFilterToEmpty(t *testing.T) {
	f := &Filter{Names: []string{"NONEXISTENT"}}
	cams := makeCams()
	result := f.Apply(cams)
	if len(result) != len(cams) {
		t.Errorf("should not filter to empty: got %d, want %d", len(result), len(cams))
	}
}
