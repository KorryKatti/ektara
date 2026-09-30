package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gen2brain/go-mpv"
	"github.com/lrstanley/go-ytdlp"
)

// audio is what the display is allowed to ask of the player. It exists so the
// tick can be tested without a sound device: the end-of-a-track path is the one
// that most easily loses the clock, and it cannot be reached without something
// that is able to say "this track has finished".
type audio interface {
	// Start points the player at something, paused.
	Start(input string) error
	// Play and Pause, and SetVolume, are what a key press reaches for.
	Play()
	Pause()
	SetVolume(float64)
	// SeekBy moves playback by a delta.
	SeekBy(time.Duration)
	// Playing is whether the track is still going. Err is why it stopped, and
	// is nil for a track that reached its end.
	Playing() bool
	Err() error
	// Position and Length are for the progress bar. Length is 0 when nothing
	// could say, which the display treats as a bar that counts up with no
	// total rather than as a failure.
	Position() time.Duration
	Length() time.Duration
	// Stop ends the current track but keeps the player for the next one.
	Stop()
	// Close is only for the end of the program.
	Close()
}

// player is the audio.
//
// It wraps libmpv, which opens the sound device itself, decodes whatever it is
// pointed at, and seeks without being restarted. None of the pipe, the
// resampling, or the ffmpeg restart have to exist because of that.
//
// There is one player for the whole session rather than one per track. The
// audio device is slow to open and mpv is built to keep it open, so a new track
// is a loadfile onto the same instance and there is no gap of silence between
// two of them.
type player struct {
	handle *mpv.Mpv

	// mu guards everything below. The event loop writes these from mpv's own
	// goroutine and the display reads them from the one running Update, and
	// neither is allowed to block the other.
	mu sync.Mutex
	// ended is true once the track has run out or broken. The display watches
	// this to know when to move on, which is why it is here rather than read
	// back off mpv's pause flag: pause is also true in the gap while mpv opens
	// the audio device, and reading that as a finished track would skip every
	// stream, because a stream is always still opening when the message saying
	// it loaded arrives.
	ended bool
	// err is what went wrong, if anything. A track that reached its end leaves
	// it nil, and that difference is what separates a song in the history from a
	// failure the user has to be told about.
	err error
	// seekTarget is where a seek was asked to go, held until mpv's own position
	// catches up with it. The display reads the position ten times a second and
	// a seek lands a moment later, so without this the bar jumps to the wrong
	// place and then corrects itself, which looks like a bug.
	seekTarget time.Duration
	// seekUntil is when seekTarget stops being believable. A seek on a slow file
	// can take longer than this, and past it the display falls back to whatever
	// mpv actually says.
	seekUntil time.Time

	// shutting is closed to tell the event loop to stop, and stopped is closed
	// by that loop on its way out. They exist so Close can take the handle apart
	// only once nothing is reading it, which is the whole reason they are here.
	shutting chan struct{}
	stopped  chan struct{}
	// closeOnce keeps Close from being run twice. Destroying a handle that is
	// already destroyed is a crash, and the defers that call it can easily both
	// run: one at the end of a test, one at the end of the program.
	closeOnce sync.Once
}

// newPlayer creates the one player this program will ever have and starts the
// loop that listens to it.
func newPlayer() (audio, error) {
	handle := mpv.New()
	if handle == nil {
		return nil, fmt.Errorf("could not create an mpv instance")
	}

	// vo=null because there is no video and the interface is drawing itself in
	// a terminal. The audio filter graph is deliberately left alone: mpv
	// already matches the sample rate to the device, so a file recorded at
	// 44100 plays at the right speed without anything here converting it, and
	// the device is no longer locked to one rate for the session.
	//
	// terminal=no because mpv would otherwise print its own status lines over the
	// top of the interface and claim the keys this program listens for.
	settings := [][2]string{
		{"vo", "null"},
		{"terminal", "no"},
		{"audio-display", "no"},
		{"idle", "yes"},
		// enough room to ride out a slow moment without dropping out, and small
		// enough that seeking back is not replaying megabytes of already-decoded
		// audio. mpv's own default is 150M, which is far more than a music player
		// needs and makes every seek feel sluggish.
		{"demuxer-max-bytes", "32MiB"},
		{"cache-secs", "30"},
	}
	for _, setting := range settings {
		if err := handle.SetOptionString(setting[0], setting[1]); err != nil {
			handle.Destroy()
			return nil, fmt.Errorf("mpv option %q: %w", setting[0], err)
		}
	}

	if err := handle.Initialize(); err != nil {
		handle.Destroy()
		return nil, fmt.Errorf("mpv: %w", err)
	}

	p := &player{
		handle:   handle,
		shutting: make(chan struct{}),
		stopped:  make(chan struct{}),
	}

	go p.listen()

	return p, nil
}

