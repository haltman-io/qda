package qda

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

type resultMsg Result

type doneMsg struct {
	err error
}

type tuiModel struct {
	ctx      context.Context
	cancel   context.CancelFunc
	results  []Result
	total    int
	skipped  int
	resultCh <-chan Result
	errCh    <-chan error
	done     bool
	err      error
	settings Settings
}

func RunInteractive(ctx context.Context, settings Settings, targets []Target, skipped []SkippedInput) ([]Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	resultCh, errCh := RunChecks(ctx, settings, targets)
	model := tuiModel{
		ctx:      ctx,
		cancel:   cancel,
		total:    len(targets),
		skipped:  len(skipped),
		resultCh: resultCh,
		errCh:    errCh,
		settings: settings,
	}

	program := tea.NewProgram(model, tea.WithContext(ctx))
	finalModel, err := program.Run()
	cancel()
	if err != nil {
		return nil, err
	}
	final, ok := finalModel.(tuiModel)
	if !ok {
		return nil, fmt.Errorf("unexpected TUI model type")
	}
	SortResults(final.results)
	if errors.Is(final.err, context.Canceled) {
		return final.results, nil
	}
	return final.results, final.err
}

func (m tuiModel) Init() tea.Cmd {
	return waitForResult(m.resultCh, m.errCh)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.cancel()
			m.err = context.Canceled
			m.done = true
			return m, tea.Quit
		}
	case resultMsg:
		m.results = append(m.results, Result(msg))
		return m, waitForResult(m.resultCh, m.errCh)
	case doneMsg:
		m.done = true
		if msg.err != nil {
			m.err = msg.err
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m tuiModel) View() string {
	var b strings.Builder
	counts := CountResults(m.results)
	progress := fmt.Sprintf("%d/%d", len(m.results), m.total)

	b.WriteString(titleStyle.Render("QDA - Quick Domain Availability"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("Progress " + progress))
	if m.skipped > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf(" | skipped %d invalid input lines", m.skipped)))
	}
	b.WriteString("\n\n")

	b.WriteString(statusLine("Available", counts[AvailabilityAvailable], availableStyle))
	b.WriteString(statusLine("Registered", counts[AvailabilityRegistered], registeredStyle))
	b.WriteString(statusLine("Reserved", counts[AvailabilityReserved], unknownStyle))
	b.WriteString(statusLine("Premium", counts[AvailabilityPremium], pendingStyle))
	b.WriteString(statusLine("Pending delete", counts[AvailabilityPendingDelete], pendingStyle))
	b.WriteString(statusLine("Redemption", counts[AvailabilityRedemption], redemptionStyle))
	b.WriteString(statusLine("Rate limited", counts[AvailabilityRateLimited], rateLimitedStyle))
	b.WriteString(statusLine("Unknown", counts[AvailabilityUnknown], unknownStyle))
	b.WriteString("\n")

	rows := recentResultsWithSettings(m.results, 14, m.settings)
	if len(rows) > 0 {
		b.WriteString(headerStyle.Render(pad("Domain", 32) + pad("State", 18) + pad("Source", 10) + pad("Expires", 22) + "Registrar"))
		b.WriteString("\n")
		for _, result := range rows {
			source := "cf"
			if result.CacheHit {
				source = "cache"
			}
			line := pad(truncate(result.Domain, 31), 32) +
				pad(string(result.Availability), 18) +
				pad(source, 10) +
				pad(emptyDash(result.ExpiresAt), 22) +
				truncate(emptyDash(result.Registrar), 35)
			b.WriteString(styleFor(result.Availability).Render(line))
			b.WriteString("\n")
		}
	}

	if m.err != nil && m.done {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render(m.err.Error()))
		b.WriteString("\n")
	}

	if !m.done {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Press q to cancel."))
	}
	return b.String()
}

func waitForResult(results <-chan Result, errs <-chan error) tea.Cmd {
	return func() tea.Msg {
		result, ok := <-results
		if ok {
			return resultMsg(result)
		}
		err := <-errs
		return doneMsg{err: err}
	}
}

func ShouldUseTUI(settings Settings, out io.Writer) bool {
	if !settings.TUI {
		return false
	}
	if f, ok := out.(interface{ Fd() uintptr }); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

var (
	titleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	dimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	headerStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250"))
	availableStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	registeredStyle  = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244"))
	pendingStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("207"))
	redemptionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	rateLimitedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	unknownStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func statusLine(label string, count int, style lipgloss.Style) string {
	return style.Render(fmt.Sprintf("%s %d", label, count)) + "  "
}

func styleFor(availability Availability) lipgloss.Style {
	switch availability {
	case AvailabilityAvailable:
		return availableStyle
	case AvailabilityRegistered:
		return registeredStyle
	case AvailabilityPendingDelete:
		return pendingStyle
	case AvailabilityRedemption:
		return redemptionStyle
	case AvailabilityRateLimited:
		return rateLimitedStyle
	default:
		return unknownStyle
	}
}

func recentResults(results []Result, limit int) []Result {
	return recentResultsWithSettings(results, limit, Settings{})
}

func recentResultsWithSettings(results []Result, limit int, settings Settings) []Result {
	filtered := make([]Result, 0, len(results))
	for _, result := range results {
		if shouldPrintConsoleResult(settings, result) {
			filtered = append(filtered, result)
		}
	}
	start := 0
	if len(filtered) > limit {
		start = len(filtered) - limit
	}
	out := append([]Result(nil), filtered[start:]...)
	SortResults(out)
	return out
}

func pad(value string, width int) string {
	if len(value) >= width {
		return value[:width]
	}
	return value + strings.Repeat(" ", width-len(value))
}

func truncate(value string, width int) string {
	if len(value) <= width {
		return value
	}
	if width <= 1 {
		return value[:width]
	}
	return value[:width-1] + "."
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
