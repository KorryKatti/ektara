package main

// This file is the whole terminal user interface.
//
// The rule everything here follows: the model is a struct, and it is the only
// thing that remembers anything. Update is handed a message, returns a changed
// copy of the model plus a command to run later, and View turns the model into
// text. Nothing else is allowed to draw or read keys.
//
// Anything slow (starting ffmpeg, searching YouTube, talking to Discord) is a
// command, which means a function that runs in the background and hands back a
// message. That is why there are no sleeps and no blocking calls below.

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/ebitengine/oto/v3"
	"github.com/hugolgst/rich-go/client"
)

// discordAppID is this program's Discord application.
const discordAppID = "1553038133006704670"

// redrawEvery is how often the player looks at the audio and redraws. Ten times
// a second is fast enough for a progress bar to look smooth and slow enough to
// cost nothing.
const redrawEvery = 100 * time.Millisecond

// ---------------------------------------------------------------------------
// modes
// ---------------------------------------------------------------------------

// mode says which screen is on the terminal. Only one at a time.
type mode int

const (
	modeMenu    mode = iota // the list of things you can do
	modeSearch              // typing a name for YouTube, or typing a number
	modeResults             // picking one of the search results
	modeFiles               // picking an mp3 in this folder
	modeHistory             // picking something from the history
	modePlaying             // the player
)

// menuItems are the choices on the first screen, in the order they are shown.
var menuItems = []string{
	"Online  - search YouTube and download",
	"Offline - play an mp3 in this folder",
	"History - play something already played",
	"Stream  - play from YouTube without downloading",
	"Queue   - play several tracks one after another",
	"Quit",
}

// The indexes of the menu rows, named so the code below can say menuSearch
// instead of 3 and be obvious about what it means. iota counts up on its own
// here, so the first one is pinned to 0 and the rest follow.
const (
	menuOnline  = iota // 0
	menuOffline        // 1
	menuHistory        // 2
	menuStream         // 3
	menuQueue          // 4
	menuQuit           // 5
)

// ---------------------------------------------------------------------------
// messages
// ---------------------------------------------------------------------------

// tickMsg arrives ten times a second. It carries nothing useful: it exists only
// to say "look at the audio again and redraw".
type tickMsg time.Time

// trackOpenedMsg says a track finished starting up. It carries the source and
// the player that were made, or the reason there was none.
type trackOpenedMsg struct {
	src    audioSource
	player *oto.Player
	err    error
}

// searchedMsg says a YouTube search finished.
type searchedMsg struct {
	videos []video
	err    error
}

// downloadedMsg says a download finished.
type downloadedMsg struct {
	track Track
	err   error
}

// warnMsg is something the audio source wanted to say, like "you seeked past
// the part that has downloaded so far".
type warnMsg string

// ---------------------------------------------------------------------------
// the model
// ---------------------------------------------------------------------------

// model is everything the program remembers. Update hands back a modified copy
// of it, and View draws it.
type model struct {
	db   *sql.DB
	opts options

	// which screen is showing
	mode mode

	// how big the terminal is. 0 means it has not said yet, which is true of
	// the very first frame.
	width  int
	height int

	// --- menu screen ---
	menuCursor int

	// --- search screen ---
	// searchInput is the box the user is typing into. It holds whatever they
	// have typed so far, which is a search name or a number depending on
	// askingCount.
	searchInput string
	// askingCount is true when this screen wants a number rather than a name,
	// which is what the queue mode uses it for.
	askingCount bool
	// searchWasStream remembers whether we came here from "Online" or from
	// "Stream", because picking a result has to know which to do with it.
	searchWasStream bool
	// searching is true from the moment enter is pressed until the results
	// land, so the screen can say so instead of looking frozen.
	searching bool

	// --- the three list screens (results, files, history) ---
	// All three are the same shape: a list of things with a cursor on one of
	// them, so they share one set of fields instead of three.
	items      []item
	itemCursor int

	// --- the queue being built ---
	// queueWanted is how many tracks are still to be picked. 0 means no queue
	// is being built and the next pick plays straight away.
	queueWanted int
	queue       []Track

	// --- the player ---
	index   int
	track   Track
	src     audioSource
	player  *oto.Player
	loading bool

	// paused, muted and volume are the truth about the player. The oto player
	// is told what they say. They are never set from what the player says back,
	// because the tick runs ten times a second and would undo every key press.
	paused bool
	muted  bool
	volume float64

	// elapsed and length are copied out of the audio on every tick. Nothing
	// else sets them.
	elapsed time.Duration
	length  time.Duration

	// notice is a short lived message, for "shuffle on" and "volume 60%"
	notice      string
	noticeUntil time.Time

	// lastError is the most recent playback failure, kept until something
	// replaces it. Unlike notice it does not expire on a timer, because a
	// broken track is skipped straight away and a message that were wiped on
	// the way to the next one would never get read.
	lastError string

	// spin counts ticks. It only exists to move the little animation shown
	// while a background job is running, so it can look alive.
	spin int

	// startTime is when playback began. Discord is given the same time, which
	// is what makes its elapsed clock survive a pause.
	startTime time.Time

	// wasShuffled remembers whether the queue was already shuffled, so turning
	// shuffle on part way through shuffles only what is left to play.
	wasShuffled bool
}

