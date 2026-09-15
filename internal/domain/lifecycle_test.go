package domain

import "testing"

func TestTransition(t *testing.T) {
	tests := []struct {
		name, current, action, target string
		valid, ignored                bool
	}{
		{"activate issued", "issued", "activate", "active", true, false},
		{"same target is ignored", "active", "activate", "active", true, true},
		{"close suspended", "suspended", "close", "closed", true, false},
		{"expire pending", "pending", "expire", "expired", true, false},
		{"terminal is invalid", "closed", "expire", "expired", false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, valid, ignored := Transition(test.current, test.action)
			if target != test.target || valid != test.valid || ignored != test.ignored {
				t.Fatalf("Transition(%q, %q) = (%q, %t, %t)", test.current, test.action, target, valid, ignored)
			}
		})
	}
}

func TestCanTransition(t *testing.T) {
	if valid, ignored := CanTransition("active", "active"); !valid || !ignored {
		t.Fatal("expected same status to be ignored")
	}
	if valid, ignored := CanTransition("issued", "suspended"); valid || ignored {
		t.Fatal("expected invalid direct transition")
	}
}
