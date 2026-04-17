# Wyze Bridge for Home Assistant

Stream your Wyze cameras locally via WebRTC, RTSP, and HLS — no cloud required.

## Setup

1. Install the add-on from the repository
2. In the **Configuration** tab, fill in the **Wyze** section:
   - **email** — your Wyze account email
   - **password** — your Wyze account password
   - **api_id** — API ID from [Wyze Developer Console](https://developer-api-console.wyze.com/#/apikey/view)
   - **api_key** — API Key from the same page
3. For WebRTC streaming, fill in the **Bridge** section:
   - **ip** — your Home Assistant host's LAN IP (required for in-browser WebRTC to work through ingress)
4. Start the add-on
5. Open the WebUI from the sidebar

The Configuration tab is grouped into collapsible sections: **wyze**, **bridge**, **camera**, **snapshot**, **record**, **mqtt**, **filter**, **location**, **webhooks**, **gwell**, **debug**. Anything beyond the fields above is optional — click "Show unused optional configuration options" to expose the rest.

## MQTT Auto-Detection

If the Mosquitto broker add-on is installed, MQTT is auto-configured — leave the **mqtt** section empty and the bridge picks up Mosquitto's host/credentials automatically. Home Assistant discovery messages are published so cameras appear as entities without further setup.

## Camera Filtering

Under the **filter** section:

- **names**, **models**, **macs** — each is a list; the UI gives you an "Add" button per entry. Narrows which cameras the bridge exposes.
- **blocks** — when `true`, the lists above act as a block-list instead of an allow-list.

## Recording

Under the **record** section:

- **all** — `true` records every camera; per-camera overrides via `camera.options` below still apply.
- **path** — directory template. HA default: `/media/wyze_bridge/recordings/{cam_name}/%Y/%m/%d`.
- **file_name** — filename template; `.mp4` auto-appended. Default: `%H-%M-%S`.
- **length** — segment length. Examples: `60s`, `5m`, `1h`.
- **keep** — auto-delete segments older than this. `0` = keep forever. Examples: `7d`, `72h`.

## Snapshots

Under the **snapshot** section:

- **path** — directory template. HA default: `/media/wyze_bridge/snapshots/{cam_name}/%Y/%m/%d`.
- **file_name** — filename template; `.jpg` auto-appended. Default: `%H-%M-%S` (time-of-day).
- **interval** — seconds between captures. `0` disables periodic capture.
- **keep** — auto-delete old snapshots. Same format as `record.keep`.
- **cameras** — list of camera names to restrict snapshots to. Empty list = all cameras.

Both `path` and `file_name` accept `{cam_name}`, `{CAM_NAME}`, and strftime tokens `%Y %m %d %H %M %S %s`. Subdirectories in `path` are created automatically.

## Per-Camera Options

Add per-camera overrides under `camera.options`:

```yaml
camera:
  options:
    - cam_name: front_door
      quality: hd
      record: true
    - cam_name: backyard
      audio: false
```

Camera names are matched case-insensitively; spaces become underscores. Only letters, digits, and underscores are preserved — cameras with other punctuation in their Wyze-account names can't be matched via this option.

## Location (sunrise/sunset snapshots)

Fill **location.latitude** and **location.longitude** to have the bridge capture an extra snapshot at civil sunrise and sunset.

## Webhooks

Add entries to **webhooks.urls** (the UI gives a "+" Add button per URL). The bridge POSTs JSON to each URL on every camera state change (offline → discovering → connecting → streaming → error). HA validates each entry as a URL.

## Gwell Cameras (OG, Doorbell Pro, Doorbell Duo)

The **gwell.enabled** toggle controls the proxy for Wyze's newer Gwell/IoTVideo cameras (GW_BE1, GW_GC1, GW_GC2, GW_DBD). **Default is `false` in 4.0-beta** because the proxy binary isn't shipped in the Docker image yet — flipping it on today will put GW_* cameras in a permanent retry loop. Leave it off until a release note says otherwise.

## REST API / WebUI Auth

Under the **bridge** section:

- **auth** — when `true`, WebUI and REST API require HTTP Basic auth with `username` + `password`.
- **api_token** — bearer token for REST API requests specifically. Independent of `auth` and the username/password pair.
- **stream_auth** — `user:pass` applied to RTSP/WebRTC stream consumers via go2rtc.

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
