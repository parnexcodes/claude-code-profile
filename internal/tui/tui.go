package tui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"ccp/internal/util"
)

var ErrCancelled = errors.New("cancelled")

var stdinR = bufio.NewReader(os.Stdin)

func IsTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// PromptLine prints label and reads a line; def is returned on empty input.
func PromptLine(label, def string) string {
	if def != "" {
		fmt.Printf("%s %s: ", label, util.Paint(util.CDim, "["+def+"]"))
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := stdinR.ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		os.Exit(130)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// ConfirmYN returns def on empty input.
func ConfirmYN(label string, def bool) bool {
	suffix := "y/N"
	if def {
		suffix = "Y/n"
	}
	for {
		fmt.Printf("%s %s: ", label, util.Paint(util.CDim, "["+suffix+"]"))
		line, err := stdinR.ReadString('\n')
		if err != nil && line == "" {
			fmt.Println()
			os.Exit(130)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		default:
			fmt.Printf("  %s\n", util.Paint(util.CYellow, "please answer y or n"))
		}
	}
}

// SelectOption shows label then options; returns the chosen index (0-based).
func SelectOption(label string, options []string, start int) (int, error) {
	if len(options) == 0 {
		return -1, errors.New("no options")
	}
	if start < 0 || start >= len(options) {
		start = 0
	}
	if !IsTTY() {
		return selectNumbered(label, options, start)
	}
	return selectArrow(label, options, start)
}

func selectNumbered(label string, options []string, start int) (int, error) {
	fmt.Println(label)
	for i, o := range options {
		fmt.Printf("  %d) %s\n", i+1, o)
	}
	for {
		fmt.Printf("Choice %s: ", util.Paint(util.CDim, fmt.Sprintf("[%d]", start+1)))
		line, err := stdinR.ReadString('\n')
		if err != nil && line == "" {
			return -1, ErrCancelled
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return start, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			fmt.Printf("  %s\n", util.Paint(util.CYellow, fmt.Sprintf("enter 1-%d", len(options))))
			continue
		}
		return n - 1, nil
	}
}

func selectArrow(label string, options []string, start int) (int, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return selectNumbered(label, options, start)
	}
	defer func() { _ = term.Restore(fd, old); fmt.Print("\x1b[?25h") }()
	fmt.Print("\x1b[?25l")

	sel := start
	fmt.Printf("%s\r\n", util.Paint(util.CBold, label))
	for i, o := range options {
		marker := "  "
		if i == sel {
			marker = util.Paint(util.CCyan, "> ")
		}
		fmt.Printf("\x1b[K%s%s\r\n", marker, o)
	}
	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			continue
		}
		b := buf[0]
		switch {
		case b == 13, b == 10:
			_ = term.Restore(fd, old)
			fmt.Print("\x1b[?25h")
			return sel, nil
		case b == 3, b == 17:
			_ = term.Restore(fd, old)
			fmt.Print("\x1b[?25h\n")
			return -1, ErrCancelled
		case b == 27:
			if n == 1 {
				_ = term.Restore(fd, old)
				fmt.Print("\x1b[?25h\n")
				return -1, ErrCancelled
			}
			if n >= 3 && buf[1] == '[' {
				switch buf[2] {
				case 'A':
					if sel > 0 {
						sel--
						redrawArrow(options, sel)
					}
				case 'B':
					if sel < len(options)-1 {
						sel++
						redrawArrow(options, sel)
					}
				}
			}
		case b == 'k', b == 'K':
			if sel > 0 {
				sel--
				redrawArrow(options, sel)
			}
		case b == 'j', b == 'J':
			if sel < len(options)-1 {
				sel++
				redrawArrow(options, sel)
			}
		case b == 'q', b == 'Q':
			_ = term.Restore(fd, old)
			fmt.Print("\x1b[?25h\n")
			return -1, ErrCancelled
		case b >= '1' && b <= '9':
			i := int(b - '1')
			if i < len(options) {
				sel = i
				redrawArrow(options, sel)
			}
		}
	}
}

func redrawArrow(options []string, sel int) {
	fmt.Printf("\x1b[%dA", len(options))
	for i, o := range options {
		marker := "  "
		if i == sel {
			marker = util.Paint(util.CCyan, "> ")
		}
		fmt.Printf("\x1b[K%s%s\r\n", marker, o)
	}
}
