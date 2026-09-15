package domain

// Transition validates a card action and returns its target and whether it is
// an already-applied no-op. It is shared by synchronous API commands and the
// executor so both surfaces enforce one lifecycle.
func Transition(current, action string) (target string, valid, ignored bool) {
	target = map[string]string{
		"activate": "active",
		"suspend":  "suspended",
		"resume":   "active",
		"close":    "closed",
		"expire":   "expired",
	}[action]
	if target == "" {
		return "", false, false
	}
	if current == target {
		return target, true, true
	}
	switch action {
	case "activate":
		return target, current == "issued", false
	case "suspend":
		return target, current == "active", false
	case "resume":
		return target, current == "suspended", false
	case "close":
		return target, current == "issued" || current == "active" || current == "suspended", false
	case "expire":
		return target, current == "pending" || current == "issued" || current == "active" || current == "suspended", false
	}
	return "", false, false
}

// CanTransition validates a direct status transition, as used by a batch.
func CanTransition(current, target string) (valid, ignored bool) {
	if current == target {
		return true, true
	}
	for _, action := range []string{"activate", "suspend", "resume", "close", "expire"} {
		candidate, ok, _ := Transition(current, action)
		if ok && candidate == target {
			return true, false
		}
	}
	return false, false
}
