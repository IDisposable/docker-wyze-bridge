// Package sddownload adapts the optional go2rtc one-file SD helper. It does
// not implement a camera protocol, perform cloud discovery, or retain clips.
package sddownload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MaxBytes int64 = 16 * 1024 * 1024

var (
	ErrRequest       = errors.New("invalid_recording_request")
	ErrDisabled      = errors.New("sd_backend_disabled")
	ErrUnavailable   = errors.New("sd_backend_unavailable")
	ErrBusy          = errors.New("sd_backend_busy")
	ErrPairing       = errors.New("camera_pairing_unavailable")
	ErrTimeout       = errors.New("sd_backend_timeout")
	ErrInvalidResult = errors.New("sd_backend_invalid_result")
	ErrNotFound      = errors.New("recording_not_found")
	ErrIncomplete    = errors.New("sd_backend_incomplete")
	ErrFailed        = errors.New("sd_backend_failed")
)

var (
	datePattern   = regexp.MustCompile(`^[0-9]{8}$`)
	numberPattern = regexp.MustCompile(`^[0-9]{1,2}$`)
	uidPattern    = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)
	macPattern    = regexp.MustCompile(`^(?:[0-9A-Fa-f]{12}|(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}|(?:[0-9A-Fa-f]{2}-){5}[0-9A-Fa-f]{2})$`)
	modelPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Request identifies one minute in the camera's local clock, not the PC clock.
type Request struct {
	Date         string
	Hour, Minute int
	New          bool
}

func (r Request) paths() (string, string, error) {
	if !datePattern.MatchString(r.Date) || r.Hour < 0 || r.Hour > 23 || r.Minute < 0 || r.Minute > 59 {
		return "", "", ErrRequest
	}
	if _, err := time.Parse("20060102", r.Date); err != nil {
		return "", "", ErrRequest
	}
	suffix := ""
	if r.New {
		suffix = "_new"
	}
	hour, minute := twoDigits(r.Hour), twoDigits(r.Minute)
	return "/media/mmc/record/" + r.Date + "/" + hour + "/" + minute + suffix + ".mp4",
		r.Date + "-" + hour + minute + suffix + ".mp4", nil
}

func twoDigits(n int) string { return string([]byte{byte('0' + n/10), byte('0' + n%10)}) }

// object rejects duplicate keys and extra JSON values, including helper output.
func object(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, ErrRequest
	}
	result := make(map[string]json.RawMessage)
	for d.More() {
		t, err = d.Token()
		key, ok := t.(string)
		if err != nil || !ok {
			return nil, ErrRequest
		}
		if _, exists := result[key]; exists {
			return nil, ErrRequest
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrRequest
		}
		result[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, ErrRequest
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrRequest
	}
	return result, nil
}

// ParseRequest accepts the same bounded JSON request as the original DWB adapter.
func ParseRequest(reader io.Reader) (Request, error) {
	var r Request
	data, err := io.ReadAll(io.LimitReader(reader, 1025))
	if err != nil || len(data) > 1024 {
		return r, ErrRequest
	}
	obj, err := object(data)
	if err != nil {
		return r, ErrRequest
	}
	for key := range obj {
		if key != "date" && key != "hour" && key != "minute" && key != "new" {
			return r, ErrRequest
		}
	}
	if json.Unmarshal(obj["date"], &r.Date) != nil {
		return r, ErrRequest
	}
	for _, field := range []struct {
		key string
		out *int
	}{{"hour", &r.Hour}, {"minute", &r.Minute}} {
		raw := obj[field.key]
		value := string(raw)
		if len(raw) > 0 && raw[0] == '"' {
			if json.Unmarshal(raw, &value) != nil {
				return r, ErrRequest
			}
		}
		if !numberPattern.MatchString(value) {
			return r, ErrRequest
		}
		*field.out, _ = strconv.Atoi(value)
	}
	if raw, ok := obj["new"]; ok {
		if string(raw) != "true" && string(raw) != "false" {
			return r, ErrRequest
		}
		r.New = string(raw) == "true"
	}
	_, _, err = r.paths()
	return r, err
}

// Pairing contains cached local camera credentials, never Wyze account tokens.
type Pairing struct {
	IP    string `json:"ip"`
	UID   string `json:"uid"`
	ENR   string `json:"enr"`
	MAC   string `json:"mac"`
	Model string `json:"model"`
	DTLS  bool   `json:"-"`
}

