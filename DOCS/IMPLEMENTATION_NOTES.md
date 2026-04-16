# Implementation Notes: Deviations, Discoveries, and Remaining Work

**Companion to:** `DESIGN.md`
**Written after:** Initial implementation on branch `go-rewrite`
**Date:** April 16, 2026

This document captures what changed during implementation compared to the original design, why, and what's still dangling.

---

## 1. Structural Deviations from DESIGN.md

### 1.1 File Layout vs Design Section 7

The design specified a clean file-per-concern layout. The implementation matches closely but has these additions/changes:

| Design said | Implementation | Why |
|-------------|---------------|-----|
| `internal/wyzeapi/models.go` | `models.go` + `totp.go` | TOTP/MFA needed its own file — RFC 4226 HOTP implementation is ~40 lines, cleaner separate |
| `internal/wyzeapi/auth.go` | `auth.go` + `http.go` | HTTP transport (request/response validation, error types) extracted to `http.go` to keep `auth.go` focused on authentication logic |
| `internal/webui/static/index.html` | Templates inline in `templates.go` | Two pages didn't justify separate HTML files. Go template strings in a `.go` file are simpler and still embed via `embed.FS` for the CSS/JS. If the UI grows beyond 2-3 pages, move to embedded HTML files. |
| `internal/webui/m3u8.go` | M3U8 generation inline in `api.go` | The handlers are <20 lines each — a dedicated file was empty overhead. `m3u8.go` exists as a placeholder noting this. |
| `internal/mqtt/format.go` | **Added** (not in design) | Extracted pure formatting functions from paho-dependent publish/subscribe code to make them testable without a broker. This is the biggest structural addition. |
| `docker/scripts/verify-go2rtc.sh` | Not created | Hash verification of the go2rtc download is a good idea but was deferred. The Dockerfile fetches by exact version tag, which is sufficient for now. |

### 1.2 Go2RTCConfig.Streams Type Change

Design section 5.2.2 shows `Streams map[string][]string`. Implementation uses `map[string]interface{}` because streams with recording enabled need a richer structure:

```yaml
# Simple stream (no recording):
front_door:
  - wyze://...

# Recording-enabled stream:
backyard:
  sources:
    - wyze://...
  record: true
  record_path: /record/backyard/%Y/%m/%d/%H-%M-%S
  record_duration: 60s
```

The `interface{}` value is either `[]string` (simple) or `map[string]interface{}` (with recording). This is a pragmatic trade-off — a union type or separate config section would be cleaner but go2rtc's YAML schema expects inline stream config.

### 1.3 RTSPConfig Extended for STREAM_AUTH

Design section 5.5.5 describes `STREAM_AUTH` translation. The implementation adds `Username`/`Password` fields to `RTSPConfig` for the single-global-user case. Per-camera auth (multiple users with camera lists) would need go2rtc's path-level credential injection, which go2rtc doesn't support in its YAML config as of v1.9.14 — it's an API-time concern. This is documented in the remaining work section.

### 1.4 MQTT Client Carries wyzeapi.Client Reference

Design section 5.4 shows MQTT as a standalone component. During implementation, the `set/night_vision` command handler needs to call `wyzeapi.SetProperty()`, so the MQTT `Client` struct gained a `wyzeAPI *wyzeapi.Client` field. This is a minor coupling increase but avoids an unnecessary callback abstraction for one command handler.

---

## 2. Discoveries During Implementation

### 2.1 Wyze API URL Constants Are Package-Level

The Python bridge uses module-level constants for API URLs (`AUTH_API`, `WYZE_API`, `CLOUD_API`). The Go port does the same (`const authAPI`, `wyzeAPI`, `cloudAPI`). This makes the `Login()`, `RefreshToken()`, `GetCameraList()` methods difficult to test with `httptest` — you can't redirect to a test server without making these configurable.

**Current workaround:** Tests exercise the transport layer (`postJSON`, `postRaw`, `validateResponse`) and the pure logic (`hashPassword`, `signMsg`, payload construction) via `httptest`, but the high-level `Login()` → `GetCameraList()` flow requires either:
- Making base URLs injectable on the `Client` struct (clean, recommended for Phase 2)
- Environment variable override (hacky)

**Recommendation:** Add `baseAuthURL`, `baseWyzeURL`, `baseCloudURL` fields to `Client` with defaults pointing at production. Tests set them to `httptest.Server.URL`. This would push wyzeapi coverage from 44% to ~85%.

### 2.2 go2rtc Log Format Varies

Design section 6 specifies go2rtc log level mapping (`[DEBUG]` → Trace, `[INFO]` → Debug, etc.). In practice, go2rtc's log format is `HH:MM:SS.mmm [LEVEL] component message`, but the level marker position and casing can vary between versions. The `emitLogLine` implementation does case-insensitive `Contains` matching, which is robust but could mis-classify lines containing `[DEBUG]` as a substring of a message.

### 2.3 State File Permissions on Windows

`StateFile.Save()` writes with `0600` permissions. On Windows, Go's `os.WriteFile` ignores Unix permission bits — the file gets default ACLs. The permission test is skipped on Windows with `filepath.Separator == '\\'`. In Docker (Linux), this works correctly.