// listen is mpv's event loop. It runs for the life of the program and does
// nothing but take in the two events worth acting on: a file has loaded, and a
// file has ended.
//
// It updates the player's own fields rather than passing anything along. There
// is no channel and no queue, because a queue would have to choose between
// growing without bound and dropping something, and the one event that must
// never be dropped is the end of a track: lose that and the tick never sees the
// song finish, so it stays in the progress bar forever and the queue never
// moves on. The mutex is the whole of the hand-off. Nothing here touches the
// display; the tick reads these fields when it is ready, which is the only
// goroutine allowed to draw.
func (p *player) listen() {
	// stopped is closed on the way out, so Close knows the handle is no longer
	// being touched before it destroys it
	defer close(p.stopped)

	for {
		event := p.handle.WaitEvent(0)

		// the loop is about to end rather than the next event, which is the
		// only way to tell an ordinary event from the empty one Wakeup sends
		// to unblock this
		select {
		case <-p.shutting:
			return
		default:
		}

		switch event.EventID {
		case mpv.EventFileLoaded:
			// the audio is really going now, so a seek that was promised is
			// either done or never going to happen
			p.mu.Lock()
			p.seekTarget = 0
			p.seekUntil = time.Time{}
			p.mu.Unlock()
		case mpv.EventEnd:
			end := event.EndFile()
			// The reason is the whole of it, and getting it wrong is a bug that
			// only shows up now and then.
			//
			// A track that played out reports EOF. A track that failed reports
			// ERROR. Stopping one ourselves reports STOP, and that is the one
			// that has to be ignored: Stop is called on every track change, the
			// event arrives on this goroutine some time afterwards, and if it
			// were treated as an end it would land on whatever track came next
			// and mark that finished before it had played a note. The next
			// track would be skipped, and the one after it, until the queue ran
			// out. Treating STOP as nothing is not a shortcut, it is the only
			// correct reading: nobody asked for a track to finish when they
			// pressed next.
			p.mu.Lock()
			switch end.Reason {
			case mpv.EndFileEOF:
				p.ended = true
			case mpv.EndFileError:
				p.ended = true
				p.err = end.Error
			}
			p.mu.Unlock()
		}
	}
}

// Start points mpv at something and leaves it paused, so the caller decides when
// sound starts.
//
// Arriving paused is the point. Resolving a stream is a second of network
// happening on its own goroutine, and someone who picks a track and then
// presses p to get on with something else should not be interrupted by the
// audio of the track they already moved past. The caller unpauses with Play
// once the model has caught up with the message.
//
// It has to be done before the loadfile, not after. mpv's default is to play
// what it loads, so a loadfile onto an unpaused instance starts coming out of
// the speakers before Start has even returned, and the caller has no chance to
// object. The keys that would pause are ignored while a track is loading, so
// nothing is lost by holding it: there is no way for the user to have asked
// for silence during this window.
func (p *player) Start(input string) error {
	p.mu.Lock()
	p.ended = false
	p.err = nil
	p.seekTarget = 0
	p.seekUntil = time.Time{}
	p.mu.Unlock()

	_ = p.handle.SetProperty("pause", mpv.FormatFlag, true)

	if err := p.handle.Command([]string{"loadfile", input, "replace"}); err != nil {
		p.setBroken(err)
		return err
	}
	return nil
}

func (p *player) setBroken(err error) {
	p.mu.Lock()
	p.ended = true
	p.err = err
	p.mu.Unlock()
}

func (p *player) Play() {
	_ = p.handle.SetProperty("pause", mpv.FormatFlag, false)
}

func (p *player) Pause() {
	_ = p.handle.SetProperty("pause", mpv.FormatFlag, true)
}

// Playing is whether this track is still going. The display combines it with
// its own paused flag, so this is the other half: not finished and not broken.
func (p *player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.ended
}

