package server

import (
	"encoding/json"
	"strings"
)

// ValidUUID accepts canonical lowercase UUID text used by the public API.
func ValidUUID(value string) bool {
	if len(value) != 36 || strings.ToLower(value) != value {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

// ValidCardStatus accepts the public card-status filter values.
func ValidCardStatus(value string) bool {
	switch value {
	case "pending", "issued", "active", "suspended", "closed", "expired":
		return true
	default:
		return false
	}
}

// JSONObject requires a JSON object and rejects arrays, scalars, and null.
func JSONObject(value json.RawMessage) bool {
	if len(value) == 0 {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

// JSONObjectOrNil permits an omitted JSON object field.
func JSONObjectOrNil(value json.RawMessage) bool { return value == nil || JSONObject(value) }
