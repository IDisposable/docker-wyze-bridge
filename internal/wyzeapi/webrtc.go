package wyzeapi

import "fmt"

// KVSStreamConfig is the typed result of GetCameraKVSConfig — the
// Wyze KVS WebRTC signaling URL + ICE servers + auth token for one
// camera, parsed out of the raw /v4/camera/get_streams response.
type KVSStreamConfig struct {
	SignalingURL string
	IceServers   []KVSIceServer
	AuthToken    string
}

// KVSIceServer is a single STUN/TURN entry in the KVS config.
type KVSIceServer struct {
	URL        string
	Username   string
	Credential string
}

// GetCameraKVSConfig calls GetCameraStream and parses the response
// into a typed KVSStreamConfig. Callers (the webui KVS shim) get a
// stable shape and don't have to navigate the nested-map structure.
func (c *Client) GetCameraKVSConfig(mac, model string) (*KVSStreamConfig, error) {
	resp, err := c.GetCameraStream(CameraInfo{MAC: mac, Model: model})
	if err != nil {
		return nil, err
	}
	dataList, ok := resp["data"].([]interface{})
	if !ok || len(dataList) == 0 {
		return nil, fmt.Errorf("get_streams: missing data array in response")
	}
	first, ok := dataList[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("get_streams: data[0] is not an object")
	}
	params, ok := first["params"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("get_streams: data[0].params missing")
	}
	signalingURL, _ := params["signaling_url"].(string)
	if signalingURL == "" {
		return nil, fmt.Errorf("get_streams: empty signaling_url")
	}
	authToken, _ := params["auth_token"].(string)

	var ice []KVSIceServer
	if rawList, ok := params["ice_servers"].([]interface{}); ok {
		for _, raw := range rawList {
			m, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			s := KVSIceServer{}
			s.URL, _ = m["url"].(string)
			s.Username, _ = m["username"].(string)
			s.Credential, _ = m["credential"].(string)
			if s.URL != "" {
				ice = append(ice, s)
			}
		}
	}
	return &KVSStreamConfig{
		SignalingURL: signalingURL,
		IceServers:   ice,
		AuthToken:    authToken,
	}, nil
}
