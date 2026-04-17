# Developer Guide

How to build, run, and test wyze-bridge.

## Recommended: VS Code Dev Container

The easiest way to develop is with the included devcontainer. It provides Go, go2rtc, and all tools pre-installed in a Linux environment.

### Setup

1. Install [Docker Desktop](https://www.docker.com/products/docker-desktop/) and the [Dev Containers](https://marketplace.visualstudio.com/items?itemName=ms-vscode-remote.remote-containers) VS Code extension
2. Copy `.env.dev.example` to `.env.dev` and fill in your Wyze credentials
3. Open the repo in VS Code, then **Reopen in Container** when prompted (or Ctrl+Shift+P → "Dev Containers: Reopen in Container")
4. The container builds with Go 1.26.2, go2rtc, gopls, and golangci-lint pre-installed

### Network requirements

The devcontainer uses `--network=host` because TUTK P2P (the protocol go2rtc uses to reach cameras) can't traverse Docker's default bridge network — UDP discovery times out. On WSL2 + Docker Desktop this works transparently; the container shares the WSL2 VM's network namespace. If you're on Docker Desktop Mac/Windows without WSL, host networking isn't supported the same way — you'll need to run the bridge natively instead.

You can tell it's working when the logs show `wyze: dial wyze://...` followed by `camera connected successfully` within a few seconds. A **discovery timeout** error means UDP isn't reaching the cameras.

### Run inside the devcontainer

```bash
set -a; source .env.dev; set +a
go run ./cmd/wyze-bridge
```

The WebUI is at http://localhost:5080 (ports are auto-forwarded).

## Alternative: Native Setup

If you prefer developing outside a container, you need:

- **Go 1.26.2+** — [go.dev/dl](https://go.dev/dl/)
- **go2rtc binary** — handles the actual camera streaming
- **Wyze account** with API credentials from [developer-api-console.wyze.com](https://developer-api-console.wyze.com/#/apikey/view)
- Cameras on the **same LAN** as your dev machine

### 1. Get go2rtc

Download from [github.com/AlexxIT/go2rtc/releases](https://github.com/AlexxIT/go2rtc/releases/tag/v1.9.14) and place in the repo root:

```bash
# Linux/macOS (amd64)
curl -fsSL https://github.com/AlexxIT/go2rtc/releases/download/v1.9.14/go2rtc_linux_amd64 -o go2rtc
chmod +x go2rtc

# macOS (Apple Silicon)
curl -fsSL https://github.com/AlexxIT/go2rtc/releases/download/v1.9.14/go2rtc_darwin_arm64 -o go2rtc
chmod +x go2rtc

# Windows (PowerShell)
Invoke-WebRequest -Uri "https://github.com/AlexxIT/go2rtc/releases/download/v1.9.14/go2rtc_win64.zip" -OutFile go2rtc.zip
Expand-Archive go2rtc.zip -DestinationPath .
Remove-Item go2rtc.zip
```

The bridge will find `./go2rtc` (or `./go2rtc.exe`) automatically.

### 2. Create your env file

```bash
cp .env.dev.example .env.dev
# Edit .env.dev with your Wyze credentials
```

#### Required credentials

| Variable | Where to get it |
| ---------- | ----------------- |
| `WYZE_EMAIL` | Your Wyze account email |
| `WYZE_PASSWORD` | Your Wyze account password |
| `WYZE_API_ID` | [Wyze Developer Console](https://developer-api-console.wyze.com/#/apikey/view) → API Keys |
| `WYZE_API_KEY` | Same page as API ID |

#### Optional: WYZE_TOTP_KEY (for accounts with 2FA)

If your Wyze account has two-factor authentication enabled, the bridge needs the TOTP secret to generate login codes automatically. This is the base32-encoded seed your authenticator app uses — not the 6-digit code itself.

To get it: go to your Wyze account's 2FA setup and choose "can't scan QR code" to reveal the raw secret (looks like `JBSWY3DPEHPK3PXP`). Set it in `.env.dev`:

```
WYZE_TOTP_KEY=JBSWY3DPEHPK3PXP
```

If your account does **not** have 2FA enabled, leave it blank.

### 3. Create local directories

```bash
mkdir -p local/config local/img
```

## Running

### Source the env and run

**Linux/macOS:**
```bash
set -a; source .env.dev; set +a
go run ./cmd/wyze-bridge
```

**Windows (PowerShell):**
```powershell
Get-Content .env.dev | ForEach-Object {
    if ($_ -match '^([^#]\S+?)=(.*)$') {
        [Environment]::SetEnvironmentVariable($matches[1], $matches[2], "Process")
    }
}
go run ./cmd/wyze-bridge
```

**Windows (Git Bash / MSYS2):**
```bash
set -a; source .env.dev; set +a
go run ./cmd/wyze-bridge
```

### What happens on startup

1. Loads config from environment variables
2. Restores auth/camera state from `./local/config/wyze-bridge.state.json` (if exists)
3. Starts go2rtc subprocess (finds `./go2rtc` in current dir)
4. Waits for go2rtc to be ready (up to 10s)
5. Logs into Wyze API (or uses cached token)
6. Discovers cameras and adds them to go2rtc
7. Starts WebUI on [http://localhost:5080](http://localhost:5080)

### Accessing streams

| What | URL |
|------|-----|
| **Bridge WebUI** | http://localhost:5080 |
| **go2rtc native UI** | http://localhost:1984 |
| **RTSP stream** | `rtsp://localhost:8554/{camera_name}` |
| **HLS stream** | http://localhost:8888/{camera_name} |
| **Health check** | http://localhost:5080/api/health |
| **Camera list** | http://localhost:5080/api/cameras |

Camera names are lowercase with underscores: "Front Door" becomes `front_door`.

## Testing

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run a specific package
go test -v ./internal/wyzeapi/

# Run a specific test
go test -v -run TestIntegration_Login ./internal/wyzeapi/

# Race detector (Linux/macOS only — needs CGO)
CGO_ENABLED=1 go test -race ./...
```

## Debugging

### Log levels

```bash
LOG_LEVEL=debug go run ./cmd/wyze-bridge    # verbose bridge logs
LOG_LEVEL=trace go run ./cmd/wyze-bridge    # everything including go2rtc relay
FORCE_IOTC_DETAIL=true go run ./cmd/wyze-bridge  # TUTK protocol tracing
```

When running from a terminal, logs are human-readable (colored, timestamped). In Docker (non-TTY), logs are JSON.

### Inspecting the Wyze API response

Set `LOG_LEVEL=debug` to see every device discovered from the Wyze API, including devices that are skipped (wrong product_type, missing P2P params). This is useful for diagnosing why a camera doesn't appear.

### go2rtc native UI

Open http://localhost:1984 to access go2rtc's built-in interface. You can:
- See active streams and their codecs
- Test stream playback directly
- Add streams manually with `wyze://` URLs
- Check connection status for individual cameras

### State file

`./local/config/wyze-bridge.state.json` contains cached auth tokens and camera data. Delete it to force a fresh login:

```bash
rm local/config/wyze-bridge.state.json
```

## Project Structure

```
cmd/wyze-bridge/main.go     Entry point, DI wiring, signal handling
internal/
  config/                    Env vars, secrets, YAML, per-camera overrides
  wyzeapi/                   Wyze API client (auth, cameras, commands, TOTP)
  go2rtcmgr/                 go2rtc subprocess + config gen + HTTP API client
  camera/                    Per-camera state machine, manager, filter
  mqtt/                      Paho MQTT client, HA discovery, commands
  webui/                     net/http server, REST API, SSE, embedded assets
  snapshot/                  Interval + sunrise/sunset capture, pruning
  recording/                 Config generation, file pruning
  webhooks/                  HTTP POST notifications on state changes
```

## Adding a New Feature

1. Create your package in `internal/`
2. Write tests first (`*_test.go`)
3. Wire it into `cmd/wyze-bridge/main.go`
4. Add any new env vars to `internal/config/config.go` (in `Load()`)
5. Run `go vet ./...` and `go test ./...`
6. Update CLAUDE.md if the architecture changed significantly

## Common Tasks

### Adding a new env var

1. Add the field to `Config` struct in `internal/config/config.go`
2. Set its default in `Load()`
3. Add to the schema in BOTH `home_assistant/wyze_bridge/config.yaml` and `home_assistant/wyze_bridge_edge/config.yaml` if applicable
4. Add to BOTH `home_assistant/wyze_bridge/translations/en.yaml` and `home_assistant/wyze_bridge_edge/translations/en.yaml`
5. Document in README.md

### Adding a new camera command via MQTT

1. Add the topic handler in `internal/mqtt/subscribe.go`
2. Add the subscribe pattern in `subscribeCommands()`
3. Add the Wyze API call in `internal/wyzeapi/commands.go` if it's a cloud command
4. Add HA discovery entity in `internal/mqtt/discovery.go`
5. Test the topic parsing in `subscribe_test.go`

### Adding a new WebUI API endpoint

1. Add the handler in `internal/webui/api.go`
2. Register the route in `registerRoutes()` in `server.go`
3. Add a test in `api_test.go` using `httptest`
