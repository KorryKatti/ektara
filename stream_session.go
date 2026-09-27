package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	//"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/go-ytdlp"
)

// ponytail: a fixed dir in /tmp so stale files can be found by hand. os.MkdirTemp
// would be tidier but leaves the tracks to be cleaned up by the OS instead.
const streamDir = "/tmp/ektara-streams"

// stream session downloads a youtube stream into a growing temp file
// instead of playe reading from youtube directly ( causues issue with seek ) a background downloader writes the audio to disk . theplayer then reads the file which is a normal file that can be seeked freely. the downloader keeps writing in bg so seeking has to never touch network

type StreamSession struct {
	pageURL string
	path    string
	//cancel stops the downlode , its the context's cancel function acallling
	// it makes exec.CommandContext kill child proceess
	cancel context.CancelFunc

	// done is closed when the downloader process has exited , whether it finished or was killed. used by wait and by stop to know when it safe to delete the file
	done chan struct{}
	// struct empty used to get 0 bytes idk
	// mu guards teh fields beblow , which are written by downloader
	// goroutine and read by player goroutine
	mu sync.Mutex
	// downloadErr is non nil if downloader fialed , a killed downloader is not an error
	downloadErr error
	// cached duration is the last duration we measured with ffprobe. caching cause ffprobe is a whole process and we dont want to spwan one every the UI asks
	cachedDuration time.Duration
	// measureAt is when cachedDuration was taken , so we know when to refresh it
	measuredAt time.Time
}

// starts downloading pageUrl into a fresh temp file and returns immediately / file grows in bg
// ctx is parent cotext , cancelling it ro callling stop kills the downloader  . teh sesion gets its own child context so Stop can cancel just this sesoin without touching the caller's context.
func NewStreamSession(ctx context.Context, pageURL string) (*StreamSession, error) {
	if err := os.MkdirAll(streamDir, 0o755); err != nil {
		return nil, fmt.Errorf("create stream dir: %w", err)
	}
	// yt-dlp is not necessarily on PATH, so go-ytdlp resolves it from its own
	// cache and downloads it if it is not there yet. Everything else in the
	// program does the same thing through ytdlp.MustInstall
	if _, err := ytdlp.Install(ctx, nil); err != nil {
		return nil, fmt.Errorf("install yt-dlp: %w", err)
	}
	//give this session its own cancel function. When stop is called only this session's downloader dies , caller ctx is left alone
	sessionCtx, cancel := context.WithCancel(ctx)
	// temp file path
	// we use yt-dlp default extension
	// creat temp creastes the file and returns and open handle we dont want the handle (yt-dlp wil lwrite to the path ) so we close and remov e it immediately then hand yt-dlp the path , gurantess name is unique and not realdy taken
	f, err := os.CreateTemp(streamDir, "track-*.webm")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create temp file : %w", err)
	}
	path := f.Name()
	f.Close()
	// remove the empty place holder so yt-dlp can create file itself
	// yt-dlp will refuse to overwrite an existing file otherwise
	if err := os.Remove(path); err != nil {
		cancel()
		return nil, fmt.Errorf("clear temp file: %w", err)
	}
	s := &StreamSession{
		pageURL: pageURL,
		path:    path,
		cancel:  cancel,
		done:    make(chan struct{}), // i still dont get channels
	}

	// start downloader in a gouroutine so NewStreamSession can return righr away. goroutine clsoes s.done when it exits
	go s.download(sessionCtx)
	return s, nil
}

func (s *StreamSession) download(ctx context.Context) {
	defer close(s.done)

	res, err := ytdlp.New().
		Format("bestaudio").
		Output(s.path).
		NoPlaylist().
		NoPart().
		Quiet().
		Run(ctx, s.pageURL)

	// a cancelled contex is not a failure
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		msg := ""
		if res != nil {
			msg = strings.TrimSpace(res.Stderr)
		}
		if msg == "" {
			msg = err.Error()
		}
		s.mu.Lock()
		s.downloadErr = fmt.Errorf("yt-dlp: %s", msg)
		s.mu.Unlock()
	}
}

// file mayb be empty for a moment but valid as soon as NewStreamSession returns
func (s *StreamSession) Path() string {
	return s.path
}

// err returns downloaders err or nil if its still running of finished cleanly
func (s *StreamSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.downloadErr
}

// wait blocks until downloader has exited , returns downloader's error
func (s *StreamSession) Wait() error {
	<-s.done
	return s.Err()
}

// downloaded ruation reports how much audio is currently on disk. return 0 if file empty or ffprobe is stupid ( incomplete webm with no duration header )
// result cached for half a second , ffprobbe is whole process , UI asks this every 100ms without cache maybe not so good things
func (s *StreamSession) DownloadedDuration() time.Duration {
	s.mu.Lock()
	// cache is fresh ?. return
	if time.Since(s.measuredAt) < 500*time.Millisecond {
		d := s.cachedDuration
		s.mu.Unlock()
		return d
	}
	s.mu.Unlock() // tbhi i think we are overdoing but it is what it is
	// Measure outside the lock: ffprobe is slow, and we don't want to hold
	// the mutex while it runs. Two goroutines racing here is fine they'll
	// both measure and the later one wins.
	d := measureDuration(s.path)

	s.mu.Lock()
	s.cachedDuration = d
	s.measuredAt = time.Now()
	s.mu.Unlock()

	return d
}

// measureDuration reports how far into the track the data on disk actually
// reaches.
//
// It does NOT use "format=duration". ffmpeg writes a webm header at the start
// of the file declaring the total intended length of the stream, so
// format=duration reports the full track length within the first megabyte,
// long before that much audio exists. A seek bound taken from that would let
// the player jump past the real data and read zeros.
//
// The honest number is the timestamp of the last packet actually written.
// That is what "how much is downloaded" means.
//
// Cost: ffprobe has to scan the packet index, which on a growing webm means
// reading through the file. For typical track sizes this is tens of
// milliseconds. The caller caches for 500ms, so we don't do it on every UI
// tick.
func measureDuration(path string) time.Duration {
	// -select_streams a:0     first audio stream only
	// -show_entries packet=pts_time   one line per packet, just the timestamp
	// -of default=...:nokey=1   plain values, one per line, no field names
	//
	// Not csv=p=0, which looks equivalent and is not. The first and last
	// packet of a webm carry an empty side_data_list, and the csv writer emits
	// a trailing comma for it — "213.034000,". ParseFloat rejects that, so
	// the reading silently becomes 0, and it becomes 0 on the complete file,
	// which is the one case where the number is most wanted.
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "packet=pts_time",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		// ffprobe fails on an empty or truncated file. Expected while the
		// download is starting, so not worth logging.
		return 0
	}

	// One timestamp per line, in order. We want the last one — that's how far
	// the audio actually goes. Skip empty lines; ffprobe sometimes emits them.
	var last string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			last = line
		}
	}
	if last == "" {
		return 0
	}

	seconds, err := strconv.ParseFloat(last, 64)
	if err != nil {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// Stop kills the downloader and deletes the temp file. Safe to call more than
// once. Blocks until the downloader has actually exited, so the file is not
// deleted out from under a still-writing process.
func (s *StreamSession) Stop() {
	// Signal the downloader to stop. exec.CommandContext turns this into a
	// kill signal to the child.
	s.cancel()
	// Wait for the download goroutine to finish before deleting. Otherwise
	// yt-dlp could still be mid-write when we remove the file.
	<-s.done
	// Best-effort removal. If it's already gone, that's fine.
	_ = os.Remove(s.path)
}
