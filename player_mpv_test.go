package main

import (
	"os"
	"testing"
	"time"
)

// Plays a real file through the real player and the real sound device, so it is
// the only test that can say the audio path works at all. Skipped when
// EKTARA_AUDIO_TESTS is unset, because a machine with no sound server would
// otherwise fail it for reasons that have nothing to do with the code.
//
// Skipped rather than made to pass quietly: a test skipped for everyone is a test
// nobody is running.
func audioTestEnabled() bool {
	return os.Getenv("EKTARA_AUDIO_TESTS") != ""
}

// Polls until condition holds or the deadline passes: mpv works on its own threads,
// so everything here waits for a fact to become true rather than commanding it.
func waitFor(t *testing.T, what string, within time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", within, what)
}

// TestPlayerPlaysALocalFile covers the whole path a local file takes: it is
// loaded, the position moves because something is actually coming out of the
// device, a seek moves it, and the end of the file arrives as a clean finish
// rather than an error.
func TestPlayerPlaysALocalFile(t *testing.T) {
	if !audioTestEnabled() {
		t.Skip("set EKTARA_AUDIO_TESTS=1 to run the tests that need a sound device")
	}

	audio, err := newPlayer()
	if err != nil {
		t.Fatalf("newPlayer: %v", err)
	}
	defer audio.Close()

	const file = "/tmp/opencode/ektest/tone8.mp3"
	if _, err := os.Stat(file); err != nil {
		t.Skipf("no test file to play: %v", err)
	}

	if err := audio.Start(file); err != nil {
		t.Fatalf("start: %v", err)
	}
	audio.Play()

	// the length is known as soon as the file is loaded, without any ffprobe
	// having been asked
	waitFor(t, "the duration to be known", 5*time.Second, func() bool {
		return audio.Length() > 0
	})
	if got := audio.Length().Seconds(); got < 7 || got > 9 {
		t.Errorf("length = %.2fs, want about 8", got)
	}

	// the position has to actually advance, which is the difference between
	// "the file loaded" and "sound is coming out"
	waitFor(t, "the position to move", 5*time.Second, func() bool {
		return audio.Position() > 300*time.Millisecond
	})

	// a seek used to kill ffmpeg and start a new one. It is a command now, so
	// what matters is that it lands and does not report an error.
	before := audio.Position()
	audio.SeekBy(3 * time.Second)
	waitFor(t, "the seek to land", 3*time.Second, func() bool {
		return audio.Position() > before+2*time.Second
	})
	if err := audio.Err(); err != nil {
		t.Fatalf("Err after a seek: %v", err)
	}

	// and it runs to the end and says so cleanly, with no error, because a
	// clean end is what earns a track its place in the history
	waitFor(t, "the track to end", 15*time.Second, func() bool {
		return !audio.Playing()
	})
	if err := audio.Err(); err != nil {
		t.Errorf("a track that played to the end reported %v, want no error", err)
	}
}

// TestStreamURLResolvesAndPlays covers the half that used to be a temporary
// file: a YouTube video is turned into a direct url, handed to mpv, and audio
// comes out. This is the test that says the temp-file machinery really is
// gone and not just been moved somewhere else.
//
// It needs the network and it needs YouTube to answer, so it is behind
// EKTARA_NETWORK_TESTS on top of the sound device.
func TestStreamURLResolvesAndPlays(t *testing.T) {
	if !audioTestEnabled() {
		t.Skip("set EKTARA_AUDIO_TESTS=1 to run the tests that need a sound device")
	}
	if os.Getenv("EKTARA_NETWORK_TESTS") == "" {
		t.Skip("set EKTARA_NETWORK_TESTS=1 as well to run the tests that go to YouTube")
	}

	// Rick Astley, never gonna give you up. Small, unremarkable and permanently
	// available, which is what a test wants from the internet.
	const pageURL = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

	url, err := streamURL(pageURL)
	if err != nil {
		t.Fatalf("streamURL: %v", err)
	}
	if url == "" {
		t.Fatal("streamURL returned nothing")
	}
	t.Logf("resolved to %s...", url[:min(len(url), 60)])

	audio, err := newPlayer()
	if err != nil {
		t.Fatalf("newPlayer: %v", err)
	}
	defer audio.Close()

	if err := audio.Start(url); err != nil {
		t.Fatalf("start: %v", err)
	}
	audio.Play()

	// a stream has to open its own connection, so this is slower than a file
	waitFor(t, "the stream to start moving", 30*time.Second, func() bool {
		return audio.Position() > time.Second
	})
	if err := audio.Err(); err != nil {
		t.Errorf("the stream reported %v", err)
	}
}
func TestPlayerSurvivesStopAndRestart(t *testing.T) {
	if !audioTestEnabled() {
		t.Skip("set EKTARA_AUDIO_TESTS=1 to run the tests that need a sound device")
	}

	audio, err := newPlayer()
	if err != nil {
		t.Fatalf("newPlayer: %v", err)
	}
	defer audio.Close()

	const file = "/tmp/opencode/ektest/tone8.mp3"
	if _, err := os.Stat(file); err != nil {
		t.Skipf("no test file to play: %v", err)
	}

	if err := audio.Start(file); err != nil {
		t.Fatalf("start: %v", err)
	}
	audio.Play()
	waitFor(t, "the first track to be going", 5*time.Second, func() bool {
		return audio.Position() > 100*time.Millisecond
	})

	audio.Stop()
	if audio.Playing() {
		t.Error("the player still reports a stopped track as going")
	}

	// a second load onto the same instance has to clear the ended flag, or the
	// tick would skip the new track the moment it landed
	if err := audio.Start(file); err != nil {
		t.Fatalf("second start: %v", err)
	}

	// Stop made mpv emit an end event, and it arrives on the event loop at some
	// point afterwards rather than at the moment Stop returned. If that event
	// were treated as a track finishing it would land on this new track and
	// mark it over before it had played anything, so the queue would walk
	// itself off the end one skip at a time.
	//
	// The event is on its way by now, so this is checking that it was ignored
	// rather than that it has not arrived. It used to fail here perhaps one run
	// in three, which is the worst way for a bug to show up.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !audio.Playing() {
			t.Fatal("a freshly loaded track was marked as finished: the end event from Stop was applied to it")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := audio.Err(); err != nil {
		t.Errorf("a freshly loaded track is carrying an error: %v", err)
	}
}
