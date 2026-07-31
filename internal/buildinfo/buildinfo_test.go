package buildinfo_test

import (
	"strings"
	"testing"

	"shelf/internal/buildinfo"
)

func TestString_defaults_mentionEveryStamp(t *testing.T) {
	t.Parallel()

	got := buildinfo.String()
	for _, want := range []string{buildinfo.Version, buildinfo.Commit, buildinfo.Date} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "shelf ") {
		t.Errorf("String() = %q, want prefix %q", got, "shelf ")
	}
}
