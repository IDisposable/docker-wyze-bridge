package wyzeapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// StateFile holds persisted auth and camera state. Save() is safe
// for concurrent callers (writeMu) and writes atomically via
// write-to-temp + rename.
type StateFile struct {
	Auth    *AuthState            `json:"auth,omitempty"`
	Cameras map[string]CameraInfo `json:"cameras"` // keyed by MAC
	// Ignored is the operator's WebUI ignore list. Lives here rather
	// than in config because it's edited at runtime and must survive
	// restarts without a redeploy. See camera.Manager.Ignore.
	Ignored []IgnoredCamera `json:"ignored,omitempty"`
	// Layout is the dashboard's tile order and lock, edited from the
	// WebUI and kept here so every browser sees the same arrangement.
	Layout  *Layout   `json:"layout,omitempty"`
	Updated time.Time `json:"updated"`

	writeMu sync.Mutex
}

// Layout is the operator's dashboard arrangement: camera names in
// display order (cameras not listed follow, by name) and whether the
// tiles are locked against dragging.
type Layout struct {
	Order  []string `json:"order"`
	Locked bool     `json:"locked"`
}

// SetLayout replaces the layout under the same lock Save uses.
func (sf *StateFile) SetLayout(l Layout) {
	sf.writeMu.Lock()
	cp := Layout{Order: append([]string(nil), l.Order...), Locked: l.Locked}
	sf.Layout = &cp
	sf.writeMu.Unlock()
}

// IgnoredCamera is a camera the operator told the bridge to leave
// alone. Keyed by MAC because nicknames change; the nickname and model
// are carried only so the ignored list in the WebUI reads well.
type IgnoredCamera struct {
	MAC      string    `json:"mac"`
	Nickname string    `json:"nickname,omitempty"`
	Model    string    `json:"model,omitempty"`
	Since    time.Time `json:"since"`
}

// SetIgnored replaces the ignore list under the same lock Save uses,
// so a save racing the edit never marshals a half-written slice.
func (sf *StateFile) SetIgnored(list []IgnoredCamera) {
	sf.writeMu.Lock()
	sf.Ignored = append([]IgnoredCamera(nil), list...)
	sf.writeMu.Unlock()
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

// Save writes the state file to disk atomically.
func (sf *StateFile) Save(stateDir string) error {
	sf.writeMu.Lock()
	defer sf.writeMu.Unlock()

	sf.Updated = time.Now()
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, "wyze-bridge.state.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write tmp state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp state file: %w", err)
	}
	return nil
}

// UpdateCameras replaces the camera list in the state file.
func (sf *StateFile) UpdateCameras(cameras []CameraInfo) {
	sf.Cameras = make(map[string]CameraInfo, len(cameras))
	for _, cam := range cameras {
		sf.Cameras[cam.MAC] = cam
	}
}
