package torr

import "testing"

func TestShouldPin(t *testing.T) {
	cases := []struct {
		name             string
		threshold        int
		connectedSeeders int
		alreadyPinned    bool
		want             bool
	}{
		// below threshold -> pin
		{"below threshold default 2", 2, 1, false, true},
		{"equal threshold", 2, 2, false, true},
		{"zero seeders always pins when threshold>=0", 2, 0, false, true},
		// above threshold -> no pin
		{"above threshold", 2, 3, false, false},
		{"way above threshold", 2, 50, false, false},
		// threshold == 0 means "always fetch": only zero seeders pins
		{"threshold zero with zero seeders", 0, 0, false, true},
		{"threshold zero with any seeders", 0, 1, false, false},
		// negative threshold is clamped to zero
		{"negative threshold treated as zero", -1, 0, false, true},
		{"negative threshold with one seeder", -1, 1, false, false},
		// already pinned -> no decision
		{"already pinned is a no-op", 2, 0, true, false},
		{"already pinned even when rare", 2, 0, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldPin(tc.threshold, tc.connectedSeeders, tc.alreadyPinned)
			if got != tc.want {
				t.Errorf("shouldPin(%d,%d,%v) = %v, want %v",
					tc.threshold, tc.connectedSeeders, tc.alreadyPinned, got, tc.want)
			}
		})
	}
}
