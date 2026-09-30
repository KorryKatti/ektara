package main

import (
	"fmt"
	"os"

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
		Render("  My Spotify TUI")

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
			"Good evening\n\n" +
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
