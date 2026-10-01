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

// presenceRetryEvery is how long to wait before trying Discord again after it
// refused. Ten seconds is long enough that a Discord which is simply not
// running is not dialled over and over, and short enough that starting Discord
// and then pressing play does not feel like a fault.
const presenceRetryEvery = 10 * time.Second

// presenceDialTimeout bounds the liveness check. It is the same figure the
// client library uses for its own connection, so that the two agree about what
// counts as reachable rather than one giving up where the other would wait.
const presenceDialTimeout = 2 * time.Second

// discordActivity is one thing the Discord status can be showing.
//
// It is kept as a value rather than being handed straight to the client,
// because what Discord should be showing has to outlive the attempt to tell it.
// See presence.
type discordActivity struct {
	// playing is false for the status shown when nothing is playing.
	playing bool
	// track is the song, when playing is true.
	track Track
	// start is when playback began, not when this was sent. Discord runs its
	// own elapsed clock from it, so a status that survives a reconnect still
	// says how far into the song the listener is rather than starting again.
	start time.Time
}

// activity builds the payload the client is given.
//
// This is what used to be setDiscordActivity, and it is unchanged: what the
// status says is a separate question from whether Discord ever hears it.
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

// discord is everything the presence needs from Discord itself.
//
// It is an interface so the reconnecting can be tested without a Discord to
// reconnect to, for the same reason the player sits behind one: the paths worth
// testing are the ones where something is missing, slow or broken, and none of
// them can be arranged by asking a real Discord to cooperate.
type discord interface {
	// login performs the handshake. It is only reached after logout, because
	// the library skips the handshake when it believes it is already in, and
	// would then report success while doing nothing at all.
	login(appID string) error
	// logout clears the library's belief that it is connected.
	logout()
	// alive reports whether Discord is still listening on its socket.
	alive() bool
	// send hands over one status.
	send(client.Activity) error
}

// realDiscord is Discord, as the client library actually reaches it.
type realDiscord struct{}

func (realDiscord) login(appID string) error { return client.Login(appID) }

func (realDiscord) logout() { client.Logout() }

func (realDiscord) alive() bool { return discordAlive() }

func (realDiscord) send(a client.Activity) error { return client.SetActivity(a) }

// discordAlive reports whether Discord is still listening on its socket.
//
// The client cannot answer this, which is the reason the second way of losing
// the status exists at all. Dialing is the only way to find out, and it is one
// connect call on a local socket, done only when a status is about to be sent.
//
// Windows is skipped. There the client talks to a named pipe rather than a
// file in a directory, and reaching one the same way to no useful end is not
// worth the code. A Discord that restarts under Windows keeps showing the last
// status until the next track, which is what happened before all of this.
func discordAlive() bool {
	if runtime.GOOS == "windows" {
		return true
	}

	// the same path and socket name the client itself uses, so this agrees
	// with it about where Discord is rather than guessing somewhere else
	path := filepath.Join(ipc.GetIpcPath(), "discord-ipc-0")

	conn, err := net.DialTimeout("unix", path, presenceDialTimeout)
	if err != nil {
		return false
	}
	// the probe opened a connection and has no use for it. Closing it straight
	// away is the point: Discord accepts and drops it, and the library's own
	// connection is untouched.
	conn.Close()
	return true
}