// item is one row in a list screen.
type item struct {
	// title is what the row shows.
	title string
	// track is what plays if the row is picked.
	track Track
	// needsDownload is true for a search result that has to be saved before it
	// can play. Picking one of those starts a download rather than playing.
	needsDownload bool
}

// ---------------------------------------------------------------------------
// init, update, view
// ---------------------------------------------------------------------------

// initialModel builds the starting model. It takes the database because
// everything the program remembers hangs off the model, including the handle it
// reads the history with.
func initialModel(db *sql.DB) model {
	return model{
		db:   db,
		mode: modeMenu,
		opts: options{},
		// 1.0 is full volume. Starting at 0 would mean a silent player until
		// the user pressed + twenty times.
		volume: 1.0,
	}
}

// Init is called once at the start. Starting the tick here is what gets the
// clock running.
func (m model) Init() tea.Cmd {
	return tick()
}

// Update is the whole program: a message arrives, the model changes, and
// sometimes a command goes out to do something in the background.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		// the terminal has told us how big it is. Nothing else uses this.
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		return m.onTick()

	case trackOpenedMsg:
		return m.onTrackOpened(msg)

	case searchedMsg:
		return m.onSearched(msg)

	case downloadedMsg:
		return m.onDownloaded(msg)

	case warnMsg:
		m.say(string(msg))
		return m, nil

	case tea.KeyPressMsg:
		return m.onKey(msg)
	}

	return m, nil
}

// View draws the model. Which screen it draws is decided by the mode.
//
// The drawing goes to the alternate screen. Without it, a long list such as the
// history leaves its lower rows behind when esc switches to the shorter menu
// box: bubbletea erases only as many lines as the new frame has, so the old
// rows above it stay on screen. The alternate screen is used whole, so every
// frame starts from a blank screen and nothing can leak between screens.
func (m model) View() tea.View {
	var s string

	switch m.mode {
	case modeMenu:
		s = m.drawMenu()
	case modeSearch:
		s = m.drawSearch()
	case modeResults, modeFiles, modeHistory:
		s = m.drawItems()
	case modePlaying:
		s = m.drawPlayer()
	}

	v := tea.NewView(s)
	v.AltScreen = true
	return v
}

// ---------------------------------------------------------------------------
// keys
// ---------------------------------------------------------------------------

// onKey is the front door for keys. It handles ctrl+c for every screen and then
// passes the rest to whichever screen is showing.
func (m model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits. It has to, because a key that only quits from one
	// screen would trap the user on any other.
	if msg.String() == "ctrl+c" {
		return m.quit()
	}

	switch m.mode {
	case modeMenu:
		return m.onMenuKey(msg)
	case modeSearch:
		return m.onSearchKey(msg)
	case modeResults, modeFiles, modeHistory:
		return m.onListKey(msg)
	case modePlaying:
		return m.onPlayerKey(msg)
	}

	return m, nil
}

func (m model) onMenuKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m.quit()

	case "up", "k":
		if m.menuCursor > 0 {
			m.menuCursor--
		}

	case "down", "j":
		if m.menuCursor < len(menuItems)-1 {
			m.menuCursor++
		}

	// typing the number jumps straight to that row, so "3" is a shortcut for
	// moving down twice and pressing enter
	case "1", "2", "3", "4", "5":
		m.menuCursor = int(msg.String()[0] - '1')
		return m.startMenuChoice()

	case "enter":
		return m.startMenuChoice()
	}

	return m, nil
}

// startMenuChoice acts on whichever menu row the cursor is on.
func (m model) startMenuChoice() (tea.Model, tea.Cmd) {
	switch m.menuCursor {

	case menuQuit:
		return m.quit()

	// "Online" and "Stream" both start by asking for a name to look up.
	// Which one it was is remembered, because picking a result has to know
	// whether to save it or stream it.
	case menuOnline, menuStream:
		m.mode = modeSearch
		m.searchInput = ""
		m.askingCount = false
		m.searchWasStream = m.menuCursor == menuStream
		return m, nil

	// "Queue" uses the same typing screen, but for a number
	case menuQueue:
		m.mode = modeSearch
		m.searchInput = ""
		m.askingCount = true
		return m, nil

	// the mp3s in this folder
	case menuOffline:
		tracks, err := localFiles()
		if err != nil {
			m.say("Could not list this folder: " + err.Error())
			return m, nil
		}
		if len(tracks) == 0 {
			m.say("No mp3 files in this folder.")
			return m, nil
		}
		m.mode = modeFiles
		m.items = tracksToItems(tracks)
		m.itemCursor = 0
		return m, nil

	// the history
	case menuHistory:
		tracks, err := historyTracks(m.db)
		if err != nil {
			m.say("Could not read the history: " + err.Error())
			return m, nil
		}
		if len(tracks) == 0 {
			m.say("Nothing has been played yet.")
			return m, nil
		}
		m.mode = modeHistory
		m.items = tracksToItems(tracks)
		m.itemCursor = 0
		return m, nil
	}

	return m, nil
}

