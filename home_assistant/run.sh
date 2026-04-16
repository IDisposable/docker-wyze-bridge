#!/usr/bin/env bashio

# Read HA add-on options and export as environment variables
# bashio provides access to the add-on config via bashio::config

# Wyze credentials
if bashio::config.has_value 'WYZE_EMAIL'; then
    export WYZE_EMAIL="$(bashio::config 'WYZE_EMAIL')"
fi
if bashio::config.has_value 'WYZE_PASSWORD'; then
    export WYZE_PASSWORD="$(bashio::config 'WYZE_PASSWORD')"
fi
if bashio::config.has_value 'WYZE_API_ID'; then
    export WYZE_API_ID="$(bashio::config 'WYZE_API_ID')"
fi
if bashio::config.has_value 'WYZE_API_KEY'; then
    export WYZE_API_KEY="$(bashio::config 'WYZE_API_KEY')"
fi
if bashio::config.has_value 'TOTP_KEY'; then
    export TOTP_KEY="$(bashio::config 'TOTP_KEY')"
fi

# Network
if bashio::config.has_value 'WB_IP'; then
    export WB_IP="$(bashio::config 'WB_IP')"
fi

# Auth
if bashio::config.has_value 'WB_AUTH'; then
    export WB_AUTH="$(bashio::config 'WB_AUTH')"
fi
if bashio::config.has_value 'WB_USERNAME'; then
    export WB_USERNAME="$(bashio::config 'WB_USERNAME')"
fi
if bashio::config.has_value 'WB_PASSWORD'; then
    export WB_PASSWORD="$(bashio::config 'WB_PASSWORD')"
fi
if bashio::config.has_value 'WB_API'; then
    export WB_API="$(bashio::config 'WB_API')"
fi
if bashio::config.has_value 'STREAM_AUTH'; then
    export STREAM_AUTH="$(bashio::config 'STREAM_AUTH')"
fi

# Camera
if bashio::config.has_value 'QUALITY'; then
    export QUALITY="$(bashio::config 'QUALITY')"
fi
if bashio::config.has_value 'AUDIO'; then
    export AUDIO="$(bashio::config 'AUDIO')"
fi

# MQTT — auto-detect from HA if available
if bashio::services.available "mqtt"; then
    export MQTT_ENABLED=true
    if ! bashio::config.has_value 'MQTT_HOST'; then
        export MQTT_HOST="$(bashio::services mqtt "host")"
        export MQTT_PORT="$(bashio::services mqtt "port")"
        export MQTT_USERNAME="$(bashio::services mqtt "username")"
        export MQTT_PASSWORD="$(bashio::services mqtt "password")"
    fi
fi
if bashio::config.has_value 'MQTT_ENABLED'; then
    export MQTT_ENABLED="$(bashio::config 'MQTT_ENABLED')"
fi
if bashio::config.has_value 'MQTT_HOST'; then
    export MQTT_HOST="$(bashio::config 'MQTT_HOST')"
fi
if bashio::config.has_value 'MQTT_PORT'; then
    export MQTT_PORT="$(bashio::config 'MQTT_PORT')"
fi
if bashio::config.has_value 'MQTT_USERNAME'; then
    export MQTT_USERNAME="$(bashio::config 'MQTT_USERNAME')"
fi
if bashio::config.has_value 'MQTT_PASSWORD'; then
    export MQTT_PASSWORD="$(bashio::config 'MQTT_PASSWORD')"
fi
if bashio::config.has_value 'MQTT_TOPIC'; then
    export MQTT_TOPIC="$(bashio::config 'MQTT_TOPIC')"
fi
if bashio::config.has_value 'MQTT_DTOPIC'; then
    export MQTT_DTOPIC="$(bashio::config 'MQTT_DTOPIC')"
fi

# Filtering
if bashio::config.has_value 'FILTER_NAMES'; then
    export FILTER_NAMES="$(bashio::config 'FILTER_NAMES')"
fi
if bashio::config.has_value 'FILTER_MODELS'; then
    export FILTER_MODELS="$(bashio::config 'FILTER_MODELS')"
fi
if bashio::config.has_value 'FILTER_MACS'; then
    export FILTER_MACS="$(bashio::config 'FILTER_MACS')"
