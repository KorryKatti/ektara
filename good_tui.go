package main

import (
	"fmt"
	"math/rand"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type model struct {
	width  int
	height int
}

var (
	titleStyle = lipgloss.NewStyle().
			Height(1).
			Padding(0, 1).
			Background(lipgloss.Color("#1DB954")).
			Foreground(lipgloss.Color("#000000")).
			Bold(true)

	sidebarStyle = lipgloss.NewStyle().
			Width(24).
			Padding(1).
			Background(lipgloss.Color("#181818")).
			Foreground(lipgloss.Color("#FFFFFF"))

	contentStyle = lipgloss.NewStyle().
			Padding(1, 2).
			Background(lipgloss.Color("#121212")).
			Foreground(lipgloss.Color("#FFFFFF"))

	playerStyle = lipgloss.NewStyle().
			Height(4).
			Padding(0, 1).
			Background(lipgloss.Color("#181818")).
			Foreground(lipgloss.Color("#FFFFFF")).
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#333333"))
)

func initialModel() model {
	return model{}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

func timeMsg() string {
	hour := time.Now().Hour()

	var pool []string

	switch {
	case hour < 5:
		pool = []string{
			"I am the master of my fate, I am the captain of my soul.",
			"Hello darkness, my old friend.",
			"Do not go gentle into that good night.",
			"See you space cowboy...",
			"In the silence, I find myself.",
			"The night whispers secrets.",
		}

	case hour < 10:
		pool = []string{
			"Here comes the sun, and I say, it's all right.",
			"Wake up to reality.",
			"I love the smell of napalm in the morning.",
			"It's a new dawn, it's a new day.",
			"Every morning brings new opportunities.",
			"Rise and shine—the world awaits.",
		}

	case hour < 14:
		pool = []string{
			"It's high noon.",
			"Fortune favors the bold.",
			"There's a starman waiting in the sky.",
			"May the Force be with you.",
			"Peak performance unlocked.",
			"The world is your playground.",
		}

	case hour < 18:
		pool = []string{
			"The show must go on.",
			"We're gonna need a bigger boat.",
			"Fear is the mind-killer.",
			"Carry on my wayward son.",
			"Every ending is a new beginning.",
			"The best is yet to come.",
		}

	case hour < 22:
		pool = []string{
			"Why so serious?",
			"I am inevitable.",
			"Paint it black.",
			"The night is dark and full of terrors.",
			"Embrace the shadows within.",
			"What doesn't kill you makes you stronger.",
		}

	default:
		pool = []string{
			"I can do this all day.",
			"Turn off your mind, relax and float downstream.",
			"End? No, the journey doesn't end here.",
			"To infinity and beyond.",
			"In dreams, we are infinite.",
			"The night whispers secrets only you can hear.",
		}
	}

	return pool[rand.Intn(len(pool))]
}

func (m model) View() string {
	if m.width == 0 {
		return ""
	}

	// Fixed-height areas.
	titleHeight := 1
	playerHeight := 4

	// Everything between title and player.
	bodyHeight := m.height - titleHeight - playerHeight
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	title := titleStyle.
		Width(m.width).
		Render("  Ektara ⭐")

	sidebar := sidebarStyle.
		Height(bodyHeight).
		Render(
			"HOME\n\n" +
				"  Home\n" +
				"  Search\n" +
				"  Library\n\n" +
				"PLAYLISTS\n\n" +
				"  Liked Songs\n" +
				"  Chill\n" +
				"  Coding\n" +
				"  Rock",
		)

	contentWidth := m.width - 24
	if contentWidth < 1 {
		contentWidth = 1
	}

	content := contentStyle.
		Width(contentWidth).
		Height(bodyHeight).
		Render(
			timeMsg() + "\n\n" +
				"Recently played\n\n" +
				"  [ Album ]    [ Album ]    [ Album ]\n\n" +
				"  Track 1\n" +
				"  Track 2\n" +
				"  Track 3",
		)

	body := lipgloss.JoinHorizontal(
		lipgloss.Top,
		sidebar,
		content,
	)

	player := playerStyle.
		Width(m.width).
		Render(
			"▶  ❚❚    ━━━━━━━━━●━━━━━━━━    2:31 / 4:12\n" +
				"     Some Song — Some Artist",
		)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		body,
		player,
	)
}

func main() {
	p := tea.NewProgram(
		initialModel(),
		tea.WithAltScreen(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
