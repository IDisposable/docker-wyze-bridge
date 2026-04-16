package wyzeapi

import (
	"fmt"
)

// RunAction sends a run_action command to a camera via the Wyze cloud API.
func (c *Client) RunAction(cam CameraInfo, action string) error {
	if err := c.EnsureAuth(); err != nil {
		return err
	}

	payload := c.authenticatedPayload("run_action")
	payload["action_params"] = map[string]interface{}{}
	payload["action_key"] = action
	payload["instance_id"] = cam.MAC
	payload["provider_key"] = cam.Model
	payload["custom_string"] = ""

	_, err := c.postJSON(c.WyzeURL+"/v2/auto/run_action", c.defaultHeaders(), payload)
	if err != nil {
		return fmt.Errorf("run_action: %w", err)
	}
	return nil
}

// GetEventList fetches recent events for the given MAC addresses.
func (c *Client) GetEventList(macs []string, beginTimeMS, endTimeMS int64) ([]map[string]interface{}, error) {
	if err := c.EnsureAuth(); err != nil {
		return nil, err
	}

	payload := c.authenticatedPayload("get_event_list")

	// Deduplicate MACs
	macSet := make(map[string]bool)
	for _, m := range macs {
		macSet[m] = true
	}
	uniqueMACs := make([]string, 0, len(macSet))
	for m := range macSet {
		uniqueMACs = append(uniqueMACs, m)
	}

	payload["count"] = 20
	payload["order_by"] = 1
	payload["begin_time"] = beginTimeMS
	payload["end_time"] = endTimeMS
	payload["device_id_list"] = uniqueMACs
	payload["event_value_list"] = []interface{}{}
	payload["event_tag_list"] = []interface{}{}

	sorted := sortDict(payload)
	headers := c.signPayloadHeaders("9319141212m2ik", sorted)

	resp, err := c.postRaw(c.CloudURL+"/v4/device/get_event_list", headers, sorted)
	if err != nil {
		return nil, fmt.Errorf("get_event_list: %w", err)
	}

	eventList, ok := resp["event_list"].([]interface{})
	if !ok {
		return nil, nil
	}

	var result []map[string]interface{}
	for _, e := range eventList {
		if m, ok := e.(map[string]interface{}); ok {
			result = append(result, m)
		}
	}
	return result, nil
}
