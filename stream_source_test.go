// stream_source_test.go
package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestWaitUntil pins down the question the whole seek warning rests on: when a
// wait for the download ends early, did the target arrive or not.
//
// The answer cannot be reconstructed afterwards. A downloader that has exited
// looks identical whether it exited after writing the target or a second before
// it, and it is usually gone by the time anybody asks — a YouTube track lands
// within seconds. So the value is checked here, on its own, in both directions,
// with a downloader that is faked as the real `done` channel closing.
func TestWaitUntil(t *testing.T) {
	// A minute of a sine wave, so the numbers below are round: half of it
	// measures about 30s and a tenth about 6s. The content is the least
	// interesting audio there is on purpose — nothing here may depend on it.
	dir := t.TempDir()
	full := filepath.Join(dir, "full.webm")
	buildSine(t, full, 60)

	t.Run("target is on disk and the download is still running", func(t *testing.T) {
		// The ordinary case: the audio is there, the downloader is still
		// fetching the rest, and the wait returns at once.
		path := filepath.Join(dir, "half-running.webm")
		copyFirstBytes(t, full, path, 0.5)
		session, stopDownload := testSession(t, path)
		defer stopDownload()

		if got := session.DownloadedDuration(); got < 20*time.Second {
			t.Fatalf("file measures %v, so 20s is not on disk and this case is not testing what it claims", got)
		}

		reached, err := session.WaitUntil(20 * time.Second)
		if err != nil {
			t.Fatalf("WaitUntil: %v", err)
		}
		if !reached {
			t.Fatal("20s is on disk and the downloader is still running, so the wait succeeded and must report reached")
		}
	})

	t.Run("target is on disk and the download has finished", func(t *testing.T) {
		// The case that matters most, and the one a caller cannot get right for
		// itself. Every stream finishes downloading within seconds, so by the
		// time a user presses k the downloader is already gone — for a seek that
		// is about to work perfectly. Anything asking "is the downloader still
		// running?" reports a failure here.
		path := filepath.Join(dir, "full-finished.webm")
		copyFile(t, full, path)
		session, stopDownload := testSession(t, path)
		stopDownload()

		if session.Downloading() {
			t.Fatal("the downloader should have stopped before the wait")
		}
		if got := session.DownloadedDuration(); got < 20*time.Second {
			t.Fatalf("file measures %v, so 20s is not on disk and this case is not testing what it claims", got)
		}

		reached, err := session.WaitUntil(20 * time.Second)
		if err != nil {
			t.Fatalf("WaitUntil: %v", err)
		}
		if !reached {
			t.Fatal("20s is on a finished 60s download, so the wait succeeded. The downloader being gone says nothing about whether the target arrived")
		}
	})

	t.Run("target is on disk with under a second to spare", func(t *testing.T) {
		// The slack in WaitUntil is not the bar the caller wants. A file
		// measuring 30s and a seek to 29.9s has the target on disk and plays the
		// last 100ms of it, so the wait succeeded and the user must not be told
		// their seek went past the end. Measuring against target+slack calls
		// this not reached.
		path := filepath.Join(dir, "half-finished.webm")
		copyFirstBytes(t, full, path, 0.5)
		session, stopDownload := testSession(t, path)
		stopDownload()

		have := session.DownloadedDuration()
		target := have - 100*time.Millisecond
		if target <= 0 {
			t.Fatalf("file measures %v, too short to test a target near its end", have)
		}
		// and the margin really is inside the slack, or the case proves nothing
		if have >= target+time.Second {
			t.Fatalf("file measures %v and the target is %v, so this is not inside the slack", have, target)
		}

		reached, err := session.WaitUntil(target)
		if err != nil {
			t.Fatalf("WaitUntil: %v", err)
		}
		if !reached {
			t.Fatalf("the target is %v and %v of audio is on disk, so the seek plays. Reporting not reached here warns about a seek that worked", target, have)
		}
	})

	t.Run("downloader stops before the target arrives", func(t *testing.T) {
		// The other direction, and the one the warning is for: the wait ends
		// because the downloader gave up with the target nowhere near the disk.
		// That is not an error — a user seeking past the end of a track does it
		// constantly — so it is the bool that carries it.
		path := filepath.Join(dir, "tenth.webm")
		copyFirstBytes(t, full, path, 0.1)
		session, stopDownload := testSession(t, path)
		defer stopDownload()

		go func() {
			time.Sleep(200 * time.Millisecond)
			stopDownload()
		}()

		reached, err := session.WaitUntil(50 * time.Second)
		if err != nil {
			t.Fatalf("a download that ended early is not a failure: %v", err)
		}
		if reached {
			t.Fatal("50s of audio was never on disk, so the wait cannot have reached the target")
		}
	})

	t.Run("the last measurement predates the last of the file", func(t *testing.T) {
		// Waiting on a download and being handed the moment it stops is the
		// likeliest moment of all to be holding a stale reading. The cache is
		// half a second long, the file stopped growing, and the reading in hand
		// was taken before the last of the audio landed — so a 20s target looks
		// unreachable when it is sitting on disk right now.
		//
		// This is arranged rather than left to timing, and the guard below
		// proves the arrangement worked: if the cache had been refreshed in
		// between, the case would be testing nothing.
		path := filepath.Join(dir, "just-landed.webm")
		from := copyFirstBytes(t, full, path, 0.1)
		session, stopDownload := testSession(t, path)
		defer stopDownload()

		// a reading taken while only a tenth of the file was there
		session.DownloadedDuration()
		// then the rest of the download lands, all at once
		appendRest(t, full, path, from)

		if got := session.DownloadedDuration(); got >= 20*time.Second {
			t.Fatalf("the cached reading is already %v, so it is not stale and this case is not testing what it claims", got)
		}
		stopDownload()

		reached, err := session.WaitUntil(20 * time.Second)
		if err != nil {
			t.Fatalf("WaitUntil: %v", err)
		}
		if !reached {
			t.Fatal("20s is on disk and the download has stopped, so the wait succeeded. Reading the cache here would call a working seek a failure, on every seek that lands within half a second of the download finishing")
		}
	})

	t.Run("a download that failed stays an error", func(t *testing.T) {
		// Ending early is a normal outcome and is reported by the bool. Breaking
		// is a failure, and has to stay an error, or a dead download would be
		// indistinguishable from a seek past the end of a track that is merely
		// short. play reports those two differently.
		path := filepath.Join(dir, "tenth-failed.webm")
		copyFirstBytes(t, full, path, 0.1)
		session, stopDownload := testSession(t, path)
		session.mu.Lock()
		session.downloadErr = errors.New("yt-dlp: video unavailable")
		session.mu.Unlock()
		stopDownload()

		reached, err := session.WaitUntil(50 * time.Second)
		if err == nil {
			t.Fatal("a failed download returned no error, so the player would show it as a track that finished")
		}
		if reached {
			t.Fatal("50s of audio was never on disk, so the wait cannot have reached the target")
		}
		t.Logf("failed download reported as: %v", err)
	})
}