func (m model) onSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// leaving while a search is out cancels its result: onSearched only
		// acts when searching is still true, so a late answer cannot yank the
		// user back to a screen they walked away from
		m.searching = false
		m.mode = modeMenu
		return m, nil

	case "enter":
		// one search at a time. A second enter while the first is still out
		// would start a duplicate request.
		if m.searching {
			return m, nil
		}
		// the queue screen wants a number, not a name
		if m.askingCount {
			return m.startQueue()
		}
		if strings.TrimSpace(m.searchInput) == "" {
			return m, nil
		}
		// searching takes a network round trip, so it happens in the
		// background and the results arrive as a message. searching is set so
		// the screen shows an animation instead of looking stuck.
		m.searching = true
		return m, searchCmd(m.db, m.searchInput)

	case "backspace":
		if m.searchInput != "" {
			m.searchInput = m.searchInput[:len(m.searchInput)-1]
		}
		return m, nil
	}

	// anything else that is a plain character gets typed into the box. Typing
	// is ignored while searching, because the box is no longer the focus.
	if !m.searching && len(msg.Key().Text) > 0 {
		m.searchInput += msg.Key().Text
	}
	return m, nil
}

// startQueue reads the number that was typed and starts collecting that many
// tracks. A queue of one is the same as no queue at all, so it is not treated
// as one.
func (m model) startQueue() (tea.Model, tea.Cmd) {
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(m.searchInput), "%d", &n); err != nil || n < 1 {
		m.mode = modeMenu
		m.say("That is not a number of tracks.")
		return m, nil
	}

	if n == 1 {
		// no queue to build, just go back to the menu for the one pick
		m.queueWanted = 0
		m.mode = modeMenu
		return m, nil
	}

	m.queueWanted = n
	m.queue = nil
	m.mode = modeMenu
	m.menuCursor = 0
	return m, nil
}

func (m model) onListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.mode = modeMenu
		m.askingCount = false
		return m, nil

	case "up", "k":
		if m.itemCursor > 0 {
			m.itemCursor--
		}

	case "down", "j":
		if m.itemCursor < len(m.items)-1 {
			m.itemCursor++
		}

	case "enter":
		if m.itemCursor >= len(m.items) {
			return m, nil
		}
		picked := m.items[m.itemCursor]

		// a search result has to be saved to disk before it can play, so
		// picking one starts the download and plays it when it lands
		if picked.needsDownload {
			m.say("Downloading...")
			return m, downloadCmd(picked.track.ID, picked.track.Title)
		}

		// still collecting tracks for a queue: add this one, and if it was the
		// last one asked for, play what has been collected
		if m.queueWanted > 0 {
			m.queue = append(m.queue, picked.track)
			m.queueWanted--
			if m.queueWanted == 0 {
				return m.playQueue(m.queue)
			}
			m.mode = modeMenu
			return m, nil
		}

		return m.playQueue([]Track{picked.track})
	}

	return m, nil
}

func (m model) onPlayerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// while a track is starting, the only key that does anything is one that
	// gives up and goes back, so a broken track cannot trap the user here
	if m.loading {
		if msg.String() == "esc" {
			return m.backToMenu()
		}
		return m, nil
	}

	switch msg.String() {
	case "p":
		if m.paused {
			m.paused = false
			if m.player != nil {
				m.player.Play()
			}
			// Discord gets the original start time again, so the time it
			// counts matches what has actually been heard
			return m, discordPlayingCmd(m.db, m.track, m.startTime)
		}
		m.paused = true
		if m.player != nil {
			m.player.Pause()
		}
		return m, discordIdleCmd()

	case "h":
		return m.seek(-5 * time.Second)
	case "k", "l":
		return m.seek(5 * time.Second)

	case "a":
		// no wrap: the first track has nowhere to go back to
		if m.index > 0 {
			return m.move(-1)
		}
		return m, nil
	case "d":
		return m.move(1)

	case "s":
		m.say(m.opts.toggle("shuffle", &m.opts.shuffle))
		return m, nil
	case "r":
		m.say(m.opts.toggle("repeat", &m.opts.repeat))
		return m, nil

	case "m":
		if m.muted {
			m.muted = false
		} else {
			m.muted = true
		}
		if m.player != nil {
			m.player.SetVolume(m.soundVolume())
		}
		if m.muted {
			m.say("muted")
		} else {
			m.say("unmuted")
		}
		return m, nil

	// = as well as +, because + needs shift and terminals disagree about what
	// shift and equals sends
	case "+", "=":
		m.volume = clampVolume(m.volume + 0.05)
		if m.player != nil {
			m.player.SetVolume(m.soundVolume())
		}
		m.say(fmt.Sprintf("volume %d%%", int(m.volume*100+0.5)))
		return m, nil
	case "-", "_":
		m.volume = clampVolume(m.volume - 0.05)
		if m.player != nil {
			m.player.SetVolume(m.soundVolume())
		}
		m.say(fmt.Sprintf("volume %d%%", int(m.volume*100+0.5)))
		return m, nil

	case "q":
		return m.backToMenu()
	}

	return m, nil
}

