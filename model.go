package main

// This file is the model: what the interface remembers, and the three methods
// that Bubble Tea drives. Update takes a message and hands back a changed model
// plus a command to run later; View turns the model into text.

import (
	"database/sql"
	"time"

	tea "charm.land/bubbletea/v2"
)

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

// menuItems are the choices down the left, in the order they are shown. The
// names are short so they sit on one line each in the sidebar.
//
// The iota constants below count these in the same order, so the first entry
// here is menuOnline and so on. Adding one means adding a constant too.
var menuItems = []string{
	"Download",
	"Offline",
	"History",
	"Stream",
	"Queue",
	"Quit",
}

// menuDescriptions say what each choice does, one line each, in the same order
// as menuItems. They are too long for the sidebar, so the menu screen shows the
// one for whatever the cursor is on, in the middle box.
var menuDescriptions = []string{
	"search YouTube and save it to disk",
	"play an mp3 already in this folder",
	"play something played before",
	"play from YouTube without saving it",
	"play several tracks one after another",
	"leave",
}

// menuDescription is the line for whichever row the cursor is on. It falls back
// to nothing if the two lists ever get out of step, rather than reading past the
// end of one of them.
func (m model) menuDescription() string {
	if m.menuCursor < 0 || m.menuCursor >= len(menuDescriptions) {
		return ""
	}
	return menuDescriptions[m.menuCursor]
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

// trackOpenedMsg says a track finished starting up. It carries no source and no
// player any more, because there is one player for the whole session and mpv
// holds the audio; the message is only here to say that opening finished, one
// way or the other.
type trackOpenedMsg struct {
	err error
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

// recentMsg says the history has been read, and carries it. An empty list with
// no error means there is nothing to show, which is different from not having
// looked yet.
type recentMsg struct {
	tracks []Track
}

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

	// recent is the history, most recent first, read once when the program
	// starts so the first screen has something real to show.
	//
	// It is kept on the model rather than read while drawing, because drawing
	// happens ten times a second on the one goroutine that owns the screen and a
	// database query there would freeze the program. It is a snapshot: nothing
	// updates it while ektara runs, so a song played now turns up on the first
	// screen the next time the program is started.
	recent []Track

	// --- the queue being built ---
	// queueWanted is how many tracks are still to be picked. 0 means no queue
	// is being built and the next pick plays straight away.
	queueWanted int
	queue       []Track

	// --- the player ---
	index int
	track Track
	// player is the one mpv instance the whole session shares. It is a pointer
	// rather than a field of its own because there is only ever one: the audio
	// device is slow to open and mpv keeps it open across loadfiles.
	player  audio
	loading bool

	// paused, muted and volume are the truth about the player. The mpv instance
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

	// greeting is the line of text on the first screen. It is worked out once
	// here and then never touched, because View is called ten times a second and
	// picking a new line each time would make the text flicker on every frame.
	greeting string

	// art is the cover for the current track, already drawn as text, and artKey
	// is the track and width it was drawn for.
	//
	// Both are needed because the drawing arrives late: it is fetched in the
	// background, so by the time it lands the user may have skipped to another
	// track or resized the window. The key says what the drawing is actually of,
	// so a late answer is dropped rather than shown against the wrong song.
	art    string
	artKey string

	// presence is the Discord connection. It is a pointer because there is one
	// for the session, like the player, and because it carries its own lock for
	// the commands that talk to it off the display's goroutine.
	presence *presence

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
// reads the history with, and the player because it is the one the whole session
// shares.
func initialModel(db *sql.DB, a audio) model {
	return model{
		db:     db,
		mode:   modeMenu,
		opts:   options{},
		player: a,
		// 1.0 is full volume. Starting at 0 would mean a silent player until
		// the user pressed + twenty times.
		volume: 1.0,
		// Made here rather than passed in, so that there is no way to build a
		// model with no presence to ask. It does not connect on construction,
		// so this costs nothing.
		presence: newPresence(discordAppID),
		// Picked once, here, and kept for the whole run. The screen is drawn ten
		// times a second, so a greeting worked out while drawing would be a
		// different line on every frame and would never sit still long enough to
		// be read.
		greeting: timeMsg(),
	}
}

// Init is called once at the start. Starting the tick here is what gets the
// clock running.
func (m model) Init() tea.Cmd {
	// The status goes up as a command rather than being written here, because
	// it is a socket write and this is the goroutine that draws. A Discord that
	// is not running logs that and leaves the player alone; it is never a
	// reason to fail to start.
	//
	// The history is read in the background too, so the first screen has
	// something real on it. The handle is the one on the model, and a nil one
	// asks for no history rather than panicking, because a model built by hand
	// rather than by initialModel has no database.
	var cmds []tea.Cmd
	if m.db != nil {
		cmds = append(cmds, recentCmd(m.db))
	}

	return tea.Batch(append(cmds, tick(), m.presence.hideCmd())...)
}

// Update is the whole program: a message arrives, the model changes, and
// sometimes a command goes out to do something in the background.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		// the terminal has told us how big it is. Nothing else uses this.
		m.width = msg.Width
		m.height = msg.Height
		// the cover is drawn to fit, so a new size means a new drawing, and the
		// one on the model is the wrong shape now
		m.art = ""
		m.artKey = ""
		return m, m.wantArt()

	case tickMsg:
		// The presence gets its turn here rather than inside onTick, because
		// onTick returns early in several places and each of those returns would
		// otherwise have to remember to carry the retry along with it. Batching
		// it on afterwards is the one spot that cannot be missed.
		next, cmd := m.onTick()
		// the nil check is for the same reason onTick guards the player: a
		// model built by hand rather than by initialModel has neither, and the
		// clock is the one thing that runs whether or not anything is playing.
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
