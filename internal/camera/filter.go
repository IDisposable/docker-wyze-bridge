package camera

import (
	"strings"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// Filter defines camera filtering rules.
type Filter struct {
	Names  []string // FILTER_NAMES (uppercased)
	Models []string // FILTER_MODELS (uppercased)
	MACs   []string // FILTER_MACS (uppercased)
	Block  bool     // FILTER_BLOCKS: if true, listed cameras are excluded
}

// HasFilters returns true if any filter criteria are set.
func (f *Filter) HasFilters() bool {
	return len(f.Names) > 0 || len(f.Models) > 0 || len(f.MACs) > 0
}

// Apply filters a camera list according to the filter rules.
func (f *Filter) Apply(cameras []wyzeapi.CameraInfo) []wyzeapi.CameraInfo {
	if !f.HasFilters() {
		return cameras
	}

	var result []wyzeapi.CameraInfo
	for _, cam := range cameras {
		matched := f.matches(cam)
		if f.Block {
			// Block mode: exclude matched cameras
			if !matched {
				result = append(result, cam)
			}
		} else {
			// Allow mode: include only matched cameras
			if matched {
				result = append(result, cam)
			}
		}
	}

	if len(result) == 0 {
		return cameras // never filter to empty
	}
	return result
}

func (f *Filter) matches(cam wyzeapi.CameraInfo) bool {
	if contains(f.Names, strings.ToUpper(strings.TrimSpace(cam.Nickname))) {
		return true
	}
	if contains(f.MACs, strings.ToUpper(cam.MAC)) {
		return true
	}
	if contains(f.Models, strings.ToUpper(cam.Model)) {
		return true
	}
	// Also check human-readable model name
	if contains(f.Models, strings.ToUpper(cam.ModelName())) {
		return true
	}
	return false
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