// soundVolume is what to actually hand oto: zero while muted, otherwise the
// volume. The volume number stays where it was while muted, which is what makes
// unmuting give back the level that was set rather than silence.
func (m model) soundVolume() float64 {
	if m.muted {
		return 0
	}
	return m.volume
}

func clampVolume(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ---------------------------------------------------------------------------
// starting, stopping and moving between tracks
// ---------------------------------------------------------------------------

// playQueue is the one place a queue starts playing. Everything else ends up
// here: a single pick from a list, or the queue the user built up.
func (m model) playQueue(queue []Track) (tea.Model, tea.Cmd) {
	m.queue = queue
	m.index = 0
	m.queueWanted = 0
	m.mode = modePlaying

	// shuffle happens once, here, rather than on every track. Doing it per
	// track would keep changing the order as you listen.
	m.wasShuffled = m.opts.shuffle
	if m.opts.shuffle {
		shuffleQueue(m.queue)
	}

	return m, m.startTrack()
}

// startTrack sets the model up for the current position in the queue and goes
// off to open the audio. It does not open it here, because opening is slow and
// doing it inline would freeze the display.
func (m *model) startTrack() tea.Cmd {
	m.stopTrack()

	// the index is wrapped with the queue length, so repeat can go round again
	m.track = m.queue[m.index%len(m.queue)]

	m.startTime = time.Now()
	m.elapsed = 0
	m.length = 0
	m.paused = false
	m.muted = false
	m.loading = true
	m.notice = ""

	// Discord is told straight away rather than waiting for the audio to be
	// ready, because opening a stream takes a second and a presence that lags
	// behind the music looks broken.
	//
	// No tick here. onTick returns one on every path out of itself, so the
	// chain started by Init is already running and keeps itself running.
	// Returning another one from here starts a second chain that also keeps
	// itself running, and the two never meet. Every play and every trip back to
	// the menu would leave one more behind, so the display would redraw faster
	// and faster for the rest of the session.
	return tea.Batch(openTrackCmd(m.track), discordPlayingCmd(m.db, m.track, m.startTime))
}

// stopTrack shuts the audio down.
//
// The order matters. The player is stopped before the source, because the
// source is the thing the player is reading from. The other way round leaves
// ffmpeg writing into a pipe nobody is reading.
//
// Every path that leaves a track goes through here. There are no defers in this
// code, because the function that used to have them returns immediately now
// and nothing would ever run them.
func (m *model) stopTrack() {
	if m.player != nil {
		m.player.PauseAndStopReading()
		m.player.Close()
		m.player = nil
	}
	if m.src != nil {
		// this kills ffmpeg, which is what stops a stream downloading in the
		// background after the user has moved on
		m.src.Stop()
		m.src = nil
	}
}

// move goes one track forwards or backwards and starts the new one.
func (m model) move(delta int) (tea.Model, tea.Cmd) {
	m.index += delta

	// past the end: repeat starts again, otherwise the queue is over
	if m.index >= len(m.queue) {
		if !m.opts.repeat {
			return m.backToMenu()
		}
		m.index = 0
	}

	// shuffle switched on part way through: shuffle what is left to play, so
	// the change takes effect now without disturbing the track already going.
	//
	// The index is wrapped, because with repeat on the index can be past the
	// end already, and handing shuffleQueue an empty slice makes it ask the
	// random number generator to pick a number out of nothing.
	if m.opts.shuffle && !m.wasShuffled {
		shuffleQueue(m.queue[m.index%len(m.queue):])
	}
	m.wasShuffled = m.opts.shuffle

	return m, m.startTrack()
}

// backToMenu stops the audio and shows the menu again.
func (m model) backToMenu() (tea.Model, tea.Cmd) {
	m.stopTrack()
	m.mode = modeMenu
	m.queue = nil
	m.queueWanted = 0
	m.loading = false

	// Discord goes to idle once, here, rather than between every pair of
	// tracks, or the presence flickers in the gap.
	//
	// No tick here either, for the same reason startTrack does not return one.
	return m, discordIdleCmd()
}

// quit stops the audio and ends the program.
//
// The audio is stopped here, right now, before tea.Quit is returned, and not
// from inside a command. A command runs in a goroutine and would race the
// program shutting down around it, which can leave the audio device held open
// while the terminal is being put back.
func (m model) quit() (tea.Model, tea.Cmd) {
	m.stopTrack()
	return m, tea.Quit
}

func (m model) seek(delta time.Duration) (tea.Model, tea.Cmd) {
	if m.player == nil || m.src == nil {
		return m, nil
	}
	// the Discord start time moves with the seek, so the elapsed time on the
	// presence keeps matching what is actually being heard
	m.startTime = seekBy(m.player, m.src, m.startTime, delta)
	return m, discordPlayingCmd(m.db, m.track, m.startTime)
}

// ---------------------------------------------------------------------------
// the tick
// ---------------------------------------------------------------------------

// onTick runs ten times a second. It does a few cheap things: nudges the busy
// animation along, sees whether the track has finished, copies the playback
// position into the model so the display can show it, and sets the next tick
// going.
//
// Every single path out of here returns tick(). That is the whole point of the
// function. A path that returns nil instead kills the clock for good: no more
// ticks arrive, the progress bar freezes, and nothing complains about it. The
// program starts on the menu where there is no audio to look at, so that early
// exit is not a corner case, it is the first thing that happens every run.
func (m model) onTick() (tea.Model, tea.Cmd) {
	// the tick also drives the little "busy" animation, so it is counted here
	// before any of the early exits below
	m.spin++

	// nothing to watch unless a track is actually open
	if m.mode != modePlaying || m.loading || m.player == nil || m.src == nil {
		return m, tick()
	}

	// the track finished, or it broke
	if !m.player.IsPlaying() && !m.paused {
		if err := m.player.Err(); err != nil {
			// A source that broke stops the player with an error on it, which
			// is not the same as reaching the end of the track. Only a clean
			// finish earns a history row, or every track that failed to stream
			// would end up in it.
			log.Printf("play %s: %v", m.track.label(), err)
			m.lastError = "Playback failed: " + err.Error()

			// Move on rather than sit here. Leaving the failed player in the
			// model means the next tick arrives, finds the same stopped player
			// carrying the same error, and does all of this again: ten times a
			// second, for as long as the app is left open. That is what filled
			// the log with eighty five megabytes of one repeated error.
			//
			// move() leads to startTrack(), which calls stopTrack() and clears
			// the player, so there is nothing left here to fail a second time.
			return m.move(1)
		}
		// it played all the way through, so it goes in the history.
		// move() starts the next tick going itself, by returning tick().
		logSong(m.db, m.track)
		return m.move(1)
	}

	// copy the position in. Nothing else knows where playback is, so this is
	// the only place the display can get it from.
	m.elapsed = playedDuration(m.src, m.player)
	m.length = m.src.Length()

	// a notice with no expiry would be wiped a tenth of a second after the key
	// that made it, which is too fast to read
	if m.notice != "" && time.Now().After(m.noticeUntil) {
		m.notice = ""
	}

	return m, tick()
}

// say puts a message on the display for a couple of seconds, which is long
// enough to read and short enough not to sit there for the rest of the track.
func (m *model) say(msg string) {
	m.notice = msg
	m.noticeUntil = time.Now().Add(2 * time.Second)
}

// ---------------------------------------------------------------------------
// the results of the slow commands
// ---------------------------------------------------------------------------

func (m model) onTrackOpened(msg trackOpenedMsg) (tea.Model, tea.Cmd) {
	m.loading = false

	if msg.err != nil {
		log.Printf("open %s: %v", m.track.label(), msg.err)
		m.lastError = "Could not play " + m.track.label()
		// carry on to the next one rather than sitting on a dead screen
		return m.move(1)
	}

	// it opened, so whatever went wrong last is no longer what is happening
	m.lastError = ""

	m.src = msg.src
	m.player = msg.player
	m.length = msg.src.Length()

	// the volume is whatever the player is already at, not whatever this model
	// happened to be holding, so a new track does not reset the level
	m.volume = msg.player.Volume()
	m.player.SetVolume(m.soundVolume())

	// a source that has something to say gets a goroutine waiting to say it
	return m, listenCmd(msg.src)
}

func (m model) onSearched(msg searchedMsg) (tea.Model, tea.Cmd) {
	// the user pressed esc and left while this search was still out, so this
	// answer is stale and must not reopen the screen
	if !m.searching {
		return m, nil
	}
	m.searching = false

	if msg.err != nil {
		m.say("Search failed: " + msg.err.Error())
		m.mode = modeMenu
		return m, nil
	}
	if len(msg.videos) == 0 {
		m.say("No videos found.")
		m.mode = modeMenu
		return m, nil
	}

	// each result becomes a list row. A result from "Stream" is ready to play
	// as it is; one from "Online" has to be downloaded first.
	items := make([]item, 0, len(msg.videos))
	for _, v := range msg.videos {
		row := item{title: v.Title, track: Track{ID: v.ID, Title: v.Title}}
		if m.searchWasStream {
			row.track.PageURL = "https://www.youtube.com/watch?v=" + v.ID
			row.track.Stream = true
		} else {
			row.needsDownload = true
		}
		items = append(items, row)
	}

	m.items = items
	m.itemCursor = 0
	m.mode = modeResults
	return m, nil
}

func (m model) onDownloaded(msg downloadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.say("Download failed: " + msg.err.Error())
		m.mode = modeMenu
		return m, nil
	}
	return m.playQueue([]Track{msg.track})
}

