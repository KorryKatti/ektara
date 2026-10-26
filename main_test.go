package main

import (
	"runtime"
	"strings"
	"testing"
)

// The version line is all --version prints, so it has to carry everything a bug
// report needs: the name, the stamped version and commit, the Go version and
// the platform. The stamped values are whatever the build left behind, which in
// a test run is the default.
func TestVersionLine(t *testing.T) {
	line := versionLine()

	for _, want := range []string{
		"ektara ",
		version,
		commit,
		runtime.Version(),
		runtime.GOOS + "/" + runtime.GOARCH,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("version line %q is missing %q", line, want)
		}
	}
}
