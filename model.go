package main

// The model: what the interface remembers, and the three methods Bubble Tea
// drives. Update takes a message and hands back a changed model plus a command
// to run later; View turns the model into text.

import (
	"database/sql"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Which screen is on the terminal. Only one at a time.
type mode int

const (
	modeMenu    mode = iota // the list of things you can do
	modeSearch              // typing a name for YouTube, or typing a number
	modeResults             // picking one of the search results
	modeFiles               // picking an mp3 in the library folder
	modeHistory             // picking something from the history
	modePlaying             // the player
)

// The choices down the left, in the order they are shown. The iota constants
// below count these in the same order, so adding one means adding a constant too.
var menuItems = []string{
	"Download",
	"Offline",
	"History",
	"Stream",
	"Queue",
	"Quit",
}

// One line each, same order as menuItems, shown for whatever the cursor is on.
var menuDescriptions = []string{
	"search YouTube and save it to disk",
	"play an mp3 already in your library",
	"play something played before",
	"play from YouTube without saving it",
	"play several tracks one after another",
	"leave",
}

// The line for whichever row the cursor is on.
func (m model) menuDescription() string {
	if m.menuCursor < 0 || m.menuCursor >= len(menuDescriptions) {
		return ""
	}
	return menuDescriptions[m.menuCursor]
}

// Named so the code can say menuSearch instead of 3.
const (
	menuOnline  = iota // 0
	menuOffline        // 1
	menuHistory        // 2
	menuStream         // 3
	menuQueue          // 4
	menuQuit           // 5
)

// Says "look at the audio again and redraw".
type tickMsg time.Time

// A track finished starting up, one way or the other, with whatever we learned
// about the music on the way.
type trackOpenedMsg struct {
	err   error
	track Track
}

type searchedMsg struct {
	videos []video
	err    error
}

type downloadedMsg struct {
	track Track
	err   error

	// True if the download is queued instead of played

	forQueue bool
}

// An empty list with no error means there is nothing to show, which is different
// from not having looked yet.
type recentMsg struct {
	tracks []Track
}

// Everything the program remembers.
type model struct {
	db   *sql.DB
	opts options

	mode mode

	// How big the terminal is. 0 means it has not said yet.
	width  int
	height int

	// --- menu screen ---
	menuCursor int

	// --- search screen ---
	// The box the user is typing into: a search name or a number.
	searchInput string
	// True when this screen wants a number rather than a name.
	askingCount bool
	// Remembers whether we came from "Online" or "Stream".
	searchWasStream bool
	// True from the moment enter is pressed until the results land.
	searching bool

	// --- the three list screens (results, files, history) ---
	// All three are the same shape, so they share one set of fields.
	items      []item
	itemCursor int

	// The history, most recent first. Kept on the model rather than read while
	// drawing, because a query there would freeze the program. A snapshot: nothing
	// updates it while ektara runs.
	recent []Track

	// --- the queue being built ---
	// How many tracks are still to be picked. 0 means the next pick plays straight
	// away.
	queueWanted int
	queue       []Track

	// --- the player ---
	index int
	track Track
	// A pointer because there is one mpv instance for the whole session.
	player  audio
	loading bool

	// The truth about the player; the mpv instance is told what they say, never
	// the other way round.
	paused bool
	muted  bool
	volume float64

	// Copied out of the audio on every tick. Nothing else sets them.
	elapsed time.Duration
	length  time.Duration

	// A short lived message, for "shuffle on" and "volume 60%".
	notice      string
	noticeUntil time.Time

	// The most recent playback failure, kept until something replaces it. Unlike
	// notice it does not expire, because a broken track is skipped straight away.
	lastError string

	// Counts ticks, only to move the animation shown while a background job runs.
	spin int

	// When playback began. Discord is given the same time, which is what makes
	// its elapsed clock survive a pause.
	startTime time.Time

	// Worked out once and then never touched, so it does not flicker on every
	// frame.
	greeting string

	// The cover for the current track, already drawn as text, with the track and
	// width it was drawn for. The drawing arrives late, so the key says what it is
	// actually of and a late answer is dropped rather than shown against the wrong
	// song.
	art    string
	artKey string

	// A pointer because there is one for the session, and it carries its own lock.
	presence *presence

	// Whether the queue was already shuffled, so turning shuffle on part way
	// through shuffles only what is left.
	wasShuffled bool

	// true from the moment a queue starts until it runs out or program tstops
	// basically doing independence , letting music continue while the whole app is browseable.
	rolling bool
}

// One row in a list screen.
type item struct {
	title string
	track Track
	// True for a search result that has to be saved before it can play: picking
	// one starts a download rather than playing.
	needsDownload bool
}

func initialModel(db *sql.DB, a audio) model {
	return model{
		db:     db,
		mode:   modeMenu,
		opts:   options{},
		player: a,
		// 1.0 is full volume.
		volume: 1.0,
		// Made here rather than passed in, so there is no way to build a model
		// with no presence to ask.
		presence: newPresence(discordAppID),
		greeting: timeMsg(),
	}
}

// Called once at the start. Starting the tick here is what gets the clock
// running.
func (m model) Init() tea.Cmd {
	// The status goes up as a command rather than written here, because it is a
	// socket write and this is the goroutine that draws. The history is read in
	// the background too, so the first screen has something real on it.
	var cmds []tea.Cmd
	if m.db != nil {
		cmds = append(cmds, recentCmd(m.db))
	}

	return tea.Batch(append(cmds, tick(), m.presence.hideCmd())...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// The cover is drawn to fit, so a new size means a new drawing.
		m.art = ""
		m.artKey = ""
		return m, m.wantArt()

	case tickMsg:
		// Presence gets its turn here rather than inside onTick, which returns
		// early in several places.
		next, cmd := m.onTick()
		// A model built by hand has no presence, and the clock runs whether or not
		// anything is playing.
		if m.presence != nil {
			if retry := m.presence.retryCmd(); retry != nil {
				cmd = tea.Batch(cmd, retry)
			}
		}
		return next, cmd

	case trackOpenedMsg:
		return m.onTrackOpened(msg)

	case searchedMsg:
		return m.onSearched(msg)

	case downloadedMsg:
		return m.onDownloaded(msg)

	case recentMsg:
		m.recent = msg.tracks
		return m, nil

	case artMsg:
		return m.onArt(msg)

	case tea.KeyPressMsg:
		return m.onKey(msg)
	}

	return m, nil
}

// The drawing goes to the alternate screen, so every frame starts from a blank
// one. Without it, a long list leaves its lower rows behind when esc switches to
// a shorter box.
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
