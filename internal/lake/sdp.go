// Package lake translates Agora gateway signaling for go2rtc's existing WebRTC
// receiver. go2rtc handles media transport and RTSP publication; FFmpeg handles
// RTP ordering and preview encoding.
package lake

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type object = map[string]any

func obj(v any) object {
	if o, ok := v.(map[string]any); ok {
		return o
	}
	return object{}
}
func array(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func number64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

type media struct {
	kind, mid, direction string
	codecs               []object
	extensions           []object
	attrs                map[string]string
}
type offer struct {
	medias    []media
	ice, dtls object
	bundle    string
}

func parseOffer(sdp string) (*offer, error) {
	if len(sdp) > 65536 {
		return nil, fmt.Errorf("SDP exceeds limit")
	}
	o := &offer{ice: object{}, dtls: object{"role": "client"}}
	current := -1
	codecByPT := map[int]object{}
	for _, line := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "m=") {
			f := strings.Fields(line[2:])
			if len(f) < 4 {
				return nil, fmt.Errorf("invalid media description")
			}
			o.medias = append(o.medias, media{kind: f[0], direction: "sendrecv", attrs: map[string]string{}})
			current++
			codecByPT = map[int]object{}
			continue
		}
		if !strings.HasPrefix(line, "a=") {
			continue
		}
		key, val, _ := strings.Cut(line[2:], ":")
		if key == "group" && strings.HasPrefix(val, "BUNDLE ") {
			o.bundle = strings.TrimPrefix(val, "BUNDLE ")
		}
		// Pion/go2rtc places its fingerprint at session level; browsers may
		// repeat it inside media sections. Accept both SDP forms.
		switch key {
		case "fingerprint":
			if o.dtls["fingerprints"] == nil {
				f := strings.Fields(val)
				if len(f) == 2 {
					o.dtls["fingerprints"] = []any{object{"hashFunction": f[0], "fingerprint": f[1]}}
				}
			}
		case "ice-ufrag":
			if str(o.ice["iceUfrag"]) == "" {
				o.ice["iceUfrag"] = val
			}
		case "ice-pwd":
			if str(o.ice["icePwd"]) == "" {
				o.ice["icePwd"] = val
			}
		}
		if current < 0 {
			continue
		}
		m := &o.medias[current]
		m.attrs[key] = val
		switch key {
		case "mid":
			m.mid = val
		case "recvonly", "sendonly", "sendrecv", "inactive":
			m.direction = key
		case "ice-ufrag":
			if len(o.ice) == 0 {
				o.ice["iceUfrag"] = val
			}
		case "ice-pwd":
			if str(o.ice["icePwd"]) == "" {
				o.ice["icePwd"] = val
			}
		case "fingerprint":
			if o.dtls["fingerprints"] == nil {
				f := strings.Fields(val)
				if len(f) == 2 {
					o.dtls["fingerprints"] = []any{object{"hashFunction": f[0], "fingerprint": f[1]}}
				}
			}
		case "rtpmap":
			f := strings.Fields(val)
			if len(f) != 2 {
				return nil, fmt.Errorf("invalid RTP map")
			}
			pt, e := strconv.Atoi(f[0])
			if e != nil {
				return nil, fmt.Errorf("invalid payload")
			}
			r := strings.Split(f[1], "/")
			if len(r) < 2 {
				return nil, fmt.Errorf("invalid codec")
			}
			rate, e := strconv.Atoi(r[1])
			if e != nil {
				return nil, fmt.Errorf("invalid clock rate")
			}
			rtp := object{"encodingName": r[0], "clockRate": rate, "encodingParameters": nil}
			if len(r) > 2 {
				rtp["encodingParameters"] = r[2]
			}
			c := object{"payloadType": pt, "rtpMap": rtp, "rtcpFeedbacks": []any{}, "fmtp": object{"parameters": object{}}}
			m.codecs = append(m.codecs, c)
			codecByPT[pt] = c
		case "fmtp":
			pt, params, ok := strings.Cut(val, " ")
			if !ok {
				continue
			}
			i, _ := strconv.Atoi(pt)
			if c := codecByPT[i]; c != nil {
				p := obj(obj(c["fmtp"])["parameters"])
				for _, entry := range strings.Split(params, ";") {
					k, v, ok := strings.Cut(strings.TrimSpace(entry), "=")
					if ok {
						p[k] = v
					}
				}
			}
		case "rtcp-fb":
			f := strings.Fields(val)
			if len(f) < 2 {
				continue
			}
			i, _ := strconv.Atoi(f[0])
			if c := codecByPT[i]; c != nil {
				p := ""
				if len(f) > 2 {
					p = f[2]
				}
				c["rtcpFeedbacks"] = append(array(c["rtcpFeedbacks"]), object{"type": f[1], "parameter": p})
			}
		case "extmap":
			f := strings.Fields(val)
			if len(f) < 2 {
				continue
			}
			id, _, _ := strings.Cut(f[0], "/")
			i, e := strconv.Atoi(id)
			if e == nil {
				m.extensions = append(m.extensions, object{"entry": i, "extensionName": f[1]})
			}
		}
	}
	if len(o.medias) == 0 || str(o.ice["iceUfrag"]) == "" || str(o.ice["icePwd"]) == "" || o.dtls["fingerprints"] == nil {
		return nil, fmt.Errorf("incomplete SDP transport")
	}
	// Limit the gateway to HEVC. This subset remains valid against go2rtc's
	// original offer and prevents a channel-wide fallback to H264.
	hevc := false
	for i := range o.medias {
		m := &o.medias[i]
		if m.mid == "" {
			m.mid = strconv.Itoa(i)
		}
		if m.kind == "video" {
			var cs []object
			for _, c := range m.codecs {
				if strings.EqualFold(str(obj(c["rtpMap"])["encodingName"]), "H265") {
					cs = append(cs, c)
					hevc = true
				}
			}
			m.codecs = cs
		}
	}
	if !hevc {
		return nil, fmt.Errorf("HEVC receiver required")
	}
	return o, nil
}
func (o *offer) ortc() object {
	caps := func() object {
		return object{"audioCodecs": []any{}, "videoCodecs": []any{}, "audioExtensions": []any{}, "videoExtensions": []any{}}
	}
	send, recv := caps(), caps()
	for _, m := range o.medias {
		if m.kind != "audio" && m.kind != "video" {
			continue
		}
		targets := []object{send, recv}
		if m.direction == "sendonly" {
			targets = []object{send}
		} else if m.direction == "recvonly" {
			targets = []object{recv}
		}
		for _, t := range targets {
			for _, c := range m.codecs {
				t[m.kind+"Codecs"] = append(array(t[m.kind+"Codecs"]), c)
			}
			for _, e := range m.extensions {
				t[m.kind+"Extensions"] = append(array(t[m.kind+"Extensions"]), e)
			}
		}
	}
	return object{"iceParameters": o.ice, "dtlsParameters": o.dtls, "rtpCapabilities": object{"send": send, "recv": recv}, "version": "2"}
}
func (o *offer) answer(ortc, stream object, fallbackFingerprint string) (string, error) {
	ice := obj(ortc["iceParameters"])
	dtls := obj(ortc["dtlsParameters"])
	fp := fallbackFingerprint
	fps := array(dtls["fingerprints"])
	if len(fps) > 0 {
		f := obj(fps[0])
		algorithm := str(f["hashFunction"])
		if algorithm == "" {
			algorithm = str(f["algorithm"])
		}
		if algorithm == "" {
			algorithm = "sha-256"
		}
		if value := str(f["fingerprint"]); value != "" {
			fp = algorithm + " " + value
		}
	}
	if fp == "" || str(ice["iceUfrag"]) == "" || str(ice["icePwd"]) == "" {
		return "", fmt.Errorf("incomplete gateway transport")
	}
	caps := obj(ortc["rtpCapabilities"])
	for _, k := range []string{"sendrecv", "recv", "send"} {
		if c := obj(caps[k]); len(c) > 0 {
			caps = c
			break
		}
	}
	lines := []string{"v=0", "o=- 0 0 IN IP4 127.0.0.1", "s=LakeGateway", "t=0 0", "a=group:BUNDLE " + o.bundle, "a=ice-lite", "a=msid-semantic: WMS"}
	for _, m := range o.medias {
		if m.kind != "video" && m.kind != "audio" {
			return "", fmt.Errorf("unsupported SDP media")
		}
		codecs := array(caps[m.kind+"Codecs"])
		pts := []string{}
		for _, c := range codecs {
			pts = append(pts, strconv.Itoa(num(obj(c)["payloadType"])))
		}
		if len(pts) == 0 {
			return "", fmt.Errorf("no negotiated codecs")
		}
		lines = append(lines, "m="+m.kind+" 9 UDP/TLS/RTP/SAVPF "+strings.Join(pts, " "), "c=IN IP4 127.0.0.1", "a=rtcp:9 IN IP4 0.0.0.0", "a=ice-ufrag:"+str(ice["iceUfrag"]), "a=ice-pwd:"+str(ice["icePwd"]), "a=ice-options:trickle", "a=fingerprint:"+fp, "a=setup:active", "a=mid:"+m.mid)
		for index, v := range array(ice["candidates"]) {
			c := obj(v)
			protocol := str(c["protocol"])
			if protocol == "" {
				protocol = "udp"
			}
			kind := str(c["type"])
			if kind == "" {
				kind = "host"
			}
			foundation := str(c["foundation"])
			if foundation == "" {
				foundation = fmt.Sprintf("gateway%d", index)
			}
			priority := num(c["priority"])
			if priority == 0 {
				priority = 2103266323
			}
			lines = append(lines, fmt.Sprintf("a=candidate:%s 1 %s %d %s %d typ %s", foundation, protocol, priority, str(c["ip"]), num(c["port"]), kind))
		}
		for _, v := range array(caps[m.kind+"Extensions"]) {
			remote := obj(v)
			for _, local := range m.extensions {
				if str(local["extensionName"]) == str(remote["extensionName"]) {
					lines = append(lines, fmt.Sprintf("a=extmap:%d %s", num(local["entry"]), str(local["extensionName"])))
				}
			}
		}
		direction := "inactive"
		if m.kind == "video" {
			direction = "sendonly"
		}
		lines = append(lines, "a="+direction, "a=rtcp-mux", "a=rtcp-rsize")
		for _, v := range codecs {
			c := obj(v)
			pt := num(c["payloadType"])
			rtp := obj(c["rtpMap"])
			r := fmt.Sprintf("a=rtpmap:%d %s/%d", pt, str(rtp["encodingName"]), num(rtp["clockRate"]))
			if e := rtp["encodingParameters"]; e != nil && fmt.Sprint(e) != "" {
				r += "/" + fmt.Sprint(e)
			}
			lines = append(lines, r)
			for _, v := range array(c["rtcpFeedbacks"]) {
				f := obj(v)
				line := fmt.Sprintf("a=rtcp-fb:%d %s", pt, str(f["type"]))
				if p := str(f["parameter"]); p != "" {
					line += " " + p
				}
				lines = append(lines, line)
			}
			params := obj(obj(c["fmtp"])["parameters"])
			keys := []string{}
			for k := range params {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fields := []string{}
			for _, k := range keys {
				fields = append(fields, k+"="+fmt.Sprint(params[k]))
			}
			if len(fields) > 0 {
				lines = append(lines, fmt.Sprintf("a=fmtp:%d %s", pt, strings.Join(fields, ";")))
			}
		}
		if m.kind == "video" {
			ssrc := number64(stream["ssrcId"])
			if ssrc == 0 {
				return "", fmt.Errorf("video SSRC missing")
			}
			name := str(stream["cname"])
			if name == "" {
				name = "lake"
			}
			lines = append(lines, "a=msid:lake lake-video", fmt.Sprintf("a=ssrc:%d cname:%s", ssrc, name), fmt.Sprintf("a=ssrc:%d msid:lake lake-video", ssrc))
		}
	}
	return strings.Join(lines, "\r\n") + "\r\n", nil
}
