package mqtt

import "github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"

// cloudSetProperty maps MQTT set topics to Wyze cloud SetProperty PIDs.
// The table lives in wyzeapi (CloudProperties) so the WebUI's camera
// controls use the same one; these names keep the MQTT code unchanged.
var cloudSetProperty = wyzeapi.CloudProperties

// parseSetPropertyValue converts MQTT payloads into Wyze pvalue and
// a normalized value to publish back to MQTT.
func parseSetPropertyValue(property, raw string) (pvalue, publishValue string, ok bool) {
	return wyzeapi.ParsePropertyValue(property, raw)
}
