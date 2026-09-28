package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/go-ytdlp"
)

// a fixed dir in /tmp so stale files can be found by hand. os.MkdirTemp
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

	// trackDuration is how long the whole track is, once the download's own
	// header has been read. It never changes, so it is measured until it is
	// known and then kept.
	trackDuration time.Duration

	// seenSize is how big the file was the last time Grown looked at it
	seenSize int64
}

// starts downloading pageUrl into a fresh temp file and returns immediately / file grows in bg
// ctx is parent cotext , cancelling it ro callling stop kills the downloader  . teh sesion gets its own child context so Stop can cancel just this sesoin without touching the caller's context.
func NewStreamSession(ctx context.Context, pageURL string) (*StreamSession, error) {
	if err := os.MkdirAll(streamDir, 0o755); err != nil {
		return nil, fmt.Errorf("create stream dir: %w", err)
	}
	// Clear out anything left behind by a previous process that did not exit
	// cleanly. An hour is far longer than any live download can last, so this
	// cannot step on one.
	sweepStreamDir(time.Hour)
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
//
// This is the number for seek bounds: how far there is real audio to seek to.
// How long the whole track is, which the progress bar wants instead, is
// TrackDuration.
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

// finalDuration is DownloadedDuration for a downloader that has exited.
//
// The cache exists because ffprobe is a whole process and the display asks how
// much has downloaded ten times a second. That reasoning only holds while the
// file is still growing, where a reading a moment old is close enough. Once the
// downloader has exited the file is never going to change again, so "close
// enough" is no longer good enough: a cached reading can be low by the last few
// hundred milliseconds of audio, and that is exactly where a seek that has just
// arrived lands. Waiting on a download and asking the moment it stops is the
// most likely moment to be handed a stale number there is.
//
// So this drops the cache and measures the finished file. It costs one ffprobe,
// on a path that has already spent a second or more waiting.
func (s *StreamSession) finalDuration() time.Duration {
	s.mu.Lock()
	// the zero time is far enough in the past that DownloadedDuration's freshness
	// check fails, which is all that is needed to make it measure again
	s.measuredAt = time.Time{}
	s.mu.Unlock()

	return s.DownloadedDuration()
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

// measureHeaderDuration is how long the track will be, read from the header the
// downloader writes before any of the audio.
//
// It is the opposite question to measureDuration, so it is the opposite query.
// The webm header carries the stream's full planned length from the first
// kilobyte onwards, which is what the progress bar's total wants: a total that
// stays put while the bar fills. The packets only say how far the audio has got,
// which is what a seek bound wants.
//
// A file with nothing in it yet has no answer rather than a failure, so the
// error is dropped and the answer is 0. That is the same thing localDuration
// does with a file whose length it cannot parse, for the same reason: not
// knowing the length costs the progress bar its total and nothing else.
func measureHeaderDuration(path string) time.Duration {
	d, err := localDuration(path)
	if err != nil {
		return 0
	}
	return d
}

// TrackDuration is how long the track will be, or 0 while the download has not
// written a header that says.
//
// The header declares the same total for the whole download, so this measures
// only until it has a number and then keeps it. That matters because the
// progress bar asks every 100ms, and a fresh reading each time would spawn
// ffprobe ten times a second for the whole track.
func (s *StreamSession) TrackDuration() time.Duration {
	s.mu.Lock()
	if s.trackDuration > 0 {
		d := s.trackDuration
		s.mu.Unlock()
		return d
	}
	s.mu.Unlock()

	// Not known yet, which for the first few seconds means the file is empty
	// while yt-dlp works out what to download.
	d := measureHeaderDuration(s.path)

	s.mu.Lock()
	s.trackDuration = d
	s.mu.Unlock()

	return d
}

// Downloading reports whether the downloader is still running.
//
// It is half of the answer to "is there more of this track?": the other half is
// Grown. A downloader that has run out has nothing more to give, so a reader
// that has everything it downloaded can call the track finished. See
// streamSource.Read, which is the reader that has to get this right.
func (s *StreamSession) Downloading() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// Grown reports whether the downloader has written anything since this was last
// asked, and remembers how big the file is now.
//
// This is how a reader of the file knows to go back for more. It asks about the
// size rather than about the audio because a stat costs nothing and ffprobe
// costs a process: this gets asked every time a reader reaches the end of the
// file, which is every time a download catches up with playback.
//
// Asking about size also means the answer cannot be out of date. A cached
// duration is a reading taken up to half a second ago, and a download that
// finished inside that half second looks like one that finished with the track
// part played.
func (s *StreamSession) Grown() bool {
	// A file that is not there yet has no size, which is the same answer as a
	// file with nothing in it: nothing has arrived.
	size := int64(0)
	if info, err := os.Stat(s.path); err == nil {
		size = info.Size()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if size <= s.seenSize {
		return false
	}
	s.seenSize = size
	return true
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

// WaitUntil blocks until the downloaded audio reaches d, or the downloader
// stops, whichever happens first.
//
// reached says which of the two it was, and it is the whole point of the return
// value: the caller cannot work it out for itself. A downloader that has exited
// looks the same whether it exited after writing the target or a second before
// it, and by the time the caller has control the downloader is usually gone
// either way — a YouTube track lands within seconds, so "is it still running"
// is false on nearly every seek and says nothing at all. Asking the function
// that watched the loop exit is the only way to tell the two apart.
//
// So: reached is true when the target is on disk, and false when the wait ended
// because nothing more is coming and the target is therefore never going to be.
// Only this function knows which, because only this function watched.
//
// The error is for a download that failed, not for a download that ended early.
// Ending early is a normal outcome, and a seek past the end of a track is a
// thing a user does constantly, so it is reported by the bool rather than as a
// failure.
func (s *StreamSession) WaitUntil(d time.Duration) (bool, error) {
	// one second slacking , last packet timestamp is boundary of what we have , ffmpeg -ss d wants audio at or after d
	// a second past the edge is enough for next packet to land
	const slack = time.Second
	want := d + slack

	// poll interval in ns , doubling upto a cap
	interval := 250 * time.Millisecond
	const maxInterval = 2 * time.Second
	const fastPolls = 8
	polls := 0

	for {
		// Downloader stopped. The target may well be sitting on disk anyway:
		// the downloader exiting is not the same as the file being short, and a
		// download that finishes during the wait succeeded. So this measures
		// rather than assuming.
		//
		// Compared against d and not against want. The slack is there so ffmpeg
		// is handed a whole packet at the position it was asked for, which is
		// an encoding detail of the seek and not the question the caller is
		// asking. A seek to a point 400ms before the end of what downloaded
		// plays those 400ms, and calling that "not reached" would be telling
		// the user their seek went past the end when it plainly did not.
		select {
		case <-s.done:
			return s.finalDuration() >= d, s.Err()
		default:
		}
		if s.DownloadedDuration() >= want {
			return true, nil
		}

		// sleep but wake early if downloader stops
		select {
		case <-s.done:
			return s.finalDuration() >= d, s.Err()
		case <-time.After(interval):
		}
		// back off after past polls , doublinf from 250ms to 2s then capped. cap matters cause a slow track shoudnkt turn int a o 10 second poll , download can still finish at any moment and we want to notice prompty aahahaha
		polls++
		if polls >= fastPolls && interval < maxInterval {
			interval *= 2
			if interval > maxInterval {
				interval = maxInterval
			}
		}
	}
}

// sweepStreamDir removes any file in streamDir older than maxAge.
//
// Called at the start of every NewStreamSession. The temp files are supposed to
// be removed by Stop, but a crash, a SIGKILL or a panic leaves them behind, and
// a program that gets killed often enough fills the disk.
//
// The age check, rather than deleting everything, is what keeps two Ektara
// processes from stepping on each other. A file sitting for an hour is not
// being written by anything still alive; a file a minute old very well might
// be.
//
// Errors are logged, not returned: a sweep that cannot do its job should not
// stop a session from starting.
func sweepStreamDir(maxAge time.Duration) {
	entries, err := os.ReadDir(streamDir)
	if err != nil {
		// The directory does not exist, or is not readable. Either way the
		// session that follows fails with a clearer error than this would be.
		return
	}

	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			// Nothing in streamDir should be a directory. If one is there it
			// is not ours, so leave it alone.
			continue
		}

		// Info is the only way to get the mod time from a ReadDir entry.
		info, err := entry.Info()
		if err != nil {
			// The file went away between ReadDir and Info. Nothing to do.
			continue
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(streamDir, info.Name())); err != nil {
				log.Printf("sweep %s: %v", info.Name(), err)
			}
		}
	}
}
