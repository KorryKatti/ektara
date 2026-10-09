package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gen2brain/go-mpv"
	"github.com/lrstanley/go-ytdlp"
)

// What the display is allowed to ask of the player. Exists so the tick can be
// tested without a sound device: the end-of-a-track path is the one that most
// easily loses the clock, and it cannot be reached without something able to say
// "this track has finished".
type audio interface {
	// Point the player at something, paused.
	Start(input string) error

	// What a key press reaches for.
	Play()
	Pause()
	SetVolume(float64)

	// Move playback by a delta.
	SeekBy(time.Duration)

	// Whether the track is still going, and why it stopped. Err is nil for a track
	// that reached its end, which is what separates a song in the history from a
	// failure the user has to be told about.
	Playing() bool
	Err() error

	// For the progress bar. Length is 0 when nothing could say, which the display
	// treats as a bar counting up with no total rather than as a failure.
	Position() time.Duration
	Length() time.Duration

	// Ends the current track but keeps the player for the next one.
	Stop()

	// Only for the end of the program.
	Close()
}

// The audio: a wrapper around libmpv, which opens the sound device itself, decodes
// whatever it is pointed at, and seeks without being restarted. None of the pipe,
// the resampling or the ffmpeg restart have to exist because of that.
//
// One player for the whole session rather than one per track: the audio device is
// slow to open and mpv is built to keep it open, so a new track is a loadfile onto
// the same instance with no gap of silence between two of them.
type player struct {
	handle *mpv.Mpv

	// Guards everything below. The event loop writes these from mpv's own
	// goroutine and the display reads them from the one running Update, and neither
	// is allowed to block the other.
	mu sync.Mutex

	// True once the track has run out or broken. The display watches this to know
	// when to move on, which is why it is here rather than read back off mpv's
	// pause flag: pause is also true in the gap while mpv opens the audio device,
	// and reading that as a finished track would skip every stream, because a
	// stream is always still opening when the message saying it loaded arrives.
	ended bool

	// What went wrong, if anything.
	err error

	// Where a seek was asked to go, held until mpv's own position catches up. The
	// display reads the position ten times a second and a seek lands a moment
	// later, so without this the bar jumps to the wrong place and then corrects
	// itself, which looks like a bug.
	seekTarget time.Duration

	// When seekTarget stops being believable. A seek on a slow file can take
	// longer than this, and past it the display falls back to what mpv says.
	seekUntil time.Time

	// shutting tells the event loop to stop, stopped is closed by that loop on its
	// way out. They exist so Close can take the handle apart only once nothing is
	// reading it.
	shutting chan struct{}
	stopped  chan struct{}

	// Keeps Close from running twice: destroying an already destroyed handle is a
	// crash, and the defers that call it can easily both run, one at the end of a
	// test and one at the end of the program.
	closeOnce sync.Once
}

