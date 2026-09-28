// stream_session_test.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMeasureDurationOnTruncatedFile is the one that actually pins
// measureDuration down, and it needs no network.
//
// It writes a known-length file, cuts it in half, and checks that the
// measurement halves too. format=duration cannot do that: ffmpeg writes the
// stream's total length into the webm header up front, so a half-file still
// reports the full length. That is the whole bug this function exists to avoid,
// and it is invisible in the live download test, where the first poll almost
// always lands before the file exists at all.
func TestMeasureDurationOnTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.webm")

	// A sine wave is the least interesting audio there is, which is the point:
	// nothing about the measurement may depend on the content. Three minutes is
	// long enough that a full/half comparison has room to be obvious.
	build := exec.Command("ffmpeg",
		"-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=180",
		"-c:a", "libopus", "-b:a", "48k",
		"-y", full,
	)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}

	whole := measureDuration(full)
	if whole < 170*time.Second || whole > 190*time.Second {
		t.Fatalf("whole file measured %v, wanted about 180s — is ffprobe sane?", whole)
	}

	// Cut the bytes in half. The audio in what is left is half the audio, and
	// that is what the measurement has to say.
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	half := filepath.Join(dir, "half.webm")
	if err := os.WriteFile(half, data[:len(data)/2], 0o644); err != nil {
		t.Fatal(err)
	}

	got := measureDuration(half)
	// Loose bounds on purpose. The first few hundred bytes of a webm are
	// headers, so half the bytes is a shade more than half the audio — the
	// true answer lands just over 50%. A header-based measurement would land
	// at 100%, so sitting at 75% still tells the two apart with room on both
	// sides.
	if got > whole*3/4 {
		t.Fatalf("half the file measured %v of a %v track — measureDuration is reading the header, not the packets", got, whole)
	}
	if got < whole/4 {
		t.Fatalf("half the file measured only %v of a %v track, which is too little to be the data on disk", got, whole)
	}
	fmt.Printf("whole=%v half-file=%v\n", whole, got)
}

// this one really downloads from youtube, so it needs a network and it takes
// as long as youtube takes
//
// It is skipped unless EKTARA_NETWORK_TESTS is set, so an ordinary go test ./...
// is hermetic and fast and does not depend on a website being up. YouTube also
// rate limits and answers 403 to a client it does not like, and a test that
// fails for that reason trains you to ignore it. Run it on purpose:
//
//	EKTARA_NETWORK_TESTS=1 go test -run TestStreamSession -v .
func TestStreamSession(t *testing.T) {
	if os.Getenv("EKTARA_NETWORK_TESTS") == "" {
		t.Skip("set EKTARA_NETWORK_TESTS=1 to run the test that downloads from YouTube")
	}

	ctx := context.Background()
	s, err := NewStreamSession(ctx, "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	// Poll until some audio is on disk, and give up rather than spinning
	// through all the seconds if the downloader died early.
	deadline := time.Now().Add(60 * time.Second)
	first := true
	for {
		time.Sleep(time.Second)
		d := s.DownloadedDuration()
		fmt.Printf("downloaded=%v  err=%v\n", d, s.Err())

		if err := s.Err(); err != nil {
			t.Fatalf("download failed: %v", err)
		}

		// The first reading should be well under the full track length. If it's
		// already near the full length while almost no bytes have been written,
		// measureDuration regressed to reading the header's planned duration.
		if first {
			first = false
			if d > 30*time.Second {
				t.Fatalf("first reading was %v — measureDuration is reading the header, not the packets", d)
			}
		}

		if d > 5*time.Second {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for audio to land on disk")
		}
	}
}