func (p Pairing) normalized() (Pairing, error) {
	ip, err := netip.ParseAddr(p.IP)
	if err != nil || !ip.Is4() || !ip.IsPrivate() || !p.DTLS ||
		!uidPattern.MatchString(p.UID) || len(p.ENR) < 16 || len(p.ENR) > 2048 ||
		!macPattern.MatchString(p.MAC) || !modelPattern.MatchString(p.Model) {
		return p, ErrPairing
	}
	p.IP = ip.String()
	p.MAC = strings.ToUpper(strings.NewReplacer(":", "", "-", "").Replace(p.MAC))
	return p, nil
}

// Service permits one export at a time, including HTTP delivery. Helper is an
// operator-provided absolute executable path; an empty path disables exports.
type Service struct {
	helper  string
	busy    atomic.Bool
	timeout time.Duration
	run     func(context.Context, string, ...string) error
}

func New(helper string) *Service {
	return &Service{helper: helper, timeout: 65 * time.Second, run: runHelper}
}

func runHelper(ctx context.Context, helper string, args ...string) error {
	cmd := exec.CommandContext(ctx, helper, args...)
	// The helper needs only its private pairing file. Do not inherit account
	// secrets, and do not forward subprocess diagnostics into HTTP/log output.
	cmd.Env = []string{}
	return cmd.Run()
}

// File owns the private scratch directory and service lock until Close.
type File struct {
	*os.File
	Size     int64
	Filename string
	once     sync.Once
	cleanup  func()
	closeErr error
}

func (f *File) Close() error {
	f.once.Do(func() { f.closeErr = f.File.Close(); f.cleanup() })
	return f.closeErr
}

func (s *Service) Download(ctx context.Context, p Pairing, request Request) (*File, error) {
	if s.helper == "" {
		return nil, ErrDisabled
	}
	info, err := os.Stat(s.helper)
	if runtime.GOOS != "linux" || !filepath.IsAbs(s.helper) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, ErrUnavailable
	}
	remote, filename, err := request.paths()
	if err != nil {
		return nil, err
	}
	p, err = p.normalized()
	if err != nil {
		return nil, err
	}
	if !s.busy.CompareAndSwap(false, true) {
		return nil, ErrBusy
	}
	dir := ""
	cleanup := func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
		s.busy.Store(false)
	}
	retained := false
	defer func() {
		if !retained {
			cleanup()
		}
	}()
	dir, err = os.MkdirTemp("", "wyze-sd-")
	if err != nil {
		return nil, ErrFailed
	}
	credentials := filepath.Join(dir, "camera.json")
	data, _ := json.Marshal(p)
	if err = os.WriteFile(credentials, data, 0600); err != nil {
		return nil, ErrFailed
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrFailed
	}
	defer root.Close()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	childErr := s.run(ctx, s.helper, "--live", "--credentials", credentials,
		"--download", remote, "--media-output", filepath.Join(dir, "clip.mp4"),
		"--output", filepath.Join(dir, "result.json"), "--max-bytes", strconv.FormatInt(MaxBytes, 10))
	if ctx.Err() != nil {
		return nil, ErrTimeout
	}
	var exitError *exec.ExitError
	if childErr != nil && !errors.As(childErr, &exitError) {
		return nil, ErrUnavailable
	}
	report, err := readResult(root)
	if err != nil {
		return nil, err
	}
	var result struct {
		Operation string `json:"operation"`
		Phase     string `json:"phase"`
		Path      string `json:"recording_path"`
		Bytes     int64  `json:"downloaded_bytes"`
		Error     string `json:"error"`
	}
	if json.Unmarshal(report, &result) != nil || result.Operation != "download_sd_file" || result.Path != remote {
		return nil, ErrInvalidResult
	}
	if result.Error == "file does not exist" {
		return nil, ErrNotFound
	}
	if childErr != nil || result.Phase != "download_complete" || result.Error != "" || result.Bytes <= 0 || result.Bytes > MaxBytes {
		return nil, ErrIncomplete
	}
	media, err := openPrivate(root, "clip.mp4")
	if err != nil {
		return nil, ErrIncomplete
	}
	info, err = media.Stat()
	if err != nil || info.Size() != result.Bytes {
		_ = media.Close()
		return nil, ErrIncomplete
	}
	retained = true
	return &File{File: media, Size: result.Bytes, Filename: filename, cleanup: cleanup}, nil
}

func readResult(root *os.Root) ([]byte, error) {
	f, err := openPrivate(root, "result.json")
	if err != nil {
		return nil, ErrInvalidResult
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return nil, ErrInvalidResult
	}
	if _, err := object(data); err != nil {
		return nil, ErrInvalidResult
	}
	return data, nil
}
