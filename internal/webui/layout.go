package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// The dashboard's tile order is server-side state rather than a
// browser preference so the phone and the desktop show the same grid.
// main.go persists it through OnLayoutChange → StateFile.Layout and
// hands it back with SetLayout at startup.

// maxLayoutEntries bounds a PUT /api/layout body; nobody has this many
// cameras and it keeps a bad client from filling the state file.
const maxLayoutEntries = 256

type layoutStore struct {
	mu       sync.RWMutex
	layout   wyzeapi.Layout
	onChange func(wyzeapi.Layout)
}

// SetLayout installs the persisted layout (startup).
func (s *Server) SetLayout(l wyzeapi.Layout) {
	s.layouts.mu.Lock()
	s.layouts.layout = copyLayout(l)
	s.layouts.mu.Unlock()
}

// Layout returns a copy of the current layout.
func (s *Server) Layout() wyzeapi.Layout {
	s.layouts.mu.RLock()
	defer s.layouts.mu.RUnlock()
	return copyLayout(s.layouts.layout)
}

// OnLayoutChange registers the persistence hook; fires after every
// accepted PUT /api/layout with the new layout.
func (s *Server) OnLayoutChange(fn func(wyzeapi.Layout)) {
	s.layouts.mu.Lock()
	s.layouts.onChange = fn
	s.layouts.mu.Unlock()
}

func (s *Server) storeLayout(l wyzeapi.Layout) {
	s.layouts.mu.Lock()
	s.layouts.layout = copyLayout(l)
	fn := s.layouts.onChange
	s.layouts.mu.Unlock()
	if fn != nil {
		fn(copyLayout(l))
	}
}

// copyLayout detaches a layout from its caller's slice. The Order is
// never nil so an empty layout encodes as [] rather than null.
func copyLayout(l wyzeapi.Layout) wyzeapi.Layout {
	return wyzeapi.Layout{Order: append([]string{}, l.Order...), Locked: l.Locked}
}

// orderCameras applies a saved order: listed cameras first, in that
// order; cameras the layout doesn't know (new ones, restored ones)
// follow sorted by name, which is how the grid was ordered before
// layouts existed. Names in the order that no longer exist are
// skipped, not an error, so an ignored camera's slot simply closes.
func orderCameras(cams []*camera.Camera, order []string) []*camera.Camera {
	byName := make(map[string]*camera.Camera, len(cams))
	for _, c := range cams {
		byName[c.Name()] = c
	}
	out := make([]*camera.Camera, 0, len(cams))
	placed := make(map[string]bool, len(cams))
	for _, name := range order {
		if c, ok := byName[name]; ok && !placed[name] {
			out = append(out, c)
			placed[name] = true
		}
	}
	var rest []*camera.Camera
	for _, c := range cams {
		if !placed[c.Name()] {
			rest = append(rest, c)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name() < rest[j].Name() })
	return append(out, rest...)
}

// handleAPILayout serves GET /api/layout and PUT (or POST) /api/layout.
// A PUT replaces the whole layout; the dashboard sends the complete
// order after every drag and on each lock toggle. Entries are deduped
// and must be plain strings; unknown names are kept so a camera that's
// temporarily offline keeps its slot.
func (s *Server) handleAPILayout(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.Layout())
	case http.MethodPut, http.MethodPost:
		var body wyzeapi.Layout
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if len(body.Order) > maxLayoutEntries {
			writeJSONError(w, http.StatusBadRequest, "too many entries")
			return
		}
		seen := make(map[string]bool, len(body.Order))
		order := make([]string, 0, len(body.Order))
		for _, name := range body.Order {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			order = append(order, name)
		}
		body.Order = order
		s.storeLayout(body)
		writeJSON(w, s.Layout())
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "GET or PUT required")
	}
}