// Err is what went wrong, or nil for a track that finished or is still going.
func (p *player) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Position is how far into the track playback is, which mpv knows because it is
// the one doing the playing. Asking it is exact, where counting bytes off a
// pipe was an estimate that had to have the read-ahead subtracted from it.
func (p *player) Position() time.Duration {
	p.mu.Lock()
	target, until := p.seekTarget, p.seekUntil
	p.mu.Unlock()

	// a seek that has not landed yet is promised as the place it is going, so
	// the bar goes where the music is about to be rather than where it was
	if !until.IsZero() && time.Now().Before(until) {
		return target
	}

	seconds, err := p.getFloat("time-pos")
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// Length is how long the whole track is, or 0 when nothing could say. A stream
// can take a moment to work this out, which is why the display treats 0 as
// "the bar counts up without a total" rather than as a failure.
func (p *player) Length() time.Duration {
	seconds, err := p.getFloat("duration")
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func (p *player) getFloat(property string) (float64, error) {
	value, err := p.handle.GetProperty(property, mpv.FormatDouble)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("mpv property %q was not a number", property)
	}
	return number, nil
}

// SeekBy moves playback by delta and promises the display the place it is
// going, so the bar does not read the old position for a moment after the key.
func (p *player) SeekBy(delta time.Duration) {
	// where we are now, read before the lock is taken. Position locks the
	// mutex itself, and a sync.Mutex is not reentrant, so asking for it while
	// holding the lock is a deadlock rather than a slow call.
	current := p.Position()

	_ = p.handle.Command([]string{"seek", seconds(delta), "relative"})

	// mpv clamps a seek past either end itself, so the promise is only a guess
	// for as long as the seek could plausibly take to happen
	p.mu.Lock()
	p.seekTarget = current + delta
	p.seekUntil = time.Now().Add(2 * time.Second)
	p.mu.Unlock()
}

// SetVolume takes the model's 0 to 1 and hands mpv its 0 to 100. Nothing is
// kept here: the model owns the number, and it is the thing that decides what
// it should be, so a second copy would only be able to disagree with it.
func (p *player) SetVolume(volume float64) {
	_ = p.handle.SetProperty("volume", mpv.FormatDouble, volume*100)
}

// Stop ends whatever is playing but keeps the instance, and the open audio
// device, for the next track.
func (p *player) Stop() {
	p.mu.Lock()
	p.ended = true
	p.seekTarget = 0
	p.seekUntil = time.Time{}
	p.mu.Unlock()

	_ = p.handle.Command([]string{"stop"})
}

// Close tears the instance down for good. It is only for the end of the
// program: Stop is what a track change uses.
//
// The order is not negotiable. Destroying an mpv handle frees the memory the
// event loop is parked in, so the loop has to be on its way out first, and
// WaitEvent has to be nudged to notice that, because it blocks with nothing
// left to wait for. Doing it the other way round is a crash or a hang rather
// than a clean exit, and it is the sort of thing that only shows up at the end
// of a session or in the second test of a run.
//
// Calling it twice is harmless.
func (p *player) Close() {
	p.closeOnce.Do(func() {
		close(p.shutting)
		// unblocks the WaitEvent in listen, which then sees shutting and returns
		p.handle.Wakeup()
		<-p.stopped
		p.handle.Destroy()
	})
}

// seconds renders a duration the way mpv's seek command wants it, as a number
// of seconds.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

// ---------------------------------------------------------------------------
// opening a track
// ---------------------------------------------------------------------------

// openTrack points the player at a track and leaves it paused, ready for the
// caller to start.
//
// This is the slow half of playing and the reason it is a tea.Cmd rather than a
// plain call: a stream has to ask yt-dlp to work out a direct media url first,
// which takes a second or two, and the display has to keep repainting while it
// does. A local file is nearly instant, but it goes through here too so there is
// only one path into the player.
func openTrack(a audio, t Track) error {
	input, err := trackInput(t)
	if err != nil {
		return err
	}
	return a.Start(input)
}

// trackInput is what mpv should read for a track: a path in the library folder
// for a local file, or a direct media url for a stream.
//
// A Track carries a bare file name rather than a path, so the library folder is
// worked out here. Doing it at the moment of opening is what lets the whole
// library move without the history in the database going stale.
func trackInput(t Track) (string, error) {
	if t.Stream {
		return streamURL(t.PageURL)
	}
	return musicPath(t.Filename)
}

// streamURL asks yt-dlp to work out a direct link to the audio of a YouTube
// video, without downloading any of it.
//
// The url is the only thing that has to be resolved, because mpv does the rest:
// it opens the http connection, decodes, buffers, and seeks, all from this one
// address. It used to be a temporary file that yt-dlp filled while ffmpeg read
// it, and the code to keep those two in step was the largest part of this
// program. A url expires eventually, but a direct media link is good for hours
// and a track is minutes, so it outlasts the playing by a very long way.
func streamURL(pageURL string) (string, error) {
	if pageURL == "" {
		return "", fmt.Errorf("this track has no url to play from")
	}

	// yt-dlp might not be installed yet, and installing it takes a while, so
	// this happens up front rather than being the user's problem mid-track
	ytdlp.MustInstall(context.TODO(), nil)

	// a stream spends from the same budget as a search, so switching to the
	// stream mode is not a way round the limiter
	youtubeLimiter.wait()

	found, _, err := ytdlp.New().
		Retries("3").
		// bestaudio picks the best audio-only stream. Asking for it by name
		// rather than picking one here is what puts the resolved url in the
		// top-level url field, which is the one thing being asked for.
		Format("bestaudio").
		ExtractInfo(context.TODO(), pageURL)
	if err != nil {
		return "", fmt.Errorf("could not resolve a stream for %s: %w", pageURL, err)
	}
	if len(found) == 0 || found[0] == nil {
		return "", fmt.Errorf("yt-dlp found nothing at %s", pageURL)
	}

	info := found[0]
	if info.URL == nil || *info.URL == "" {
		return "", fmt.Errorf("yt-dlp gave no playable url for %s", pageURL)
	}

	return *info.URL, nil
}