// ---------------------------------------------------------------------------
// commands: work that happens in the background
// ---------------------------------------------------------------------------

// tick waits a moment and then sends a tickMsg, which makes the model look at
// the audio again.
func tick() tea.Cmd {
	return tea.Tick(redrawEvery, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// openTrackCmd starts a track's audio in the background and sends back what it
// made. Opening a local file spawns ffmpeg, and opening a stream resolves a
// fresh YouTube url first, so this takes long enough that doing it inline would
// freeze the screen.
func openTrackCmd(t Track) tea.Cmd {
	return func() tea.Msg {
		src, player, err := openTrack(t)
		return trackOpenedMsg{src: src, player: player, err: err}
	}
}

// searchCmd runs a YouTube search in the background.
func searchCmd(db *sql.DB, query string) tea.Cmd {
	return func() tea.Msg {
		videos, err := searchYouTube(db, query)
		return searchedMsg{videos: videos, err: err}
	}
}

// downloadCmd saves a video as an mp3 in the background.
func downloadCmd(id, title string) tea.Cmd {
	return func() tea.Msg {
		track, err := downloadVideo(id, title)
		return downloadedMsg{track: track, err: err}
	}
}

// listenCmd waits for the audio source to say something and passes it on as a
// message. It sits there until the source speaks or the track is stopped, which
// is fine: one goroutine per track, thrown away with the track.
//
// A local file is not a warner, so it never has anything to say and this
// returns nothing rather than a goroutine waiting forever.
func listenCmd(src audioSource) tea.Cmd {
	w, ok := src.(warner)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return warnMsg(<-w.Warnings())
	}
}

// discordPlayingCmd tells Discord what is playing. It talks to a socket and
// looks a cover image up in the database, so it is a command too.
func discordPlayingCmd(db *sql.DB, t Track, start time.Time) tea.Cmd {
	return func() tea.Msg {
		if err := setDiscordActivity(db, t, start); err != nil {
			log.Printf("update Discord activity: %v", err)
		}
		return nil
	}
}

// discordIdleCmd tells Discord nothing is playing.
func discordIdleCmd() tea.Cmd {
	return func() tea.Msg {
		if err := setDiscordIdleActivity(); err != nil {
			log.Printf("update Discord activity: %v", err)
		}
		return nil
	}
}

// ---------------------------------------------------------------------------
// drawing
// ---------------------------------------------------------------------------

// The styles used to draw. Kept in one place so the colours can be changed
// without hunting through the drawing code.
var (
	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// contentWidth is how many columns fit inside the box. Until the terminal has
// said how big it is, this is a made up number, because the first frame can
// arrive before the size does.
func (m model) contentWidth() int {
	if m.width == 0 {
		return 60
	}
	w := m.width - 6 // two for the border, two for the padding, two to spare
	if w < 20 {
		return 20
	}
	return w
}

func (m model) drawMenu() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("ektara") + "\n\n")

	for i, text := range menuItems {
		marker := "  "
		if i == m.menuCursor {
			marker = cursorStyle.Render("▸ ")
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, text))
	}

	b.WriteString("\n" + dimStyle.Render("up/down move, enter choose, q quit") + "\n")
	if m.lastError != "" {
		b.WriteString("\n" + errorStyle.Render(m.lastError) + "\n")
	}
	if m.notice != "" {
		b.WriteString("\n" + m.notice + "\n")
	}

	return boxStyle.Width(m.contentWidth()).Render(b.String())
}

