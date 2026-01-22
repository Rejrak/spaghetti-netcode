package configcluster

import "testing"

func TestVersionStampIsNewerThan(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		a      VersionStamp
		b      VersionStamp
		newerA bool
	}{
		{
			name:   "version higher wins",
			a:      VersionStamp{Version: 1, NodeID: "a"},
			b:      VersionStamp{Version: 0, NodeID: "z"},
			newerA: true,
		},
		{
			name:   "node id tiebreaker",
			a:      VersionStamp{Version: 4, NodeID: "a"},
			b:      VersionStamp{Version: 4, NodeID: "b"},
			newerA: false,
		},
		{
			name:   "equal is not newer",
			a:      VersionStamp{Version: 3, NodeID: "a"},
			b:      VersionStamp{Version: 3, NodeID: "a"},
			newerA: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.a.IsNewerThan(tc.b); got != tc.newerA {
				t.Fatalf("IsNewerThan: got %v want %v", got, tc.newerA)
			}
		})
	}
}
