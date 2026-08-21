package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

var errCancelled = errors.New("cancelled")

var stdinR = bufio.NewReader(os.Stdin)

func isTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// promptLine prints label and reads a line; def is returned on empty input.
func promptLine(label, def string) string {
	if def != "" {
		fmt.Printf("%s %s: ", label, paint(cDim, "["+def+"]"))
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

// confirmYN returns def on empty input.
func confirmYN(label string, def bool) bool {
	suffix := "y/N"
	if def {
		suffix = "Y/n"
	}
	for {
		fmt.Printf("%s %s: ", label, paint(cDim, "["+suffix+"]"))
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
			fmt.Printf("  %s\n", paint(cYellow, "please answer y or n"))
		}
	}
}

// selectOption shows label then options; returns the chosen index (0-based).
// On a real terminal it is an arrow-key menu; otherwise a numbered fallback.
func selectOption(label string, options []string, start int) (int, error) {
	if len(options) == 0 {
		return -1, errors.New("no options")
	}
	if start < 0 || start >= len(options) {
		start = 0
	}
	if !isTTY() {
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
		fmt.Printf("Choice %s: ", paint(cDim, fmt.Sprintf("[%d]", start+1)))
		line, err := stdinR.ReadString('\n')
		if err != nil && line == "" {
			return -1, errCancelled
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return start, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(options) {
			fmt.Printf("  %s\n", paint(cYellow, fmt.Sprintf("enter 1-%d", len(options))))
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
	// print label once, options each with \r\n so \x1b[A math stays simple
	fmt.Printf("%s\r\n", paint(cBold, label))
	for i, o := range options {
		marker := "  "
		if i == sel {
			marker = paint(cCyan, "> ")
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
		case b == 13, b == 10: // enter
			_ = term.Restore(fd, old)
			fmt.Print("\x1b[?25h")
			return sel, nil
		case b == 3, b == 17: // ctrl-c / ctrl-q
			_ = term.Restore(fd, old)
			fmt.Print("\x1b[?25h\n")
			return -1, errCancelled
		case b == 27: // escape sequence
			if n == 1 {
				_ = term.Restore(fd, old)
				fmt.Print("\x1b[?25h\n")
				return -1, errCancelled
			}
			if n >= 3 && buf[1] == '[' {
				switch buf[2] {
				case 'A': // up
					if sel > 0 {
						sel--
						redrawArrow(options, sel)
					}
				case 'B': // down
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
			return -1, errCancelled
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
	// cursor sits just after the last option line; move up to first option
	fmt.Printf("\x1b[%dA", len(options))
	for i, o := range options {
		marker := "  "
		if i == sel {
			marker = paint(cCyan, "> ")
		}
		fmt.Printf("\x1b[K%s%s\r\n", marker, o)
	}
}
