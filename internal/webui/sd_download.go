package webui

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/sddownload"
)

// handleSDRecording retrieves one existing recording without changing live
// streams or refreshing cloud metadata. The service owns private helper files.
func (s *Server) handleSDRecording(w http.ResponseWriter, r *http.Request, cam *camera.Camera) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeJSONError(w, http.StatusUnsupportedMediaType, "json_required")
		return
	}
	if r.ContentLength > 1024 {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_recording_request")
		return
	}
	if len(body) > 1024 {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	request, err := sddownload.ParseRequest(bytes.NewReader(body))
	if err != nil {
		writeSDDownloadError(w, err)
		return
	}
	if s.sdDownload == nil {
		writeSDDownloadError(w, sddownload.ErrDisabled)
		return
	}
	// The helper's 65-second budget exceeds the server's usual 60-second
	// write timeout. Extend this response only, including delivery time.
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(90 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeSDDownloadError(w, sddownload.ErrFailed)
		return
	}
	info := cam.GetInfo()
	file, err := s.sdDownload.Download(r.Context(), sddownload.Pairing{
		IP: info.LanIP, UID: info.P2PID, ENR: info.ENR, MAC: info.MAC,
		Model: info.Model, DTLS: info.DTLS,
	}, request)
	if err != nil {
		writeSDDownloadError(w, err)
		return
	}
	// Retain both scratch files and the single-operation lock until copying
	// finishes or the client disconnects. Close also cleans up failed copies.
	defer file.Close()
	if file.Filename == "" || filepath.Base(file.Filename) != file.Filename || strings.ContainsAny(file.Filename, "\r\n") {
		writeSDDownloadError(w, sddownload.ErrInvalidResult)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Filename}))
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Once delivery begins, a write failure must not append a JSON error to a
	// partial recording. The bounded helper result was already verified.
	_, _ = io.CopyN(w, file, file.Size)
}

func writeSDDownloadError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadGateway, "sd_backend_failed"
	switch {
	case errors.Is(err, sddownload.ErrRequest):
		status, code = http.StatusBadRequest, "invalid_recording_request"
	case errors.Is(err, sddownload.ErrDisabled):
		status, code = http.StatusNotImplemented, "sd_backend_disabled"
	case errors.Is(err, sddownload.ErrUnavailable):
		status, code = http.StatusServiceUnavailable, "sd_backend_unavailable"
	case errors.Is(err, sddownload.ErrBusy):
		status, code = http.StatusConflict, "sd_backend_busy"
	case errors.Is(err, sddownload.ErrPairing):
		status, code = http.StatusUnprocessableEntity, "camera_pairing_unavailable"
	case errors.Is(err, sddownload.ErrTimeout):
		status, code = http.StatusGatewayTimeout, "sd_backend_timeout"
	case errors.Is(err, sddownload.ErrNotFound):
		status, code = http.StatusNotFound, "recording_not_found"
	case errors.Is(err, sddownload.ErrInvalidResult):
		code = "sd_backend_invalid_result"
	case errors.Is(err, sddownload.ErrIncomplete):
		code = "sd_backend_incomplete"
	}
	writeJSONError(w, status, code)
}