fi
if bashio::config.has_value 'FILTER_BLOCKS'; then
    export FILTER_BLOCKS="$(bashio::config 'FILTER_BLOCKS')"
fi

# Recording
if bashio::config.has_value 'RECORD_ALL'; then
    export RECORD_ALL="$(bashio::config 'RECORD_ALL')"
fi
if bashio::config.has_value 'RECORD_PATH'; then
    export RECORD_PATH="$(bashio::config 'RECORD_PATH')"
fi
if bashio::config.has_value 'RECORD_FILE_NAME'; then
    export RECORD_FILE_NAME="$(bashio::config 'RECORD_FILE_NAME')"
fi
if bashio::config.has_value 'RECORD_LENGTH'; then
    export RECORD_LENGTH="$(bashio::config 'RECORD_LENGTH')"
fi
if bashio::config.has_value 'RECORD_KEEP'; then
    export RECORD_KEEP="$(bashio::config 'RECORD_KEEP')"
fi

# Snapshots
if bashio::config.has_value 'SNAPSHOT_INT'; then
    export SNAPSHOT_INT="$(bashio::config 'SNAPSHOT_INT')"
fi
if bashio::config.has_value 'SNAPSHOT_FORMAT'; then
    export SNAPSHOT_FORMAT="$(bashio::config 'SNAPSHOT_FORMAT')"
fi
if bashio::config.has_value 'SNAPSHOT_CAMERAS'; then
    export SNAPSHOT_CAMERAS="$(bashio::config 'SNAPSHOT_CAMERAS')"
fi
if bashio::config.has_value 'SNAPSHOT_KEEP'; then
    export SNAPSHOT_KEEP="$(bashio::config 'SNAPSHOT_KEEP')"
fi
if bashio::config.has_value 'IMG_DIR'; then
    export IMG_DIR="$(bashio::config 'IMG_DIR')"
fi

# Location
if bashio::config.has_value 'LATITUDE'; then
    export LATITUDE="$(bashio::config 'LATITUDE')"
fi
if bashio::config.has_value 'LONGITUDE'; then
    export LONGITUDE="$(bashio::config 'LONGITUDE')"
fi

# Debugging
if bashio::config.has_value 'LOG_LEVEL'; then
    export LOG_LEVEL="$(bashio::config 'LOG_LEVEL')"
fi
if bashio::config.has_value 'FORCE_IOTC_DETAIL'; then
    export FORCE_IOTC_DETAIL="$(bashio::config 'FORCE_IOTC_DETAIL')"
fi

# Webhooks
if bashio::config.has_value 'WEBHOOK_URLS'; then
    export WEBHOOK_URLS="$(bashio::config 'WEBHOOK_URLS')"
fi

# Gwell (IoTVideo) P2P proxy — master toggle for GW_* cameras
# (OG, Doorbell Pro, Doorbell Duo). Defaults to enabled in the bridge;
# users only need to flip this to disable.
if bashio::config.has_value 'GWELL_ENABLED'; then
    export GWELL_ENABLED="$(bashio::config 'GWELL_ENABLED')"
fi

# Per-camera overrides from CAM_OPTIONS array.
# HA stores the add-on options as JSON in /data/options.json. bashio's
# scalar helpers don't iterate arrays, so we fan CAM_OPTIONS out with jq
# into the same QUALITY_{CAM}/AUDIO_{CAM}/RECORD_{CAM} env vars the Go
# bridge already consumes (internal/config/yaml.go:loadCamOverrides).
# Camera-name normalization matches Go's normalizeCamName:
# uppercase, spaces→underscores.
if bashio::config.has_value 'CAM_OPTIONS'; then
    bashio::log.info "Applying per-camera CAM_OPTIONS overrides..."
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
    done < <(jq -r '.CAM_OPTIONS[]? | [.CAM_NAME, (.QUALITY // "null"), (.AUDIO // "null"), (.RECORD // "null")] | @tsv' /data/options.json)
fi

# State dir — use HA add-on config directory
export STATE_DIR="/config"

bashio::log.info "Starting Wyze Bridge..."
exec /usr/local/bin/wyze-bridge