// TestSeekPastDownloadWarns is the test for the seek warning, and it needs no
// network and no sound card.
//
// Every case goes through the real streamSource.Seek, a real ffmpeg seek and real
// ffprobe measurements, with only the downloader faked. It is the same four
// situations as TestWaitUntil, but at the level of the thing the user sees: a
// notice on the display, or none.
func TestSeekPastDownloadWarns(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.webm")
	buildSine(t, full, 60)

	t.Run("target is on disk and the download is still running", func(t *testing.T) {
		// Half a track on disk, half still to come, and the seek lands inside
		// what has arrived. Nothing to say.
		path := filepath.Join(dir, "half-running.webm")
		copyFirstBytes(t, full, path, 0.5)
		session, stopDownload := testSession(t, path)
		src := testStreamSource(t, session, path)
		defer src.Stop()
		defer stopDownload()

		if !session.Downloading() {
			t.Fatal("the downloader should still be running before the seek")
		}

		pos, err := seekWithin(t, src, at(20), 10*time.Second)
		if err != nil {
			t.Fatalf("seek to 20s: %v", err)
		}
		if msgs := warningsQueued(src); len(msgs) != 0 {
			t.Fatalf("queued %q on a seek to 20s of a 30s download that was still running", msgs)
		}
		// The seek has to have actually moved, or the assertion above would
		// pass for the wrong reason: the position is all the player has to go on.
		if pos != at(20) {
			t.Fatalf("seek returned position %d bytes, wanted %d", pos, at(20))
		}
	})

	t.Run("target is on disk and the download has finished", func(t *testing.T) {
		// A forward seek over a download that finished seconds ago, which is
		// what almost every forward seek is. No notice.
		path := filepath.Join(dir, "full-finished.webm")
		copyFile(t, full, path)
		session, stopDownload := testSession(t, path)
		src := testStreamSource(t, session, path)
		defer src.Stop()
		stopDownload()

		if got := session.DownloadedDuration(); got < 20*time.Second {
			t.Fatalf("file measures %v, so a seek to 20s is on disk and this case is not testing what it claims", got)
		}

		pos, err := seekWithin(t, src, at(20), 10*time.Second)
		if err != nil {
			t.Fatalf("seek to 20s: %v", err)
		}
		if msgs := warningsQueued(src); len(msgs) != 0 {
			t.Fatalf("queued %q on a seek to 20s of a finished 60s download. The downloader being gone says nothing about whether the seek is playable", msgs)
		}
		if pos != at(20) {
			t.Fatalf("seek returned position %d bytes, wanted %d", pos, at(20))
		}
	})

	t.Run("target is past the end of a finished download", func(t *testing.T) {
		// The case the warning exists for: 50s is never going to arrive, ffmpeg
		// will produce nothing at that point, and the track is about to end.
		// Without the notice it is silence after a jump, unexplained.
		path := filepath.Join(dir, "half-finished.webm")
		copyFirstBytes(t, full, path, 0.5)
		session, stopDownload := testSession(t, path)
		src := testStreamSource(t, session, path)
		defer src.Stop()
		stopDownload()

		if got := session.DownloadedDuration(); got > 45*time.Second {
			t.Fatalf("file measures %v, so 50s is not past its end and this case is not testing what it claims", got)
		}

		if _, err := seekWithin(t, src, at(50), 10*time.Second); err != nil {
			// A seek past the end is meant to succeed: ffmpeg produces nothing,
			// the pipe closes, and the player sees a clean end of track. An
			// error here would mean the seek failed rather than landing on
			// silence, which is a different bug and still worth knowing about.
			t.Fatalf("seek to 50s of a 30s download: %v", err)
		}

		// Exactly one. The buffer holds a single message, so a second warning
		// before the display read the first would be dropped rather than
		// queued, and queueing several would mean something is warning on a
		// loop.
		msgs := warningsQueued(src)
		if len(msgs) != 1 {
			t.Fatalf("queued %d messages (%q) for a single seek past the end, wanted 1 — the user gets a jump and silence with no explanation", len(msgs), msgs)
		}
		// The text is not pinned. What it has to be is not empty, and saying
		// why is left to whoever writes it — a test that fails over a word is a
		// test that gets deleted rather than fixed.
		t.Logf("warned: %q", msgs[0])
	})

	t.Run("waits for the download to catch up", func(t *testing.T) {
		// The important one. A tenth of the track on disk, the seek asks for
		// 20s, and the rest of the download lands a moment later. The wait
		// ends because the download caught up, which is the other way a wait
		// can end, and nothing is said about it.
		const downloadDelay = time.Second
		path := filepath.Join(dir, "growing.webm")
		from := copyFirstBytes(t, full, path, 0.1)
		session, stopDownload := testSession(t, path)
		src := testStreamSource(t, session, path)
		defer src.Stop()
		defer stopDownload()

		if got := session.DownloadedDuration(); got > 15*time.Second {
			t.Fatalf("file measures %v, so 20s is already on disk and the seek would not have to wait", got)
		}

		go appendAfter(t, full, path, from, downloadDelay)

		start := time.Now()
		pos, err := seekWithin(t, src, at(20), 30*time.Second)
		waited := time.Since(start)
		if err != nil {
			t.Fatalf("seek to 20s while the download caught up: %v", err)
		}

		// It has to have waited. A seek that returned before the rest of the
		// download landed would mean the wait is not happening at all, and the
		// no-warning assertion below would be passing because nothing was ever
		// downloaded. Half the delay rather than all of it, because a loaded
		// machine can make the first ffprobe slow enough that the seek sees the
		// finished file straight away, and that is not a failure of the code.
		if waited < downloadDelay/2 {
			t.Fatalf("seek returned in %v, well before the rest of the download was on disk — it did not wait", waited)
		}
		t.Logf("waited %v for the download to catch up", waited.Round(10*time.Millisecond))

		if msgs := warningsQueued(src); len(msgs) != 0 {
			t.Fatalf("queued %q on a seek that waited for the download and then played from the target", msgs)
		}
		if pos != at(20) {
			t.Fatalf("seek returned position %d bytes, wanted %d", pos, at(20))
		}
	})
}