func (m model) drawSearch() string {
	prompt := "Search YouTube for: "
	if m.askingCount {
		prompt = "How many tracks? "
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(prompt) + "\n\n")
	b.WriteString("  " + cursorStyle.Render(m.searchInput) + "▌\n\n")

	// while the search is out, show that something is happening. The four
	// frames are plain characters, so this looks the same in every font.
	if m.searching {
		b.WriteString("  " + spinFrame(m.spin) + " Searching...\n")
	} else {
		b.WriteString(dimStyle.Render("enter confirm, backspace erase, esc back") + "\n")
	}

	return boxStyle.Width(m.contentWidth()).Render(b.String())
}

// spinFrames is the busy animation. Spinning through four characters is the
// whole trick.
var spinFrames = []string{"|", "/", "-", "\\"}

// spinFrame picks the frame for a given tick count. The remainder keeps it
// inside the list, so the counter can grow forever.
func spinFrame(n int) string {
	return spinFrames[n%len(spinFrames)]
}

// itemsTitle is the heading for whichever list is showing.
func (m model) itemsTitle() string {
	switch m.mode {
	case modeResults:
		return "Results"
	case modeFiles:
		return "Files in this folder"
	case modeHistory:
		return "History"
	}
	return ""
}

func (m model) drawItems() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.itemsTitle()) + "\n\n")

	// only the rows near the cursor are drawn. The history can be hundreds of
	// songs long, and drawing all of them pushes the rest of the screen off the
	// bottom, so the list scrolls instead.
	start, count := listWindow(len(m.items), m.itemCursor, m.listRows())

	for i := 0; i < count; i++ {
		it := m.items[start+i]
		marker := "  "
		line := it.title
		if start+i == m.itemCursor {
			marker = cursorStyle.Render("▸ ")
			line = cursorStyle.Render(it.title)
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, clip(line, m.contentWidth()-4)))
	}

	// a count, so it is obvious that the list continues past the bottom. It is
	// only shown when there is more than fits, because "1 of 1" is just noise.
	if len(m.items) > count {
		b.WriteString(dimStyle.Render(fmt.Sprintf("\n  showing %d-%d of %d",
			start+1, start+count, len(m.items))) + "\n")
	}

	b.WriteString("\n" + dimStyle.Render("up/down move, enter play, esc back") + "\n")
	if m.notice != "" {
		b.WriteString("\n" + m.notice + "\n")
	}

	return boxStyle.Width(m.contentWidth()).Render(b.String())
}

