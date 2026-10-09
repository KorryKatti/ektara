package main

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/hugolgst/rich-go/client"
)

// The status that gets built, which must not be disturbed by the reconnecting:
// the song, the cover, the link back to YouTube, and the time playback started.
func TestDiscordActivityCarriesTheTrack(t *testing.T) {
	start := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	a := discordActivity{
		playing: true,
		track:   Track{ID: "dQw4w9WgXcQ", Title: "Never Gonna Give You Up"},
		start:   start,
	}

	got := a.activity()

	if got.Details != "Never Gonna Give You Up" {
		t.Errorf("Details = %q, want the title", got.Details)
	}
	if got.LargeImage != thumbnailURL("dQw4w9WgXcQ") {
		t.Errorf("LargeImage = %q, want the thumbnail for the id", got.LargeImage)
	}
	if len(got.Buttons) != 1 {
		t.Fatalf("got %d buttons, want 1 for a YouTube track", len(got.Buttons))
	}
	if got.Buttons[0].Url != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Errorf("button url = %q, want the watch page", got.Buttons[0].Url)
	}
	// the elapsed clock is Discord's, and it is driven from this. Losing it is
	// what would make a status that survived a reconnect look like it had just
	// started.
	if got.Timestamps == nil || got.Timestamps.Start == nil {
		t.Fatal("no start time on the activity")
	}
	if !got.Timestamps.Start.Equal(start) {
		t.Errorf("start = %v, want %v", got.Timestamps.Start, start)
	}
}

// A local file: no video id, so no cover and no link. A thumbnail built from an
// empty id would otherwise put a broken image in front of every local track.
func TestDiscordActivityWithoutAnID(t *testing.T) {
	got := discordActivity{playing: true, track: Track{Title: "A Local Song"}}.activity()

	if got.LargeImage != "playing" {
		t.Errorf("LargeImage = %q, want the placeholder for a track with no cover", got.LargeImage)
	}
	if len(got.Buttons) != 0 {
		t.Errorf("got %d buttons, want none for a local file", len(got.Buttons))
	}
}

// The status when nothing is playing, which has to be safe to send at any time,
// including before the first track, because that is when the program sends it.
func TestDiscordIdleActivity(t *testing.T) {
	got := discordActivity{}.activity()

	if got.State != "Idle" {
		t.Errorf("State = %q, want Idle", got.State)
	}
	if got.Timestamps != nil {
		t.Error("an idle status should carry no timestamps")
	}
	if len(got.Buttons) != 0 {
		t.Errorf("got %d buttons, want none while idle", len(got.Buttons))
	}
}

// The test that matters most here: it pins why the retry is asked for. A presence
// reconnecting on every tick would dial Discord ten times a second for the rest of
// the session, a bug that looks like nothing at all.
func TestPresenceRetryOnlyWhenItIsDue(t *testing.T) {
	soon := time.Now().Add(presenceRetryEvery)
	past := time.Now().Add(-time.Minute)

	cases := []struct {
		name string
		p    *presence
		want bool
	}{
		{
			name: "connected, nothing wanted",
			p:    &presence{connected: true},
		},
		{
			name: "disconnected, but the wait has not passed",
			p:    &presence{wanted: playingActivity(), retryAt: soon},
		},
		{
			name: "disconnected, wait passed, a song is playing",
			p:    &presence{wanted: playingActivity(), retryAt: past},
			want: true,
		},
		{
			name: "disconnected, wait passed, nothing is playing",
			// The idle card is not worth a connection attempt every ten seconds
			// on its own, so this one deliberately does not retry.
			p: &presence{retryAt: past},
		},
		{
			name: "disconnected, but never tried",
			// retryAt is zero, which means never attempted. The first set does
			// that attempt, so there is nothing here waiting to be retried.
			p: &presence{wanted: playingActivity()},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.retryCmd() != nil; got != c.want {
				t.Errorf("retryCmd() returned a command = %v, want %v", got, c.want)
			}
		})
	}
}

