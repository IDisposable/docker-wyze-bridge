// Package gwell manages the gwell-proxy sidecar that speaks Wyze's
// Gwell / IoTVideo P2P protocol for cameras that don't use TUTK
// (GW_BE1 Doorbell Pro, GW_GC1 OG, GW_GC2 OG 3X, GW_DBD Doorbell Duo).
//
// The sidecar is a separate pure-Go binary (sourced from the
// reverse-engineering project github.com/wlatic/hacky-wyze-gwell,
// MIT-licensed) that:
//
//  1. Accepts a per-camera registration over a loopback HTTP control
//     API, carrying the Wyze cloud access_token plus the camera's
//     mac/enr/ip.
//  2. Performs the Gwell P2P handshake (UDP+KCP, with TCP/MTP fallback,
//     RC5-32/6 + RC5-64/6 + XOR + HMAC-MD5 cryptography).
//  3. Extracts H.264 from the AVSTREAMCTL frame sequence.
//  4. Publishes the live stream on a loopback RTSP port
//     (rtsp://127.0.0.1:<GWELL_RTSP_PORT>/<cam_name>).
//
// The bridge then registers that rtsp:// URL with go2rtc exactly the
// same way it registers a wyze:// URL for a TUTK camera, so recording,
// snapshots, WebRTC, HLS, and MQTT all work unchanged.
//
// See DOCS/GWELL_INTEGRATION.md for the design rationale.
package gwell
