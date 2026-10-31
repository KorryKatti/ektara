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

// Where the status button points.
const presenceSiteURL = "https://korrykatti.github.io/ektara/"

// How long to wait before trying Discord again after it refused.
const presenceRetryEvery = 10 * time.Second

// Bounds the liveness check. The same figure the client library uses, so the two
// agree about what counts as reachable.
const presenceDialTimeout = 2 * time.Second

// One thing the Discord status can be showing. A value rather than handed straight
// to the client, because what Discord should be showing has to outlive the attempt
// to tell it.
type discordActivity struct {
	playing bool // false for the status shown when nothing is playing
	track   Track
	// When playback began, not when this was sent, so a status that survives a
	// reconnect still says how far into the song the listener is.
	start time.Time
}

// The payload the client is given.
func (a discordActivity) activity() client.Activity {
	siteButton := client.Button{
		Label: "Get Ektara",
		Url:   presenceSiteURL,
	}

	if !a.playing {
		return client.Activity{
			State:      "Idle",
			Details:    "No song playing",
			LargeImage: "ektara",
			LargeText:  "Ektara",
			SmallImage: "playing",
			SmallText:  "Idle",
			Buttons:    []*client.Button{&siteButton},
		}
	}

	t := a.track

	largeImage := "playing"
	buttons := []*client.Button{&siteButton}
	if t.ID != "" {
		// already inside the id check, so thumbnailURL cannot answer empty here
		largeImage = thumbnailURL(t.ID)
		pageURL := t.PageURL
		if pageURL == "" {
			pageURL = "https://www.youtube.com/watch?v=" + t.ID
		}
		buttons = append(buttons, &client.Button{
			Label: "Watch on YouTube",
			Url:   pageURL,
		})
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

// Everything the presence needs from Discord itself. An interface so the
// reconnecting can be tested without a Discord to reconnect to.
type discord interface {
	// Performs the handshake. Only reached after logout, because the library skips
	// the handshake when it believes it is already in.
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

// Whether Discord is still listening on its socket. The client cannot answer this,
// and dialing is the only way to find out.
//
// Windows is skipped: there the client talks to a named pipe rather than a file in
// a directory.
func discordAlive() bool {
	if runtime.GOOS == "windows" {
		return true
	}

	// the same path and socket name the client itself uses
	path := filepath.Join(ipc.GetIpcPath(), "discord-ipc-0")

	conn, err := net.DialTimeout("unix", path, presenceDialTimeout)
	if err != nil {
		return false
	}
	// the probe opened a connection and has no use for it
	conn.Close()
	return true
}

// The Discord connection, and what we last asked it to show.
//
// The client library cannot be asked whether it is connected and will not try
// again on its own, so the connection state is kept here, where it can be
// examined, and the thing to show is held rather than fired and forgotten.
type presence struct {
	d     discord
	appID string

	// Guards everything below, and is held across the send as well as around the
	// fields, so two tracks changing in quick succession do not interleave their
	// writes on the one socket.
	mu sync.Mutex

	// Whether we believe the handshake is in place. A belief rather than a fact:
	// nothing in the library reports the far end closing.
	connected bool

	// What the next successful send should carry, so a reconnect with nothing to
	// say does not leave the status blank until the next track comes round.
	wanted discordActivity

	// When to try again. The zero value means never tried, so try at once.
	retryAt time.Time

	// Records that a failure has already been logged, so a Discord that stays
	// closed does not write the same line every ten seconds.
	complained bool
}

// One for the given application. Does not connect: the first set does that.
func newPresence(appID string) *presence {
	return &presence{d: realDiscord{}, appID: appID}
}

// Performs the handshake and reports whether it worked.
//
// The logout first is deliberate and not tidiness: the library skips the handshake
// if it believes it is already logged in, so after Discord has restarted a bare
// login would return success while doing nothing at all.
func (p *presence) connect() error {
	p.d.logout()
	return p.d.login(p.appID)
}

// The whole connection story: make sure there is a live connection, then hand over
// what was last wanted.
//
// The caller holds the lock. Nothing here reports failure upwards: a Discord that
// is not there is an ordinary state, not an error.
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
	// Discord restarted a moment ago and the library is none the wiser.
	if !p.d.alive() {
		p.connected = false
		p.retryAt = time.Now().Add(presenceRetryEvery)
		return
	}

	// The return value is checked anyway, even though it is always nil, because
	// the library reports a failed write by printing it rather than by saying so.
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
// Returns nil in the ordinary case, so it can go into every return path of the
// tick. Only while disconnected, only once the wait has passed, and only while
// something is playing.
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
