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

// The choices down the left, in the order they are shown. Short so they fit on
// one line each in the sidebar.
//
// The iota constants below count these in the same order, so the first entry here
// is menuOnline and so on: adding one means adding a constant too.
var menuItems = []string{
	"Download",
	"Offline",
	"History",
	"Stream",
	"Queue",
	"Quit",
}

// One line each, same order as menuItems. Too long for the sidebar, so the menu
// screen shows the one for whatever the cursor is on, in the middle box.
var menuDescriptions = []string{
	"search YouTube and save it to disk",
	"play an mp3 already in your library",
	"play something played before",
	"play from YouTube without saving it",
	"play several tracks one after another",
	"leave",
}

// The line for whichever row the cursor is on, falling back to nothing if the
// two lists ever get out of step rather than reading past the end of one.
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

// Ten times a second. It carries nothing useful: it exists only to say "look at
// the audio again and redraw".
type tickMsg time.Time

// A track finished starting up, one way or the other. No source or player,
// because there is one player for the session and mpv holds the audio.
type trackOpenedMsg struct {
	err error
}

type searchedMsg struct {
	videos []video
	err    error
}

type downloadedMsg struct {
	track Track
	err   error
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

	// How big the terminal is. 0 means it has not said yet, true of frame one.
	width  int
	height int

	// --- menu screen ---
	menuCursor int

	// --- search screen ---
	// The box the user is typing into: a search name or a number, depending on
	// askingCount.
	searchInput string
	// True when this screen wants a number rather than a name, which is what the
	// queue mode uses it for.
	askingCount bool
	// Remembers whether we came from "Online" or "Stream", because picking a
	// result has to know which to do with it.
	searchWasStream bool
	// True from the moment enter is pressed until the results land, so the screen
	// can say so instead of looking frozen.
	searching bool

	// --- the three list screens (results, files, history) ---
	// All three are the same shape, so they share one set of fields.
	items      []item
	itemCursor int

	// The history, most recent first. Kept on the model rather than read while
	// drawing, because drawing happens ten times a second on the one goroutine
	// that owns the screen and a query there would freeze the program. It is a
	// snapshot: nothing updates it while ektara runs, so a song played now turns
	// up on the first screen the next time the program is started.
	recent []Track

	// --- the queue being built ---
	// How many tracks are still to be picked. 0 means none is being built and the
	// next pick plays straight away.
	queueWanted int
	queue       []Track

	// --- the player ---
	index int
	track Track
	// A pointer because there is one mpv instance for the whole session: the audio
	// device is slow to open and mpv keeps it open across loadfiles.
	player  audio
	loading bool

	// The truth about the player; the mpv instance is told what they say. Never
	// set from what the player says back, because the tick runs ten times a
	// second and would undo every key press.
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
	// notice it does not expire, because a broken track is skipped straight away
	// and a message wiped on the way to the next one would never get read.
	lastError string

	// Counts ticks, only to move the animation shown while a background job
	// runs, so it can look alive.
	spin int

	// When playback began. Discord is given the same time, which is what makes
	// its elapsed clock survive a pause.
	startTime time.Time

	// Worked out once and then never touched: View is called ten times a second
	// and a greeting picked while drawing would flicker on every frame.
	greeting string

	// The cover for the current track, already drawn as text, and the track and
	// width it was drawn for.
	//
	// Both are needed because the drawing arrives late: it is fetched in the
	// background, so by the time it lands the user may have skipped to another
	// track or resized the window. The key says what the drawing is actually of,
	// so a late answer is dropped rather than shown against the wrong song.
	art    string
	artKey string

	// A pointer because there is one for the session, like the player, and because
	// it carries its own lock for commands that talk to it off the display's
	// goroutine.
	presence *presence

	// Whether the queue was already shuffled, so turning shuffle on part way
	// through shuffles only what is left to play.
	wasShuffled bool
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
		// 1.0 is full volume; starting at 0 means silence until + is pressed 20
		// times.
		volume: 1.0,
		// Made here rather than passed in, so there is no way to build a model
		// with no presence to ask. Construction does not connect, so it is free.
		presence: newPresence(discordAppID),
		greeting: timeMsg(),
	}
}

// Called once at the start. Starting the tick here is what gets the clock
// running.
func (m model) Init() tea.Cmd {
	// The status goes up as a command rather than written here, because it is a
	// socket write and this is the goroutine that draws. A Discord that is not
	// running logs that and leaves the player alone.
	//
	// The history is read in the background too, so the first screen has something
	// real on it. A nil db asks for no history rather than panicking, because a
	// model built by hand has no database.
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
		// The cover is drawn to fit, so a new size means a new drawing and the one
		// on the model is the wrong shape now.
		m.art = ""
		m.artKey = ""
		return m, m.wantArt()

	case tickMsg:
		// Presence gets its turn here rather than inside onTick, because onTick
		// returns early in several places and each would otherwise have to
		// remember to carry the retry along. Batching on afterwards is the one spot
		// that cannot be missed.
		next, cmd := m.onTick()
		// The nil check is for the same reason onTick guards the player: a model
		// built by hand has neither, and the clock runs whether or not anything is
		// playing.
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

// Which screen it draws is decided by the mode.
//
// The drawing goes to the alternate screen. Without it, a long list such as the
// history leaves its lower rows behind when esc switches to the shorter menu box:
// bubbletea erases only as many lines as the new frame has, so the old rows above
// it stay on screen. The alternate screen is used whole, so every frame starts
// from a blank screen.
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
