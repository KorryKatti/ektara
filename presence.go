package main

import (
	"log"
	"net"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hugolgst/rich-go/client"
	"github.com/hugolgst/rich-go/ipc"
)

// How long to wait before trying Discord again after it refused: long enough that
// a Discord which is simply not running is not dialled over and over, short enough
// that starting Discord then pressing play does not feel like a fault.
const presenceRetryEvery = 10 * time.Second

// Bounds the liveness check. The same figure the client library uses for its own
// connection, so the two agree about what counts as reachable rather than one
// giving up where the other would wait.
const presenceDialTimeout = 2 * time.Second

// One thing the Discord status can be showing.
//
// A value rather than handed straight to the client, because what Discord should
// be showing has to outlive the attempt to tell it. See presence.
type discordActivity struct {
	playing bool // false for the status shown when nothing is playing
	track   Track
	// When playback began, not when this was sent. Discord runs its own elapsed
	// clock from it, so a status that survives a reconnect still says how far into
	// the song the listener is rather than starting again.
	start time.Time
}

// The payload the client is given. What the status says is a separate question
// from whether Discord ever hears it.
func (a discordActivity) activity() client.Activity {
	if !a.playing {
		return client.Activity{
			State:      "Idle",
			Details:    "No song playing",
			LargeImage: "ektara",
			LargeText:  "Ektara",
			SmallImage: "playing",
			SmallText:  "Idle",
		}
	}

	t := a.track

	largeImage := "playing"
	var buttons []*client.Button
	if t.ID != "" {
		// already inside the id check, so thumbnailURL cannot answer empty here
		largeImage = thumbnailURL(t.ID)
		pageURL := t.PageURL
		if pageURL == "" {
			pageURL = "https://www.youtube.com/watch?v=" + t.ID
		}
		buttons = []*client.Button{
			{
				Label: "Watch on YouTube",
				Url:   pageURL,
			},
		}
	}

	return client.Activity{
		State:      "Listening to music",
		Details:    t.Title,
		LargeImage: largeImage,
		LargeText:  t.Title,
		SmallImage: "ektara",
		SmallText:  "Ektara",
		Timestamps: &client.Timestamps{
			Start: &a.start,
		},
		Buttons: buttons,
	}
}

// Everything the presence needs from Discord itself.
//
// An interface so the reconnecting can be tested without a Discord to reconnect
// to, for the same reason the player sits behind one: the paths worth testing are
// the ones where something is missing, slow or broken, and none of them can be
// arranged by asking a real Discord to cooperate.
type discord interface {
	// Performs the handshake. Only reached after logout, because the library skips
	// the handshake when it believes it is already in, and would then report
	// success while doing nothing at all.
	login(appID string) error

	// Clears the library's belief that it is connected.
	logout()

	// Whether Discord is still listening on its socket.
	alive() bool

	// Hands over one status.
	send(client.Activity) error
}

// Discord, as the client library actually reaches it.
type realDiscord struct{}

func (realDiscord) login(appID string) error { return client.Login(appID) }

func (realDiscord) logout() { client.Logout() }

func (realDiscord) alive() bool { return discordAlive() }

func (realDiscord) send(a client.Activity) error { return client.SetActivity(a) }

// Whether Discord is still listening on its socket.
//
// The client cannot answer this, which is the reason the second way of losing the
// status exists at all. Dialing is the only way to find out, and it is one connect
// call on a local socket, done only when a status is about to be sent.
//
// Windows is skipped: there the client talks to a named pipe rather than a file in
// a directory, and reaching one the same way to no useful end is not worth the
// code. A Discord that restarts under Windows keeps showing the last status until
// the next track, which is what happened before all of this.
func discordAlive() bool {
	if runtime.GOOS == "windows" {
		return true
	}

	// the same path and socket name the client itself uses, so this agrees with
	// it about where Discord is rather than guessing somewhere else
	path := filepath.Join(ipc.GetIpcPath(), "discord-ipc-0")

	conn, err := net.DialTimeout("unix", path, presenceDialTimeout)
	if err != nil {
		return false
	}
	// the probe opened a connection and has no use for it. Closing it straight away
	// is the point: Discord accepts and drops it, and the library's own connection
	// is untouched.
	conn.Close()
	return true
}