// TestWarnDropsTheSecondMessage pins the buffering, which is the only thing
// standing between a warning and a stalled player.
//
// Seek runs on oto's goroutine. If a full buffer made warn wait for the display
// to catch up, then a source warning twice before a tick would stop the audio
// goroutine, and a stop nobody is waiting for is a program that hangs. The
// second call below must therefore return rather than block; if the buffer were
// the wrong size this test would hang rather than fail, which the test timeout
// would also catch.
func TestWarnDropsTheSecondMessage(t *testing.T) {
	src := &streamSource{warnings: make(chan string, 1)}

	src.warn("first")
	src.warn("second")

	msgs := warningsQueued(src)
	if len(msgs) != 1 {
		t.Fatalf("queued %d messages, wanted 1 — the buffer has to drop the second so a warn can never block", len(msgs))
	}
	if msgs[0] != "first" {
		t.Fatalf("kept %q, wanted the first message: a full buffer means the older one is still unread", msgs[0])
	}
}

// warningsQueued returns everything Seek has queued for the player, reading
// without waiting, which is the read play does.
func warningsQueued(s *streamSource) []string {
	var got []string
	for {
		select {
		case msg := <-s.Warnings():
			got = append(got, msg)
		default:
			return got
		}
	}
}

// at is a playing time as the byte offset a seek takes, so the tests read in
// seconds and nothing has to remember the bytes-per-frame arithmetic.
func at(seconds int) int64 {
	return int64(seconds) * int64(defaultSampleRate) * bytesPerFrame
}

