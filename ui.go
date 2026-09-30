package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const boxWidth = 76

// UI is a line-based dialog interface: boxes are drawn with plain text and
// answers are read from the input line by line.
type UI struct {
	in    *bufio.Reader
	out   io.Writer
	color bool
}

func NewUI(in io.Reader, out io.Writer) *UI {
	u := &UI{in: bufio.NewReader(in), out: out}
	if f, ok := out.(*os.File); ok && os.Getenv("NO_COLOR") == "" {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			u.color = true
		}
	}
	return u
}

func (u *UI) Printf(format string, args ...any) { fmt.Fprintf(u.out, format, args...) }

func (u *UI) paint(code, s string) string {
	if !u.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (u *UI) Bold(s string) string   { return u.paint("1", s) }
func (u *UI) Dim(s string) string    { return u.paint("2", s) }
func (u *UI) Green(s string) string  { return u.paint("32", s) }
func (u *UI) Yellow(s string) string { return u.paint("33", s) }
func (u *UI) Red(s string) string    { return u.paint("31", s) }

// ReadLine returns io.EOF when the input is exhausted.
func (u *UI) ReadLine(prompt string) (string, error) {
	fmt.Fprint(u.out, prompt)
	line, err := u.in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(u.out)
		return "", io.EOF
	}
	return strings.TrimSpace(line), nil
}

func (u *UI) Confirm(question string) bool {
	line, err := u.ReadLine(question + " [y/N]: ")
	if err != nil {
		return false
	}
	switch strings.ToLower(line) {
	case "y", "yes", "д", "да", "н": // "н" is the Y key in the Russian layout
		return true
	}
	return false
}

// Box draws a window with a title and sections separated by rules.
func (u *UI) Box(title string, sections ...[]string) {
	title = truncate(title, boxWidth-4)
	fmt.Fprintf(u.out, "\n╭─ %s %s╮\n", u.Bold(title), strings.Repeat("─", boxWidth-3-utf8.RuneCountInString(title)))
	for i, sec := range sections {
		if i > 0 {
			fmt.Fprintf(u.out, "├%s┤\n", strings.Repeat("─", boxWidth))
		}
		for _, line := range sec {
			line = truncate(line, boxWidth-2)
			fmt.Fprintf(u.out, "│ %s%s │\n", line, strings.Repeat(" ", boxWidth-2-utf8.RuneCountInString(line)))
		}
	}
	fmt.Fprintf(u.out, "╰%s╯\n", strings.Repeat("─", boxWidth))
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