// presence is the Discord connection, and what we last asked it to show.
//
// It exists because the client library cannot be asked whether it is connected
// and will not try again on its own. Two ways of losing the status follow from
// that, and both used to happen here without a word:
//
//   - Discord not running when ektara starts. The handshake fails, the library
//     marks itself logged out, and from then on every update it is given is
//     quietly dropped. Starting Discord afterwards changes nothing, because
//     nothing ever asks again. The status is dead for the whole run.
//
//   - Discord restarting mid-song. The library still believes it is logged in,
//     so it keeps writing to a socket nobody is reading. The write fails, and
//     the library prints that failure to standard output, which is the terminal
//     this program owns and is drawing in. The status is stale and the screen
//     is now carrying someone else's error message.
//
// So the connection state is kept here instead, where it can be examined, and
// the thing to show is held rather than fired and forgotten.
type presence struct {
	// d is Discord itself.
	d discord
	// appID is this program's Discord application.
	appID string

	// mu guards everything below and is held across the send as well as
	// around the fields. Two tracks changing in quick succession each run on
	// their own goroutine, and without the lock their writes interleave on the
	// one socket.
	mu sync.Mutex

	// connected is whether we believe the handshake is in place. It is a belief
	// rather than a fact: nothing in the library reports the far end closing,
	// which is what the liveness check is for.
	connected bool
	// wanted is what the next successful send should carry. Keeping it is the
	// other half of the fix, because a reconnect that has nothing to say would
	// leave the status blank until the next track came round.
	wanted discordActivity
	// retryAt is when to try again. The zero value means never tried, which is
	// not the same as do not try: it means try at once.
	retryAt time.Time
	// complained records that a failure has already been logged, so a Discord
	// that stays closed does not write the same line to the log every ten
	// seconds for as long as ektara is open.
	complained bool
}

// newPresence makes one for the given application. It does not connect: the
// first set does that, which keeps all of the socket handling in one place.
func newPresence(appID string) *presence {
	return &presence{d: realDiscord{}, appID: appID}
}

// connect performs the handshake and reports whether it worked.
//
// The logout first is deliberate and not tidiness. The library will skip the
// handshake if it believes it is already logged in, so after Discord has
// restarted a bare login would return success while doing nothing at all, and
// the status would be written into a socket with nobody behind it. Logging out
// first means this always asks for a real connection. Logging out when there
// was never a connection is safe: it only clears a flag and closes a socket
// that is already nil.
func (p *presence) connect() error {
	p.d.logout()
	return p.d.login(p.appID)
}

// send is the whole connection story: make sure there is a live connection,
// then hand over what was last wanted.
//
// The caller holds the lock. Nothing here reports failure upwards, because
// there is nobody above to report to. A Discord that is not there is an
// ordinary state, not an error, and the only thing to be done about it is to
// arrange another try.
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
	// here, it costs one connection attempt later. Missed, it costs the
	// library writing its failure onto the terminal this program is drawing.
	if !p.d.alive() {
		p.connected = false
		p.retryAt = time.Now().Add(presenceRetryEvery)
		return
	}

	// The return value is checked anyway. It is always nil, because the library
	// reports a failed write by printing it rather than by saying so, but
	// relying on that would be relying on a library detail to notice a library
	// change.
	if err := p.d.send(p.wanted.activity()); err != nil {
		p.connected = false
		p.retryAt = time.Now().Add(presenceRetryEvery)
		log.Printf("update Discord activity: %v", err)
	}
}

// set records what should be showing and puts it to Discord, reconnecting if
// that is what it takes.
//
// It does socket work, so it belongs on a command goroutine rather than in
// Update. Nothing here can fail visibly: Discord being absent has to leave the
// player working exactly as it did before, which is why nothing is returned.
func (p *presence) set(a discordActivity) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.wanted = a
	p.send()
}

// showCmd sets the status to a playing song.
func (p *presence) showCmd(t Track, start time.Time) tea.Cmd {
	return func() tea.Msg {
		p.set(discordActivity{playing: true, track: t, start: start})
		return nil
	}
}

// hideCmd sets the status to nothing playing.
func (p *presence) hideCmd() tea.Cmd {
	return func() tea.Msg {
		p.set(discordActivity{})
		return nil
	}
}

// retryCmd asks Discord again, but only when that is worth doing.
//
// It returns nil in the ordinary case, which is what lets it be put into every
// return path of the tick without each one having to think about it. The three
// tests are deliberate:
//
//   - only while disconnected, so a healthy Discord is never touched;
//   - only once the wait has passed, so ten times a second does not become ten
//     attempts a second;
//   - only while something is playing, so a session that never leaves the menu
//     does not spend the rest of the run dialling for a status nobody would
//     look at. The idle card is not worth a connection attempt every ten
//     seconds on its own; a real song is.
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