// The one player this program will ever have, with the loop listening to it.
func newPlayer() (audio, error) {
	handle := mpv.New()
	if handle == nil {
		return nil, fmt.Errorf("could not create an mpv instance")
	}

	// vo=null because there is no video and the interface draws itself in a
	// terminal. The audio filter graph is deliberately left alone: mpv already
	// matches the sample rate to the device, so a file recorded at 44100 plays at
	// the right speed without anything here converting it, and the device is no
	// longer locked to one rate for the session.
	//
	// terminal=no because mpv would otherwise print its own status lines over the
	// interface and claim the keys this program listens for.
	settings := [][2]string{
		{"vo", "null"},
		{"terminal", "no"},
		{"audio-display", "no"},
		{"idle", "yes"},
		// Enough room to ride out a slow moment without dropping out, and small
		// enough that seeking back is not replaying megabytes of already-decoded
		// audio. mpv's own default is 150M, far more than a music player needs, and
		// it makes every seek feel sluggish.
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

// mpv's event loop: runs for the life of the program and takes in the two events
// worth acting on, a file loaded and a file ended.
//
// It updates the player's own fields rather than passing anything along. There is
// no channel and no queue, because a queue would have to choose between growing
// without bound and dropping something, and the one event that must never be
// dropped is the end of a track: lose that and the tick never sees the song
// finish, so it stays in the progress bar forever and the queue never moves on.
// The mutex is the whole of the hand-off. Nothing here touches the display; the
// tick reads these fields when it is ready, being the only goroutine allowed to
// draw.
func (p *player) listen() {
	// closed on the way out, so Close knows the handle is no longer being touched
	// before it destroys it
	defer close(p.stopped)

	for {
		event := p.handle.WaitEvent(0)

		// the loop is about to end rather than the next event, which is the only
		// way to tell an ordinary event from the empty one Wakeup sends to unblock
		// this
		select {
		case <-p.shutting:
			return
		default:
		}

		switch event.EventID {
		case mpv.EventFileLoaded:
			// the audio is really going now, so a promised seek is either done
			// or never going to happen
			p.mu.Lock()
			p.seekTarget = 0
			p.seekUntil = time.Time{}
			p.mu.Unlock()
		case mpv.EventEnd:
			end := event.EndFile()
			// A track that played out reports EOF, one that failed reports ERROR. Stopping
			// one ourselves reports STOP, and that is the one that has to be
			// ignored: Stop is called on every track change, the event arrives here
			// some time afterwards, and treating it as an end would mark whatever
			// track came next finished before it had played a note, skipping it and
			// the one after that until the queue ran out. Ignoring STOP is not a
			// shortcut, it is the only correct reading: nobody asked for a track to
			// finish when they pressed next.
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

// Points mpv at something and leaves it paused, so the caller decides when sound
// starts.
//
// Arriving paused is the point. Resolving a stream is a second of network on its
// own goroutine, and someone who picks a track then presses p to get on with
// something else should not be interrupted by the audio of the track they already
// moved past. The caller unpauses once the model has caught up with the message.
//
// It has to be done before the loadfile, not after: mpv's default is to play what
// it loads, so a loadfile onto an unpaused instance starts coming out of the
// speakers before Start has even returned. The keys that would pause are ignored
// while a track is loading, so nothing is lost by holding it.
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

// Whether this track is still going. The display combines it with its own paused
// flag, so this is the other half: not finished and not broken.
func (p *player) Playing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.ended
}

// What went wrong, or nil for a track that finished or is still going.
func (p *player) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// How far into the track playback is, which mpv knows because it is the one doing
// the playing. Asking it is exact, where counting bytes off a pipe was an estimate
// that had to have the read-ahead subtracted from it.
func (p *player) Position() time.Duration {
	p.mu.Lock()
	target, until := p.seekTarget, p.seekUntil
	p.mu.Unlock()

	// a seek that has not landed yet is promised as the place it is going, so the
	// bar goes where the music is about to be rather than where it was
	if !until.IsZero() && time.Now().Before(until) {
		return target
	}

	seconds, err := p.getFloat("time-pos")
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// How long the whole track is, or 0 when nothing could say. A stream can take a
// moment to work this out, which is why the display treats 0 as "the bar counts up
// without a total" rather than as a failure.
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

// Moves playback by delta and promises the display the place it is going, so the
// bar does not read the old position for a moment after the key.
func (p *player) SeekBy(delta time.Duration) {
	// where we are now, read before the lock is taken: Position locks the mutex
	// itself, and a sync.Mutex is not reentrant, so asking for it while holding
	// the lock is a deadlock rather than a slow call.
	current := p.Position()

	_ = p.handle.Command([]string{"seek", seconds(delta), "relative"})

	// mpv clamps a seek past either end itself, so the promise is only a guess for
	// as long as the seek could plausibly take
	p.mu.Lock()
	p.seekTarget = current + delta
	p.seekUntil = time.Now().Add(2 * time.Second)
	p.mu.Unlock()
}

// The model's 0 to 1, handed to mpv as its 0 to 100. Nothing is kept here: the
// model owns the number and decides what it should be, so a second copy would only
// be able to disagree with it.
func (p *player) SetVolume(volume float64) {
	_ = p.handle.SetProperty("volume", mpv.FormatDouble, volume*100)
}

// Ends whatever is playing but keeps the instance, and the open audio device, for
// the next track.
func (p *player) Stop() {
	p.mu.Lock()
	p.ended = true
	p.seekTarget = 0
	p.seekUntil = time.Time{}
	p.mu.Unlock()

	_ = p.handle.Command([]string{"stop"})
}

// Tears the instance down for good. Only for the end of the program: Stop is what a
// track change uses.
//
// The order is not negotiable. Destroying an mpv handle frees the memory the event
// loop is parked in, so the loop has to be on its way out first, and WaitEvent has
// to be nudged to notice that, because it blocks with nothing left to wait for.
// The other way round is a crash or a hang rather than a clean exit, and the sort
// of thing that only shows up at the end of a session or in the second test of a
// run.
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

// A duration the way mpv's seek command wants it, as a number of seconds.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

// Points the player at a track and leaves it paused, ready for the caller to
// start.
//
// The slow half of playing, and the reason it is a tea.Cmd rather than a plain
// call: a stream has to ask yt-dlp to work out a direct media url first, which
// takes a second or two, and the display has to keep repainting while it does. A
// local file is nearly instant, but goes through here too so there is only one
// path into the player.
func openTrack(a audio, t Track) error {
	input, err := trackInput(t)
	if err != nil {
		return err
	}
	return a.Start(input)
}

// What mpv should read for a track: a path in the library folder for a local file,
// or a direct media url for a stream.
//
// A Track carries a bare file name rather than a path, so the library folder is
// worked out here. Doing it at the moment of opening is what lets the whole library
// move without the history going stale.
func trackInput(t Track) (string, error) {
	if t.Stream {
		return streamURL(t.PageURL)
	}
	return musicPath(t.Filename)
}

// Asks yt-dlp to work out a direct link to the audio of a YouTube video, without
// downloading any of it.
//
// The url is the only thing that has to be resolved, because mpv does the rest: it
// opens the connection, decodes, buffers and seeks, all from this one address. It
// used to be a temporary file that yt-dlp filled while ffmpeg read it, and the
// code to keep those two in step was the largest part of this program. A url
// expires eventually, but a direct media link is good for hours and a track is
// minutes, so it outlasts the playing by a very long way.
func streamURL(pageURL string) (string, error) {
	if pageURL == "" {
		return "", fmt.Errorf("this track has no url to play from")
	}

	// yt-dlp might not be installed yet, and installing it takes a while, so this
	// happens up front rather than being the user's problem mid-track
	ytdlp.MustInstall(context.TODO(), nil)

	// a stream spends from the same budget as a search, so switching to stream
	// mode is not a way round the limiter
	youtubeLimiter.wait()

	found, _, err := ytdlp.New().
		Retries("3").
		// web_embedded rather than the default web client: YouTube answers the
		// default client with "Sign in to confirm you're not a bot" once it has
		// decided an address is a bot, and the embedded client is not always given
		// the same answer. A workaround for that decision, not a fix to it: when a
		// real sign-in is needed this fails too.
		ExtractorArgs("youtube:player_client=web_embedded").
		// bestaudio, asked for by name rather than picked here, which is what puts
		// the resolved url in the top-level url field: the one thing being asked
		// for.
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

	// A freshly resolved url answers 403 for the first second or two of its life and
	// 206 after that, which is YouTube letting the address settle rather than
	// anything wrong with it. yt-dlp waits five seconds for the same reason when it
	// downloads. Handing the url straight to mpv means a refusal, and a refusal is
	// silent from mpv's side: the position simply never moves, which looks exactly
	// like a broken sound card.
	time.Sleep(3 * time.Second)

	return *info.URL, nil
}