### 2.4 TOTP Implementation Is Minimal but Correct

The design mentions `TOTPKey` support but doesn't detail the implementation. The Go port implements RFC 4226 HOTP + RFC 6238 TOTP in ~40 lines (`totp.go`) using only `crypto/hmac`, `crypto/sha1`, and `encoding/base32` — no external dependency. Tested against known vectors. The MFA flow in `completeMFA()` follows the same endpoint and payload structure as the Python `mfa_login()`.

### 2.5 Camera `InjectCamera()` Method

Not in the design — added to `camera.Manager` to support test injection of cameras without going through the full Wyze API discovery flow. This is test-only infrastructure but exported because the webui tests (separate package) need it.

---

## 3. Remaining Dangling Wires

These are features that are partially implemented — code exists but the full pipeline isn't connected end-to-end.

### 3.1 STREAM_AUTH Per-Camera Injection — Resolved as Limitation

**Investigation:** go2rtc source (`internal/rtsp/rtsp.go`) confirms a single global `username`/`password` on the RTSP server. There is no per-path or per-stream credential differentiation. Loopback connections (localhost) skip auth entirely, which is how the bridge WebUI player works.

**What works:**
- `STREAM_AUTH=user:pass` → sets `RTSP.Username`/`RTSP.Password` in go2rtc YAML. All RTSP/WebRTC consumers must provide these credentials. Tested.
- If multiple `|`-separated users are specified, the first entry's credentials are used globally (the "first user wins" approach is documented in MIGRATION.md).

**What doesn't work:**
- `STREAM_AUTH=user1:pass1@cam1,cam2|user2:pass2@cam3` — per-camera credential scoping. The Python bridge enforced this at its own proxy layer (Flask + MediaMTX). go2rtc has no equivalent. This is a **documented feature loss** in MIGRATION.md.

**No further code changes needed.** The `ParseStreamAuth` code and tests remain useful for extracting the first user's credentials; the per-camera camera lists are parsed but ignored.

### 3.2 ON_DEMAND Streaming — Resolved: Dropped

**Decision:** `ON_DEMAND` is removed entirely. All cameras connect eagerly at startup for reliability and speed. The env var is silently ignored. Documented in MIGRATION.md.

### 3.3 MQTT Snapshot Trigger — Resolved

`OnSnapshotRequest` callback added to MQTT Client. `handleSnapshotCommand` calls it. Wired in `main.go`: `mqttClient.OnSnapshotRequest(snapMgr.CaptureOne)`.

### 3.4 Wyze API Base URL Injectability — Resolved

`AuthURL`, `WyzeURL`, `CloudURL` are now exported fields on `Client` with production defaults. All call sites use `c.AuthURL` etc. Integration tests point these at `httptest.Server` URLs. wyzeapi coverage jumped from 44% to 75%.

### 3.5 go2rtc Version Detection — Dropped

Not worth the complexity. Version is pinned in the Dockerfile; hardcoded string in `/api/version` is fine.

### 3.6 Webhook Support (Phase 3, Not Started)

Design section 12 Phase 3 lists "Webhook support (offline/online events)". No code exists. This would be a new `internal/webhooks/` package that POSTs camera state changes to configurable URLs.

### 3.7 HA Add-on / Unraid — Partially Resolved

Unraid template dropped entirely (`unraid/` removed, documented in MIGRATION.md). HA add-on being rebuilt from scratch following current HA developer docs (`developers.home-assistant.io/docs/apps/`).

### 3.8 README.md and MIGRATION.md

MIGRATION.md: Done. Covers all breaking changes, env var reference, and feature losses.
README.md: In progress.

---

## 4. Test Architecture Notes

The test suite (194 tests) uses three patterns:

1. **Pure unit tests** — config parsing, crypto functions, formatting, models. No mocks needed.
2. **httptest mock servers** — go2rtc API client, Wyze API transport layer, WebUI handlers. Each test spins up a temporary HTTP server.
3. **State-injected tests** — camera manager operations use `InjectCamera()` to bypass Wyze API discovery. Verifies state machine transitions, health checks, reconnection.

The MQTT package is the hardest to test because `paho.mqtt.golang` doesn't offer a mock broker. The pure formatting logic was extracted to `format.go` (testable), but the paho-dependent `Publish`/`Subscribe`/`Connect` paths remain at 0% coverage. Options:
- Use a real MQTT broker in CI (e.g., `eclipse-mosquitto` in Docker)
- Use `github.com/mochi-mqtt/server` as an in-process test broker
- Accept that these paths are integration-only (current approach)

---

## 5. Performance and Size Notes

- **Binary size:** `go build` produces ~12MB before stripping. With `-ldflags="-s -w"`: ~8MB. Combined with go2rtc (~10MB) and Alpine base (~5MB), total image should be ~25MB — well under the 50MB target.
- **Memory:** No profiling done yet, but no large allocations. All camera data is in-memory maps; go2rtc handles the heavy media work.
- **Startup time:** Sequential: config → state file → go2rtc start → wait ready (up to 10s) → Wyze login → discovery → connect cameras. Total cold start estimate: 15-20s.