// seekWithin calls Seek on a goroutine of its own and fails the test if it does
// not come back. A wait that never ends is a failure to report, not a reason to
// hang until the test timeout.
func seekWithin(t *testing.T, s *streamSource, offset int64, limit time.Duration) (int64, error) {
	t.Helper()
	type result struct {
		pos int64
		err error
	}
	got := make(chan result, 1)
	go func() {
		pos, err := s.Seek(offset, io.SeekStart)
		got <- result{pos: pos, err: err}
	}()
	select {
	case r := <-got:
		return r.pos, r.err
	case <-time.After(limit):
		t.Fatalf("Seek did not return within %v", limit)
		return 0, nil
	}
}

// buildSine writes a sine wave of the given length as opus in a webm, which is
// the shape a stream is: the same container yt-dlp writes, so measureDuration
// and ffmpeg both see what they would see in the real thing.
func buildSine(t *testing.T, path string, seconds int) {
	t.Helper()
	build := exec.Command("ffmpeg",
		"-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(seconds),
		"-c:a", "libopus", "-b:a", "48k",
		"-y", path,
	)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
}

// testSession builds a StreamSession around a file the test wrote, with the
// downloader under the test's control.
//
// The downloader is the one part of a session that cannot be faked with a
// number, because what matters is when its done channel closes: WaitUntil wakes
// on it, Downloading reports it, and Stop blocks on it. The returned function is
// that close, and it is also what StreamSession.Stop calls to cancel, so a test
// that finishes the download early and a test that shuts down at the end are
// doing the same thing the real thing does.
//
// Every other field is the real one, so DownloadedDuration still runs ffprobe
// against the real file and the measurement under test is a real measurement.
func testSession(t *testing.T, path string) (*StreamSession, func()) {
	t.Helper()
	done := make(chan struct{})
	// Stop is safe to call more than once, so closing the channel has to be too
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }

	return &StreamSession{
		path:   path,
		cancel: finish,
		done:   done,
	}, finish
}

// testStreamSource builds the whole wrapper around a session, the way openStream
// does, so the Seek under test is the real Seek and not a call that set itself
// up to pass.
//
// The inner source is built here rather than through newFFmpegSource because
// that opens the audio device, and nothing in the warning path is about audio.
// The device is the only thing it adds: the rate it would have worked out is
// the constant the whole program uses anyway.
func testStreamSource(t *testing.T, session *StreamSession, path string) *streamSource {
	t.Helper()
	inner := &ffmpegSource{
		ctx:  context.Background(),
		rate: defaultSampleRate,
		resolve: func(context.Context) (string, time.Duration, error) {
			return path, session.TrackDuration(), nil
		},
	}
	if err := inner.start(0); err != nil {
		t.Fatalf("start ffmpeg: %v", err)
	}
	return &streamSource{session: session, inner: inner, warnings: make(chan string, 1)}
}

// copyFirstBytes writes the first frac of full to path and says how many bytes
// that was, so the remainder can be appended later without the file being
// replaced under a reader that is measuring it.
func copyFirstBytes(t *testing.T, full, path string, frac float64) int {
	t.Helper()
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	n := int(float64(len(data)) * frac)
	if err := os.WriteFile(path, data[:n], 0o644); err != nil {
		t.Fatal(err)
	}
	return n
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// appendAfter writes the rest of the download in after a delay, the way a
// downloader catching up to a seek looks from the reader's side: the file only
// ever grows, and nothing is ever rewritten in place.
func appendAfter(t *testing.T, full, path string, from int, delay time.Duration) {
	t.Helper()
	time.Sleep(delay)
	appendRest(t, full, path, from)
}

// appendRest adds the rest of full to path at the end, which is the only way a
// download grows: nothing here is ever rewritten in place, so a reader measuring
// the file is never looking at a file that is being replaced.
func appendRest(t *testing.T, full, path string, from int) {
	t.Helper()
	data, err := os.ReadFile(full)
	if err != nil {
		t.Error(err)
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Error(err)
		return
	}
	defer f.Close()
	if _, err := f.Write(data[from:]); err != nil {
		t.Error(err)
	}
}
