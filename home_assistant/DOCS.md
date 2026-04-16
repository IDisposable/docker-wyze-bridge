# Wyze Bridge for Home Assistant

Stream your Wyze cameras locally via WebRTC, RTSP, and HLS — no cloud required.

## Setup

1. Install the add-on from the repository
2. Configure your Wyze credentials in the add-on settings:
   - **WYZE_EMAIL** — your Wyze account email
   - **WYZE_PASSWORD** — your Wyze account password
   - **WYZE_API_ID** — API ID from [Wyze Developer Console](https://developer-api-console.wyze.com/#/apikey/view)
   - **WYZE_API_KEY** — API Key from the same page
3. Start the add-on
4. Open the WebUI from the sidebar

## MQTT Auto-Detection

If you have the Mosquitto broker add-on installed, MQTT is configured automatically. The bridge will publish camera states and Home Assistant discovery messages.

## WebRTC

For WebRTC streaming (lowest latency), set **WB_IP** to your Home Assistant host's IP address.

## Camera Filtering

Use **FILTER_NAMES**, **FILTER_MODELS**, or **FILTER_MACS** (comma-separated) to limit which cameras appear. Set **FILTER_BLOCKS** to `true` to **exclude** instead of include.

## Recording

Set **RECORD_ALL** to `true` to record all cameras, or use per-camera `RECORD_{CAM_NAME}` overrides via **CAM_OPTIONS**.

## Per-Camera Options

Use **CAM_OPTIONS** in the add-on config to override settings per camera:

```yaml
CAM_OPTIONS:
  - CAM_NAME: front_door
    QUALITY: hd
    RECORD: true
  - CAM_NAME: backyard
    AUDIO: false
```

Camera names are matched case-insensitively; spaces are treated as underscores. Only letters, digits, and underscores are preserved — cameras with other punctuation in their Wyze-account names can't be matched via this option.

## Webhooks

Set **WEBHOOK_URLS** to a comma-separated list of URLs. The bridge will POST JSON on every camera state change (offline → discovering → connecting → streaming → error).

## Gwell Cameras (OG, Doorbell Pro, Doorbell Duo)

Support for Wyze's newer Gwell/IoTVideo cameras (GW_BE1, GW_GC1, GW_GC2, GW_DBD) is enabled by default via an embedded proxy. Set **GWELL_ENABLED** to `false` to disable it if you have only TUTK cameras and want to skip the sidecar.

## REST API Token

Set **WB_API** to a secret string to require that token on REST API requests. Leave empty to disable API auth (the WebUI itself is still covered by `WB_AUTH`/`WB_USERNAME`/`WB_PASSWORD`).

## Ports

| Port | Protocol | Purpose |
| ------ | ---------- | --------- |
| 5080 | TCP | WebUI (ingress) |
| 1984 | TCP | go2rtc native UI |
| 8554 | TCP | RTSP |
| 8888 | TCP | HLS |
| 8889 | TCP | WebRTC HTTP |
| 8189 | UDP | WebRTC ICE |

## Support

- [GitHub Issues](https://github.com/IDisposable/docker-wyze-bridge/issues)
- [Migration Guide](https://github.com/IDisposable/docker-wyze-bridge/blob/main/MIGRATION.md)
