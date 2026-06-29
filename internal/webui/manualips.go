package webui

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// ManualIPs maps a camera MAC (uppercase, no separators) to a LAN IP
// the operator supplies by hand. It exists because the Wyze cloud API
// returns an empty LAN IP for Gwell cameras — and for LAN-direct models
// like the Window Cam (GW_WC) that the cloud also doesn't reliably
// surface, P2P discovery alone may never find the camera on the local
// subnet. Supplying the IP here lets the shim hand it to gwell-proxy so
// the session can go LAN-direct instead of relaying through the cloud.
//
// This mirrors the manual_ips.json used by wlatic/hacky-wyze-gwell, so
// an existing file drops in unchanged.
type ManualIPs map[string]string

// normalizeMAC strips a model prefix (e.g. "GW_WC_80482CB9EF6E" ->
// "80482CB9EF6E"), removes ':'/'-' separators, and uppercases. Accepts
// plain MACs too. Returns "" for an unusable key.
func normalizeMAC(key string) string {
	// A wlatic-style key is "<MODEL>_<MAC>" where MAC is the trailing
	// underscore-delimited segment. Plain MAC keys have no underscore.
	if i := strings.LastIndex(key, "_"); i >= 0 {
		key = key[i+1:]
	}
	key = strings.NewReplacer(":", "", "-", "").Replace(key)
	return strings.ToUpper(strings.TrimSpace(key))
}

// LoadManualIPs reads a JSON object of {key: ip} from path and returns a
// MAC-normalized map. A missing file is not an error — it returns an
// empty map so the feature is opt-in by simply dropping the file in.
// Keys may be plain MACs or wlatic "<MODEL>_<MAC>" form; values are LAN
// IPs (or arbitrary strings, validated downstream).
func LoadManualIPs(path string) (ManualIPs, error) {
	if path == "" {
		return ManualIPs{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ManualIPs{}, nil
		}
		return nil, err
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make(ManualIPs, len(raw))
	for k, v := range raw {
		mac := normalizeMAC(k)
		ip := strings.TrimSpace(v)
		if mac == "" || ip == "" {
			continue
		}
		out[mac] = ip
	}
	return out, nil
}

// lookup returns the manual IP for a MAC (any case/separator form), or
// "" if none is configured.
func (m ManualIPs) lookup(mac string) string {
	if m == nil {
		return ""
	}
	return m[normalizeMAC(mac)]
}