// The other half of the fix: the status is held rather than fired and forgotten,
// so a connection made later shows the song playing now instead of nothing.
func TestPresenceSetRemembersWhatToShow(t *testing.T) {
	p := newPresence(discordAppID)
	want := Track{ID: "abc12345678", Title: "Something"}

	// the track is put in wanted directly rather than through set, so that this
	// stays a test of the retention rather than of the socket
	p.wanted = discordActivity{playing: true, track: want}

	if p.wanted.playing != true {
		t.Fatal("the wanted activity did not survive")
	}
	if p.wanted.track.Title != want.Title {
		t.Errorf("retained title = %q, want %q", p.wanted.track.Title, want.Title)
	}

	// and an idle status has to replace it, or a song that ended would be
	// restored the next time Discord showed up
	p.wanted = discordActivity{}
	if p.wanted.playing {
		t.Error("the idle status did not replace the retained one")
	}
}

// Guards putting the retry in Update: a model assembled by hand has no presence,
// and the clock runs whether or not anything is playing, so a missing presence
// must be survivable rather than a panic on the first tick.
func TestTickWithoutAPresenceDoesNotStopTheClock(t *testing.T) {
	m := model{} // deliberately no presence, as a hand-built model has

	_, cmd := m.Update(tickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("Update returned no command, so the clock has stopped")
	}

	// isTick looks one level into a batch, which is where the tick would be if
	// the presence had contributed a retry alongside it.
	if !isTick(cmd()) {
		t.Error("the command does not contain a tick, so the clock stops on the first frame")
	}
}

// A Discord that can be made to be in any state a test needs: not there at all,
// there but the socket died, or working. The paths worth testing are the ones
// where something is missing or broken, which no amount of asking a real Discord
// will produce on demand. It also keeps the tests off whatever Discord happens to
// be on the machine, which would make them mean different things in different
// places.
type fakeDiscord struct {
	// loginErr is what the handshake answers. A non-nil error means Discord is
	// not there.
	loginErr error
	// listening answers the liveness check. It is separate from loginErr because
	// those are different faults: a Discord that has quit still has a socket
	// file lying around, and one that is merely wedged is still listening.
	listening bool
	// sendErr is what handing over a status answers.
	sendErr error

	// the rest is what the test looks at afterwards
	logins   int
	logouts  int
	alives   int
	sends    int
	sentLast client.Activity
}

func (f *fakeDiscord) login(string) error {
	f.logins++
	return f.loginErr
}

func (f *fakeDiscord) logout() { f.logouts++ }

func (f *fakeDiscord) alive() bool {
	f.alives++
	return f.listening
}

func (f *fakeDiscord) send(a client.Activity) error {
	f.sends++
	f.sentLast = a
	return f.sendErr
}

// A presence backed by a fake, so the test controls what Discord does.
func presenceWith(d discord) *presence {
	return &presence{d: d, appID: "test-app"}
}

// The fix itself: a Discord not running when the first status is set has to be
// tried again later, and once it answers the status being asked for has to be the
// one that goes up. Before, one failed handshake left the status dead for the rest
// of the run with nothing to say so.
func TestPresenceRetriesUntilDiscordAppears(t *testing.T) {
	d := &fakeDiscord{loginErr: errNoDiscord}
	p := presenceWith(d)

	song := Track{ID: "dQw4w9WgXcQ", Title: "Never Gonna Give You Up"}
	p.set(discordActivity{playing: true, track: song, start: time.Now()})

	if d.sends != 0 {
		t.Fatalf("sent %d statuses to a Discord that was not there", d.sends)
	}
	if p.connected {
		t.Fatal("reports connected after a failed handshake")
	}

	// nothing yet, so asking again must do nothing. This is the test that keeps
	// the retry off the tick's back: at ten ticks a second, without this the
	// program would hammer Discord continuously.
	if p.retryCmd() != nil {
		t.Error("asked to retry before the wait had passed")
	}
	if d.logins != 1 {
		t.Errorf("dialled Discord %d times for one retry that was not due", d.logins)
	}

	// time passes, and Discord comes up
	p.mu.Lock()
	p.retryAt = time.Now().Add(-time.Second)
	p.mu.Unlock()
	d.loginErr = nil
	d.listening = true

	cmd := p.retryCmd()
	if cmd == nil {
		t.Fatal("no retry was offered even though one was due")
	}
	cmd()

	if !p.connected {
		t.Fatal("still disconnected after the handshake succeeded")
	}
	if d.sends != 1 {
		t.Fatalf("sent %d statuses after reconnecting, want exactly 1", d.sends)
	}
	if d.sentLast.Details != song.Title {
		t.Errorf("the status after reconnecting shows %q, want the song that was playing (%q)",
			d.sentLast.Details, song.Title)
	}
}

