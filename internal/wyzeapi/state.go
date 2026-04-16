package wyzeapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
)

// StateFile holds persisted auth and camera state.
type StateFile struct {
	Auth    *AuthState            `json:"auth,omitempty"`
	Cameras map[string]CameraInfo `json:"cameras"` // keyed by MAC
	Updated time.Time             `json:"updated"`
}

// LoadState reads the state file from disk.
func LoadState(stateDir string, log zerolog.Logger) (*StateFile, error) {
	path := filepath.Join(stateDir, "wyze-bridge.state.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		log.Debug().Msg("no state file found, starting fresh")
		return &StateFile{Cameras: make(map[string]CameraInfo)}, nil
	}
	if err != nil {
		return nil, err
	}

	var sf StateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		log.Warn().Err(err).Msg("corrupt state file, starting fresh")
		return &StateFile{Cameras: make(map[string]CameraInfo)}, nil
	}

	if sf.Cameras == nil {
		sf.Cameras = make(map[string]CameraInfo)
	}

	log.Info().
		Time("updated", sf.Updated).
		Int("cameras", len(sf.Cameras)).
		Bool("has_auth", sf.Auth != nil).
		Msg("state file loaded")

	return &sf, nil
}

// Save writes the state file to disk.
func (sf *StateFile) Save(stateDir string) error {
	sf.Updated = time.Now()
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, "wyze-bridge.state.json")
	return os.WriteFile(path, data, 0600)
}

// UpdateCameras replaces the camera list in the state file.
func (sf *StateFile) UpdateCameras(cameras []CameraInfo) {
	sf.Cameras = make(map[string]CameraInfo, len(cameras))
	for _, cam := range cameras {
		sf.Cameras[cam.MAC] = cam
	}
}
