package pool

import (
	"strings"
)

// Some models take no temperature at all: Anthropic's Claude 5.5 family,
// reached through its OpenAI-compatible endpoint, answers 400 "`temperature`
// is deprecated for this model" to any request that carries one, so every
// call failed before it began. A client that hears that drops the field and
// asks again, and remembers (temperatureRejected) so the requests after it
// go without it from the start.

// withoutTemperature is body without its temperature, and whether it had
// one; a body that is not a request map comes back as it was.
func withoutTemperature(body interface{}) (interface{}, bool) {
	m, ok := body.(map[string]interface{})
	if !ok {
		return body, false
	}
	if _, has := m["temperature"]; !has {
		return body, false
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if k != "temperature" {
			out[k] = v
		}
	}
	return out, true
}

// isTemperatureRejection reports whether err is an upstream refusing the
// temperature parameter.
func isTemperatureRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "temperature") || !strings.Contains(msg, "status 400") {
		return false
	}
	for _, w := range []string{"deprecated", "not supported", "unsupported", "does not support", "not allowed", "invalid", "only the default"} {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}
