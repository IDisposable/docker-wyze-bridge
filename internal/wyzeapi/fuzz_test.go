package wyzeapi

import "testing"

// ParsePropertyValue takes whatever MQTT or the WebUI sends; it must
// never panic, and anything it accepts must map to a non-empty Wyze
// value for a property the table knows.
func FuzzParsePropertyValue(f *testing.F) {
	f.Add("night_vision", "auto")
	f.Add("hor_flip", " ON ")
	f.Add("bitrate", "120")
	f.Add("fps", "-1")
	f.Add("nope", "on")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, property, raw string) {
		pvalue, publish, ok := ParsePropertyValue(property, raw)
		if !ok {
			if pvalue != "" || publish != "" {
				t.Fatalf("rejected value leaked output: %q %q", pvalue, publish)
			}
			return
		}
		if _, known := CloudProperties[property]; !known {
			t.Fatalf("accepted a value for unknown property %q", property)
		}
		if pvalue == "" || publish == "" {
			t.Fatalf("accepted %q=%q with empty output", property, raw)
		}
	})
}