// The Discord connection, and what we last asked it to show.
//
// Exists because the client library cannot be asked whether it is connected and
// will not try again on its own. Two ways of losing the status follow, and both used
// to happen here without a word:
//
//   - Discord not running when ektara starts. The handshake fails, the library marks
//     itself logged out, and from then on every update it is given is quietly
//     dropped. Starting Discord afterwards changes nothing, because nothing ever
//     asks again. The status is dead for the whole run.
//
//   - Discord restarting mid-song. The library still believes it is logged in, so it
//     keeps writing to a socket nobody is reading. The write fails, and the library
//     prints that failure to standard output, which is the terminal this program
//     owns and is drawing in. The status is stale and the screen is now carrying
//     someone else's error message.
//
// So the connection state is kept here, where it can be examined, and the thing to
// show is held rather than fired and forgotten.
type presence struct {
	d     discord
	appID string

	// Guards everything below, and is held across the send as well as around the
	// fields. Two tracks changing in quick succession each run on their own
	// goroutine, and without the lock their writes interleave on the one socket.
	mu sync.Mutex

	// Whether we believe the handshake is in place. A belief rather than a fact:
	// nothing in the library reports the far end closing, which is what the
	// liveness check is for.
	connected bool

	// What the next successful send should carry. Keeping it is the other half of
	// the fix, because a reconnect with nothing to say would leave the status blank
	// until the next track came round.
	wanted discordActivity

	// When to try again. The zero value means never tried, which is not the same as
	// do not try: it means try at once.
	retryAt time.Time

	// Records that a failure has already been logged, so a Discord that stays
	// closed does not write the same line every ten seconds for as long as ektara
	// is open.
	complained bool
}

// One for the given application. Does not connect: the first set does that, which
// keeps all of the socket handling in one place.
func newPresence(appID string) *presence {
	return &presence{d: realDiscord{}, appID: appID}
}

// Performs the handshake and reports whether it worked.
//
// The logout first is deliberate and not tidiness. The library skips the handshake
// if it believes it is already logged in, so after Discord has restarted a bare
// login would return success while doing nothing at all, and the status would be
// written into a socket with nobody behind it. Logging out first always asks for a
// real connection, and is safe when there was never one: it only clears a flag and
// closes a socket that is already nil.
func (p *presence) connect() error {
	p.d.logout()
	return p.d.login(p.appID)
}

// The whole connection story: make sure there is a live connection, then hand over
// what was last wanted.
//
// The caller holds the lock. Nothing here reports failure upwards, because there is
// nobody above to report to. A Discord that is not there is an ordinary state, not
// an error, and the only thing to be done about it is arrange another try.
func (p *presence) send() {
	if !p.connected {
		if err := p.connect(); err != nil {
			p.connected = false
			p.retryAt = time.Now().Add(presenceRetryEvery)
			if !p.complained {
				log.Printf("Discord rich presence unavailable: %v", err)
				p.complained = true
			}
			return
		}
		p.connected = true
		p.retryAt = time.Time{}
		p.complained = false
	}

	// The handshake succeeded, but the far end can still have gone away since:
	// Discord restarted a moment ago and the library is none the wiser. Caught
	// here it costs one connection attempt later; missed, it costs the library
	// writing its failure onto the terminal this program is drawing.
	if !p.d.alive() {
		p.connected = false
		p.retryAt = time.Now().Add(presenceRetryEvery)
		return
	}

	// The return value is checked anyway. It is always nil, because the library
	// reports a failed write by printing it rather than by saying so, but relying
	// on that would be relying on a library detail to notice a library change.
	if err := p.d.send(p.wanted.activity()); err != nil {
		p.connected = false
		p.retryAt = time.Now().Add(presenceRetryEvery)
		log.Printf("update Discord activity: %v", err)
	}
}

// Records what should be showing and puts it to Discord, reconnecting if that is
// what it takes.
//
// Does socket work, so it belongs on a command goroutine rather than in Update.
// Nothing here can fail visibly: Discord being absent has to leave the player
// working exactly as before, which is why nothing is returned.
func (p *presence) set(a discordActivity) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.wanted = a
	p.send()
}

// Sets the status to a playing song.
func (p *presence) showCmd(t Track, start time.Time) tea.Cmd {
	return func() tea.Msg {
		p.set(discordActivity{playing: true, track: t, start: start})
		return nil
	}
}

// Sets the status to nothing playing.
func (p *presence) hideCmd() tea.Cmd {
	return func() tea.Msg {
		p.set(discordActivity{})
		return nil
	}
}

// Asks Discord again, but only when that is worth doing.
//
// Returns nil in the ordinary case, which is what lets it go into every return
// path of the tick without each one having to think about it. The three tests are
// deliberate: only while disconnected, so a healthy Discord is never touched; only
// once the wait has passed, so ten times a second does not become ten attempts a
// second; and only while something is playing, so a session that never leaves the
// menu does not spend the run dialling for a status nobody would look at.
func (p *presence) retryCmd() tea.Cmd {
	p.mu.Lock()
	due := !p.connected && p.wanted.playing &&
		!p.retryAt.IsZero() && time.Now().After(p.retryAt)
	p.mu.Unlock()

	if !due {
		return nil
	}

	return func() tea.Msg {
		p.set(p.wanted)
		return nil
	}
}