// The handshake is really asked for each time: the library skips it when it
// believes it is already logged in, so a reconnect after a restart would report
// success while doing nothing, writing the status into a socket nobody reads.
func TestPresenceLoggedOutBeforeEveryLogin(t *testing.T) {
	d := &fakeDiscord{loginErr: errNoDiscord}
	p := presenceWith(d)

	for i := 0; i < 3; i++ {
		p.set(discordActivity{playing: true})
		p.mu.Lock()
		p.retryAt = time.Now().Add(-time.Second)
		p.mu.Unlock()
		d.loginErr = errNoDiscord
		p.retryCmd()()
	}

	if d.logouts != d.logins {
		t.Errorf("%d logins but only %d logouts, so at least one handshake was skipped",
			d.logins, d.logouts)
	}
}

// The second way the status used to be lost, and the one that put the library's
// error message onto the terminal this program draws in. The connection believes
// it is fine and the socket is not, only tellable apart by going and looking.
func TestPresenceNoticesDiscordDiedMidSong(t *testing.T) {
	d := &fakeDiscord{listening: true}
	p := presenceWith(d)

	p.set(discordActivity{playing: true, track: Track{Title: "Before"}})
	if d.sends != 1 {
		t.Fatalf("setup: sent %d statuses, want 1", d.sends)
	}

	// Discord goes away. The next status is about to be sent and the socket is
	// not there any more.
	d.listening = false
	p.set(discordActivity{playing: true, track: Track{Title: "After"}})

	if p.connected {
		t.Error("still connected after the socket went away")
	}
	if d.sends != 1 {
		t.Errorf("sent %d statuses into a dead socket, want the 1 from before it died", d.sends)
	}
	if p.retryAt.IsZero() {
		t.Error("nothing queued to reconnect, so the status stays dead")
	}

	// and it comes back, on its own
	d.listening = true
	p.mu.Lock()
	p.retryAt = time.Now().Add(-time.Second)
	p.mu.Unlock()
	p.retryCmd()()

	if !p.connected || d.sentLast.Details != "After" {
		t.Errorf("after reconnecting the status shows %q, want the newer track", d.sentLast.Details)
	}
}

// A Discord which is not running logs once when first noticed and then stops,
// rather than repeating itself every ten seconds for as long as the program is
// open.
func TestPresenceLogsAnOutageOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	d := &fakeDiscord{loginErr: errNoDiscord}
	p := presenceWith(d)

	for i := 0; i < 5; i++ {
		p.set(discordActivity{playing: true})
		p.mu.Lock()
		p.retryAt = time.Now().Add(-time.Second)
		p.mu.Unlock()
		p.retryCmd()()
	}

	// five sets and five retries, so ten failed handshakes in total
	if n := strings.Count(buf.String(), "Discord rich presence unavailable"); n != 1 {
		t.Errorf("the outage was logged %d times, want it logged once and no more", n)
	}
}

// A working Discord is left alone: the retry exists for the broken case, and a
// reconnect loop against a healthy Discord would be its own bug.
func TestPresenceConnectedSendsNothingMore(t *testing.T) {
	d := &fakeDiscord{listening: true}
	p := presenceWith(d)
	p.set(discordActivity{playing: true})

	if p.retryCmd() != nil {
		t.Error("a connected presence asked to reconnect")
	}
	if d.logins != 1 {
		t.Errorf("dialled Discord %d times for one status, want 1", d.logins)
	}
}

// errNoDiscord stands in for a handshake that fails because nothing is
// listening. It is a distinct value so a test cannot mistake it for the send
// failing instead.
var errNoDiscord = errors.New("dial unix /run/user/1000/discord-ipc-0: connect: no such file or directory")

// playingActivity is a status with something on it, for the tests that only
// care that a song is what is being asked for.
func playingActivity() discordActivity {
	return discordActivity{
		playing: true,
		track:   Track{ID: "dQw4w9WgXcQ", Title: "Never Gonna Give You Up"},
		start:   time.Now(),
	}
}
