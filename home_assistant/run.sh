#!/usr/bin/env bashio

# HA add-on options (nested under wyze/bridge/camera/snapshot/record/
# mqtt/filter/location/webhooks/gwell/debug) → flat env vars for the Go
# bridge. Bashio's jq-backed bashio::config accepts dotted keys.

# Export the value at <key> to <env_var> if the user populated it.
export_opt() {
    local key=$1
    local var=$2
    if bashio::config.has_value "$key"; then
        export "$var=$(bashio::config "$key")"
    fi
}

# Join a schema array at <jq_path> into a comma-separated env var. Empty
# arrays and missing paths produce no export so Go falls back to defaults.
# Example: export_array '.filter.names' FILTER_NAMES
export_array() {
    local jq_path=$1
    local var=$2
    local joined
    joined=$(jq -r "${jq_path}? // [] | map(select(. != null and . != \"\")) | join(\",\")" /data/options.json)
    if [ -n "$joined" ]; then
        export "$var=$joined"
    fi
}

# ── Wyze account ────────────────────────────────────────────────────────────
export_opt 'wyze.email'      WYZE_EMAIL
export_opt 'wyze.password'   WYZE_PASSWORD
export_opt 'wyze.api_id'     WYZE_API_ID
export_opt 'wyze.api_key'    WYZE_API_KEY
export_opt 'wyze.totp_key'   WYZE_TOTP_KEY

# ── Bridge HTTP server + auth ───────────────────────────────────────────────
export_opt 'bridge.ip'          BRIDGE_IP
export_opt 'bridge.auth'        BRIDGE_AUTH
export_opt 'bridge.username'    BRIDGE_USERNAME
export_opt 'bridge.password'    BRIDGE_PASSWORD
export_opt 'bridge.api_token'   BRIDGE_API_TOKEN
export_opt 'bridge.stream_auth' STREAM_AUTH
export_opt 'bridge.go2rtc_url'  GO2RTC_URL

# ── Camera defaults ─────────────────────────────────────────────────────────
export_opt 'camera.quality' QUALITY
export_opt 'camera.audio'   AUDIO

# ── MQTT — auto-detect Mosquitto addon if the user didn't set a host ───────
if bashio::services.available "mqtt"; then
    export MQTT_ENABLED=true
    if ! bashio::config.has_value 'mqtt.host'; then
        export MQTT_HOST="$(bashio::services mqtt "host")"
        export MQTT_PORT="$(bashio::services mqtt "port")"
        export MQTT_USERNAME="$(bashio::services mqtt "username")"
        export MQTT_PASSWORD="$(bashio::services mqtt "password")"
    fi
fi
export_opt 'mqtt.enabled'         MQTT_ENABLED
export_opt 'mqtt.host'            MQTT_HOST
export_opt 'mqtt.port'            MQTT_PORT
export_opt 'mqtt.username'        MQTT_USERNAME
export_opt 'mqtt.password'        MQTT_PASSWORD
export_opt 'mqtt.topic'           MQTT_TOPIC
export_opt 'mqtt.discovery_topic' MQTT_DISCOVERY_TOPIC

# ── Filter ──────────────────────────────────────────────────────────────────
export_array '.filter.names'  FILTER_NAMES
export_array '.filter.models' FILTER_MODELS
export_array '.filter.macs'   FILTER_MACS
export_opt   'filter.blocks'  FILTER_BLOCKS

# ── Recording ───────────────────────────────────────────────────────────────
export_opt 'record.all'       RECORD_ALL
export_opt 'record.path'      RECORD_PATH
export_opt 'record.file_name' RECORD_FILE_NAME
export_opt 'record.length'    RECORD_LENGTH
export_opt 'record.keep'      RECORD_KEEP

# ── Snapshots ───────────────────────────────────────────────────────────────
export_opt 'snapshot.path'      SNAPSHOT_PATH
export_opt 'snapshot.file_name' SNAPSHOT_FILE_NAME
export_opt 'snapshot.interval'  SNAPSHOT_INTERVAL
export_opt 'snapshot.keep'      SNAPSHOT_KEEP
export_array '.snapshot.cameras' SNAPSHOT_CAMERAS

# ── Location (sunrise/sunset snapshots) ─────────────────────────────────────
export_opt 'location.latitude'  LATITUDE
export_opt 'location.longitude' LONGITUDE

# ── Webhooks + Gwell + Debug ────────────────────────────────────────────────
export_array '.webhooks.urls'        WEBHOOK_URLS
export_opt 'gwell.enabled'           GWELL_ENABLED
export_opt 'debug.log_level'         LOG_LEVEL
export_opt 'debug.force_iotc_detail' FORCE_IOTC_DETAIL

# ── Per-camera overrides ────────────────────────────────────────────────────
# Fan camera.options[] out to QUALITY_<NAME>/AUDIO_<NAME>/RECORD_<NAME> env
# vars that internal/config/yaml.go:loadCamOverrides consumes. Bashio has no
# native array iterator so we go straight to jq. Name normalization matches
# Go's normalizeCamName (uppercase, spaces→underscores, strip non-alnum_).
if bashio::config.has_value 'camera.options'; then
    bashio::log.info "Applying per-camera overrides from camera.options..."
    while IFS=$'\t' read -r cam_name quality audio record; do
        [ -z "$cam_name" ] && continue
        key="$(printf '%s' "$cam_name" | tr '[:lower:]' '[:upper:]' | tr ' ' '_' | tr -cd 'A-Z0-9_')"
        [ -z "$key" ] && continue
        if [ "$quality" != "null" ] && [ -n "$quality" ]; then
            export "QUALITY_${key}=${quality}"
        fi
        if [ "$audio" != "null" ] && [ -n "$audio" ]; then
            export "AUDIO_${key}=${audio}"
        fi
        if [ "$record" != "null" ] && [ -n "$record" ]; then
            export "RECORD_${key}=${record}"
        fi
    done < <(jq -r '.camera.options[]? | [.cam_name, (.quality // "null"), (.audio // "null"), (.record // "null")] | @tsv' /data/options.json)
fi

# ── State dir — HA persists /config ─────────────────────────────────────────
export STATE_DIR="/config"

# HA-specific defaults for paths that land on disk. The Go bridge's flat
# defaults (/img snapshots, /record recordings) assume bare-Docker with
# explicit volume mounts. In HA only /config and /media are persisted;
# anything else is ephemeral. Structured layouts below mirror RECORD_* so
# snapshots and recordings have parallel on-disk shapes. Base dirs are
# created up front so first write doesn't trip on a missing parent.
if ! bashio::config.has_value 'snapshot.path'; then
    export SNAPSHOT_PATH="/media/wyze_bridge/snapshots/{cam_name}/%Y/%m/%d"
fi
if ! bashio::config.has_value 'snapshot.file_name'; then
    export SNAPSHOT_FILE_NAME="%H-%M-%S"
fi
if ! bashio::config.has_value 'record.path'; then
    export RECORD_PATH="/media/wyze_bridge/recordings/{cam_name}/%Y/%m/%d"
fi
mkdir -p /media/wyze_bridge/snapshots /media/wyze_bridge/recordings

bashio::log.info "Starting Wyze Bridge..."
exec /usr/local/bin/wyze-bridge
