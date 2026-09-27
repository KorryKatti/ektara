package main

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// streamSource plays a YouTube track out of a file that is still being written
// to, so that seeking is a read of the disk rather than a fresh request to
// YouTube for a url that may have expired since.
//
// It exists to tie two lifetimes together. A StreamSession is created and
// destroyed around a whole track, and owns a downloader process and the file
// that process writes. An ffmpegSource is created and destroyed around a single
// stretch of playback, and is recreated on every seek. Neither can be handed to
// the player on its own: the session needs somebody to stop it, and the ffmpeg
// is reading the session's file. So the player is given this, which is both,
// behind the one interface it already knows how to play.
type streamSource struct {
	// session owns the download and the file it is writing
	session *StreamSession
	// inner is the ffmpeg player decoding the growing file
	inner *ffmpegSource

	// stopping is set by Stop, so a Read that is waiting for the download to
	// catch up gives up at once rather than starting an ffmpeg that nothing is
	// left to kill.
	stopping atomic.Bool
}

// openStream starts downloading t into a temp file and returns a source that
// plays out of it.
//
// The file is empty for the first few seconds, while yt-dlp works out what to
// fetch, so this returns before there is any audio in it. Nothing has to wait
// for that here: Read waits for the file rather than reporting a track with
// nothing to play, and the progress bar picks up the total as soon as the
// download's header says what it is.
//
// If the player cannot be started the session is stopped, so a stream that
// never plays does not leave a downloader running.
func openStream(ctx context.Context, t Track) (audioSource, error) {
	session, err := NewStreamSession(ctx, t.PageURL)
	if err != nil {
		return nil, fmt.Errorf("open stream %q: %w", t.PageURL, err)
	}

	// ffmpeg is pointed at the file, and told how long the track is, which it
	// asks for again on every seek because every seek starts a new ffmpeg. The
	// session answers that rather than the file, because the file has no length
	// in it yet.
	inner, err := newFFmpegSource(ctx, func(context.Context) (string, time.Duration, error) {
		return session.Path(), session.TrackDuration(), nil
	})
	if err != nil {
		session.Stop()
		return nil, err
	}

	return &streamSource{session: session, inner: inner}, nil
}

// Read plays the file, and starts ffmpeg again whenever it catches up with the
// download.
//
// ffmpeg reads a file to wherever it currently ends and then stops. It does not
// wait for a file that is still being written to: handed one that is not there
// yet it gives up at once, and handed a part-downloaded one it plays what is
// there, reaches the end, and exits without complaint. Both look exactly like
// the end of a track, and YouTube delivers faster than a track plays, so
// forwarding the end of file as it comes would skip to the next song seconds
// into every stream.
//
// So the end of the file is only the end of the track when the download has
// stopped as well. Until then this waits for the file to grow and picks the
// audio up again from where ffmpeg left off.
func (s *streamSource) Read(p []byte) (int, error) {
	for {
		n, err := s.inner.Read(p)
		if err != io.EOF {
			// audio, or a real failure. Neither is anything Read can do about.
			return n, err
		}

		// shutting down, so there is nothing left to wait for
		if s.stopping.Load() {
			return n, io.EOF
		}

		// a download that failed is a track that broke rather than one that
		// finished, and the player only tells those two apart by the error
		if err := s.session.Err(); err != nil {
			return n, err
		}

		// ffmpeg has everything that has been downloaded so far. Going back for
		// more is only worth it if there is more, and starting ffmpeg when there
		// is not would spawn a process every 100ms for as long as it takes
		// yt-dlp to work out what to download.
		if s.session.Grown() {
			if err := s.inner.restartAtCurrent(); err != nil {
				return n, err
			}
			continue
		}

		// Nothing new on disk, and the downloader has finished, so this really is
		// the end of the track.
		if !s.session.Downloading() {
			return n, io.EOF
		}

		// Still downloading, so more is coming. Wait for it rather than ending
		// the track here.
		time.Sleep(100 * time.Millisecond)
	}
}

// Seek forwards to the inner ffmpegSource, which restarts ffmpeg at the new

func (s *streamSource) Seek(offset int64, whence int) (int64, error) {
	if s.stopping.Load() {
		return 0, fmt.Errorf("stream source stopped")
	}

	// Current position, in bytes. ffmpegSource.Seek(0, SeekCurrent) just
	// returns f.pos.Load(), so this is a cheap atomic read, no restart.
	cur, err := s.inner.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}

	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = cur + offset
	default:
		return 0, fmt.Errorf("seek whence %d not supported", whence)
	}
	if abs < 0 {
		abs = 0
	}

	// The 10 Hz path: playedDuration asks where we are, which lands here as
	// Seek(0, SeekCurrent). Return without touching the session.
	if abs == cur {
		return cur, nil
	}

	target := pcmDuration(abs, s.inner.Rate())
	if err := s.session.WaitUntil(target); err != nil {
		return 0, err
	}

	return s.inner.Seek(abs, io.SeekStart)
}

func (s *streamSource) Rate() int {
	return s.inner.Rate()
}

// Length is how long the whole track is, which is what the progress bar's total
// wants.
//
// It is asked of the session rather than taken from the inner source, because
// the inner source measured the file when it was empty and has no reason to look
// at it again. The session keeps measuring until the download's header has been
// read, and then keeps that number: the total appears within the first seconds
// of a stream and does not climb afterwards, so the bar does not appear to move
// backwards as it fills.
func (s *streamSource) Length() time.Duration {
	return s.session.TrackDuration()
}

// Stop kills the player and the downloader, and deletes the file.
//
// The flag comes first, so a Read that is waiting for more data gives up instead
// of starting an ffmpeg nothing is left to kill. The player is stopped before
// the session, so it is not reading the file while the session removes it.
func (s *streamSource) Stop() {
	s.stopping.Store(true)
	s.inner.Stop()
	s.session.Stop()
}
