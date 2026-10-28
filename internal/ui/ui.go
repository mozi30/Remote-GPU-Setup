// Package ui holds terminal output styles and prompt helpers.
package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	errStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	BoxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).
			BorderForeground(lipgloss.Color("12"))
	AlertBox = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).Padding(0, 1).
			BorderForeground(lipgloss.Color("9"))
)

func Title(s string)                { fmt.Println("\n" + titleStyle.Render("== "+s+" ==")) }
func Step(f string, a ...any)       { fmt.Println(">> " + fmt.Sprintf(f, a...)) }
func OK(f string, a ...any)         { fmt.Println(okStyle.Render("✓ " + fmt.Sprintf(f, a...))) }
func Warn(f string, a ...any)       { fmt.Println(warnStyle.Render("! " + fmt.Sprintf(f, a...))) }
func Err(f string, a ...any)        { fmt.Println(errStyle.Render("✗ " + fmt.Sprintf(f, a...))) }
func Dim(f string, a ...any) string { return dimStyle.Render(fmt.Sprintf(f, a...)) }

// Alert prints a loud boxed message (used when data could be at risk).
func Alert(lines ...string) { fmt.Println(AlertBox.Render(strings.Join(lines, "\n"))) }

// Aborted reports whether a prompt was cancelled with Ctrl+C / Esc.
func Aborted(err error) bool { return errors.Is(err, huh.ErrUserAborted) }

func Confirm(q string, def bool) bool {
	v := def
	if err := huh.NewConfirm().Title(q).Affirmative("Yes").Negative("No").Value(&v).Run(); err != nil {
		return false
	}
	return v
}

type Option[T comparable] struct {
	Label string
	Value T
}

func Select[T comparable](title string, opts []Option[T]) (T, error) {
	var v T
	hopts := make([]huh.Option[T], len(opts))
	for i, o := range opts {
		hopts[i] = huh.NewOption(o.Label, o.Value)
	}
	err := huh.NewSelect[T]().Title(title).Options(hopts...).Value(&v).Height(min(len(opts)+2, 24)).Run()
	return v, err
}

func Input(title, def string) (string, error) {
	v := def
	err := huh.NewInput().Title(title).Value(&v).Run()
	return strings.TrimSpace(v), err
}

// TypeToConfirm requires the exact word, for irreversible actions.
func TypeToConfirm(prompt, word string) bool {
	var v string
	err := huh.NewInput().Title(prompt).Description(fmt.Sprintf("Type %s to confirm, anything else cancels.", word)).
		Value(&v).Run()
	return err == nil && strings.TrimSpace(v) == word
}

func Pause() {
	fmt.Print(Dim("\npress Enter to continue..."))
	var s string
	fmt.Scanln(&s)
}