// listRows is how many list rows fit on the screen. The box takes four lines
// (top border, heading, blank, blank) and three more at the bottom (blank,
// hints, notice), so that is what is left of the height.
func (m model) listRows() int {
	if m.height == 0 {
		return 10
	}
	n := m.height - 12
	if n < 3 {
		return 3
	}
	return n
}

// listWindow picks the range of rows to draw, keeping the cursor inside it.
//
// total is how many rows there are, cursor is which one is highlighted, and max
// is how many fit on the screen. It returns the index of the first row drawn
// and how many rows there are in that window, which is all the caller needs to
// work out which row it is looking at.
func listWindow(total, cursor, max int) (start, rows int) {
	// everything fits, so draw it all
	if total <= max {
		return 0, total
	}

	// start half a screen above the cursor, so it sits near the middle and
	// there is room to scroll in both directions
	start = cursor - max/2
	if start < 0 {
		start = 0
	}
	if start > total-max {
		start = total - max
	}
	return start, max
}

func (m model) drawPlayer() string {
	width := m.contentWidth()
	var b strings.Builder

	// the loading screen is a different box, not the player box with a gap in
	// it, so a slow track says plainly that it is still starting
	if m.loading {
		b.WriteString(titleStyle.Render("Loading "+m.track.Title) + "\n")
		if m.notice != "" {
			b.WriteString("\n" + m.notice + "\n")
		}
		return boxStyle.Width(width).Render(b.String())
	}

	b.WriteString(titleStyle.Render("♪ "+m.track.Title) + "\n")
	b.WriteString(dimStyle.Render(clip(m.subtitle(), width-2)) + "\n")

	// a paused marker, so the display says why there is no sound
	state := ""
	if m.paused {
		state = dimStyle.Render("[paused]")
	}

	// elapsed on the left, the total on the right, the pacman between them at
	// the position playback has reached
	b.WriteString(fmt.Sprintf("\n%s %s %s  %s\n",
		clock(m.elapsed), seekBar(m.elapsed, m.length, m.barWidth()), clock(m.length), state))

	b.WriteString("\n" + dimStyle.Render(m.keyHints()) + "\n")
	if m.notice != "" {
		b.WriteString(m.notice + "\n")
	}

	if len(m.queue) > 1 {
		b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("queue (%d)", len(m.queue))) + "\n")
		start, rows := queueWindow(m.queue, m.index, 5)
		for i, t := range rows {
			marker := "  "
			if start+i == m.index {
				marker = cursorStyle.Render("▸ ")
			}
			b.WriteString(fmt.Sprintf("%s%s\n", marker, clip(t.Title, width-6)))
		}
	}

	return boxStyle.Width(width).Render(b.String())
}

// subtitle is the second line of the player: the youtube link for a stream, and
// the file it came from for a local mp3, which has no link.
func (m model) subtitle() string {
	if m.track.Stream {
		return m.track.PageURL
	}
	return m.track.Filename
}

