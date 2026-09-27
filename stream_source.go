package main

import (
	"context"
	"fmt"
	"time"
)

// streamSource adapts a StreamSession to audioSource interface so player can read from a growing downloade file as if it were any other source
// exirs because StreamSession outlives a single read , it owns a downloader process and temp file , while an audioSource is created and destroyed per track. wrappeing keep sthe two lifetimes tied togehter , when player calls stop the session dies with it
// blah blah understanding bullshit

type streamSource struct {
	session *StreamSession
	// inner is the ffmpegSource reading growing file
	inner audioSource
}

// openStream downloads t into a growing temp file and returns a source that
// reads from it.
//
// The session is created first and the ffmpeg player is started on top of it.
// If the player fails to start, the session is stopped so we don't leave a
// downloader running.
func openStream(ctx context.Context, t Track) (audioSource, error) {
	session, err := NewStreamSession(ctx, t.PageURL)
	if err != nil {
		return nil, fmt.Errorf("open stream %q: %w", t.PageURL, err)
	}
	// player reads the growing file , resolve returns the file path and currently downloaded length which the ffmpegSource uses only for its length rpoetrnig , actual seeking handled by ffmpegSource
	// restarting ffmpeg with -ss
	inner, err := newFFmpegSource(ctx, func(ctx context.Context) (string, time.Duration, error) {
		// man i am really struggling with this language here
		return session.Path(), session.DownloadedDuration(), nil
	})
	if err != nil {
		session.Stop()
		return nil, err
	}

	return &streamSource{session: session, inner: inner}, nil
	// yeah i am struggling

}

// read forwards to inner ffmpegSource reading the growing file
func (s *streamSource) Read(p []byte) (int, error) {
	return s.inner.Read(p)
}

// seek forwards to the inner ffmpegSource , for now this can seek past the downlaoded data and produce silence , fuck it
func (s *streamSource) Seek(offset int64, whence int) (int64, error) {
	return s.inner.Seek(offset, whence)
}

func (s *streamSource) Rate() int {
	return s.inner.Rate()
}

func (s *streamSource) Length() time.Duration {
	// Prefer the session's fresh reading over the ffmpegSource's cached one.
	// The ffmpegSource cached its length at start, when the download had just
	// begun; the session keeps measuring.
	if d := s.session.DownloadedDuration(); d > 0 {
		return d
	}
	return s.inner.Length()
}

// Stop shuts down both the player and the downloader. Order matters: stop the
// player first, so it isn't reading the file while the session removes it.
func (s *streamSource) Stop() {
	s.inner.Stop()
	s.session.Stop()
}
