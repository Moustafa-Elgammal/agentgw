package core

import "testing"

func TestScopeCap(t *testing.T) {
	tests := []struct {
		s, max, want Scope
	}{
		{ScopeOperator, "", ScopeOperator},
		{ScopeOperator, ScopeOperator, ScopeOperator},
		{ScopeOperator, ScopeCoach, ScopeCoach},
		{ScopeCoach, ScopeCoach, ScopeCoach},
		{"", ScopeCoach, ScopeCoach},            // not scope-limited (dev auth) becomes coach
		{"", ScopeOperator, ""},                 // no cap leaves dev auth alone
		{"junk", ScopeCoach, "junk"},            // a corrupt grant stays corrupt and allows nothing
		{ScopeCoach, ScopeOperator, ScopeCoach}, // a cap never raises a scope
	}
	for _, tt := range tests {
		if got := tt.s.Cap(tt.max); got != tt.want {
			t.Errorf("%q.Cap(%q) = %q, want %q", tt.s, tt.max, got, tt.want)
		}
		if got := tt.s.Cap(tt.max); got.Allows(TierMedium) && tt.max == ScopeCoach {
			t.Errorf("%q under a coach cap still allows medium tools", tt.s)
		}
	}
}