// keyHints is the two lines of keys at the bottom of the player. s and r show
// their state, because a key that does nothing visible is a key nobody presses
// twice.
func (m model) keyHints() string {
	return fmt.Sprintf("h/k seek  a prev  d next  p pause  s shuffle %s",
		onOff(m.opts.shuffle)) + "\n" +
		fmt.Sprintf("r repeat %s  m mute  q back  vol %d%%%s",
			onOff(m.opts.repeat), int(m.volume*100+0.5), muted(m.muted))
}

// barWidth is how wide the progress bar can be. It shares a line with the
// elapsed and total times, so it gets whatever is left over.
func (m model) barWidth() int {
	w := m.contentWidth() - 2 - 7 - 7 - 8
	if w < 10 {
		return 10
	}
	return w
}

// tracksToItems turns tracks into rows for a list screen.
func tracksToItems(tracks []Track) []item {
	items := make([]item, 0, len(tracks))
	for _, t := range tracks {
		items = append(items, item{title: t.Title, track: t})
	}
	return items
}

// ---------------------------------------------------------------------------
// small display helpers
// ---------------------------------------------------------------------------

// clock formats a playing time as m:ss, or h:mm:ss once it is over an hour.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Seconds())
	if hours := seconds / 3600; hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// onOff says whether a switch is on, in as few characters as possible because
// the key hints have to fit on one line.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// muted marks the volume as silent, because the volume number stays where it
// was while mute is on, so the number on its own would say otherwise.
func muted(b bool) string {
	if b {
		return " (muted)"
	}
	return ""
}

// seekBar is the progress bar, with a pacman at the playback position. The
// pacman opens and closes its mouth as it goes, which is what makes the
// movement easy to follow while the track plays.
//
// The two mouth shapes are Canadian aboriginal syllabics, which most terminal
// fonts carry. A terminal without them shows a box instead of a pacman, which
// is a poor look but not a broken one.
func seekBar(elapsed, length time.Duration, width int) string {
	// how far through, as a fraction between 0 and 1
	done := 0.0
	if length > 0 {
		done = float64(elapsed) / float64(length)
	}
	if done < 0 {
		done = 0
	}
	if done > 1 {
		done = 1
	}

	// the cell the pacman sits in, and whether its mouth is open right now
	at := int(done * float64(width))
	if at > width-1 {
		at = width - 1
	}
	pacman := 'ᗣ' // mouth closed
	if int(elapsed/(250*time.Millisecond))%2 == 0 {
		pacman = 'ᗧ' // mouth open, mid-chomp
	}

	bar := make([]rune, width)
	for i := range bar {
		switch {
		case i < at:
			bar[i] = '█' // already played
		case i == at:
			bar[i] = pacman
		default:
			bar[i] = '░' // still to come
		}
	}
	return string(bar)
}

// queueWindow picks the part of the queue to show, at most max entries, with
// the track playing kept inside it so a and d move the list with the music.
func queueWindow(queue []Track, index, max int) (start int, rows []Track) {
	if len(queue) <= max {
		return 0, queue
	}
	start = index - max/2
	if start < 0 {
		start = 0
	}
	if start > len(queue)-max {
		start = len(queue) - max
	}
	return start, queue[start : start+max]
}

// clip shortens s until it is n columns wide, ending in an ellipsis when it had
// to cut, so a long title cannot push the box out of shape.
//
// It counts columns rather than letters, because lipgloss does too: a CJK or
// emoji rune takes two columns, and counting it as one is what makes a box with
// a Japanese title in it look broken.
func clip(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	// one column is kept for the ellipsis itself
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > n {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	// The interface owns the terminal, so nothing may write a log line to it. A
	// log goes to stderr, which is the same screen, and the line lands in the
	// middle of the drawing and stays there until the next full redraw. The log
	// therefore goes to a file in the state directory. If that file cannot be
	// opened the log is thrown away instead: a missing log is a nuisance, but a
	// garbled screen is a bug.
	if path, err := logPath(); err == nil {
		if lf, err := tea.LogToFile(path, ""); err == nil {
			defer lf.Close()
		} else {
			log.SetOutput(io.Discard)
		}
	} else {
		log.SetOutput(io.Discard)
	}

	db, err := openDB()
	if err != nil {
		fmt.Println("Could not open songs.db:", err)
		os.Exit(1)
	}
	defer db.Close()

	// Discord is optional. Without it the player still works, it just does not
	// show what is playing, so a login failure is worth a line in the log and
	// nothing more. The client leaves itself unlogged when the socket cannot be
	// opened, and quietly ignores every activity update after that.
	if err := client.Login(discordAppID); err != nil {
		log.Printf("Discord rich presence unavailable: %v", err)
	}
	if err := setDiscordIdleActivity(); err != nil {
		log.Printf("update Discord activity: %v", err)
	}

	p := tea.NewProgram(initialModel(db))
	if _, err := p.Run(); err != nil {
		fmt.Println("Alas, there's been an error:", err)
		os.Exit(1)
	}
}
