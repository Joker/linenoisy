// Package linenoisy provides readline-like line editing functionality over io.Reader & io.Writer.
package linenoisy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

const (
	ctrlA     = 1
	ctrlB     = 2
	ctrlC     = 3
	ctrlD     = 4
	ctrlE     = 5
	ctrlF     = 6
	ctrlH     = 8
	tab       = 9
	ctrlK     = 11
	ctrlL     = 12
	enter     = 13
	ctrlN     = 14
	ctrlP     = 16
	ctrlT     = 20
	ctrlU     = 21
	ctrlW     = 23
	esc       = 27
	space     = 32
	backspace = 127
	question  = '?'
)

var (
	SupportedTerms = []string{"dumb", "cons25", "emacs"} // SupportedTerms is a list of supported terminals.
	curPosPattern  = regexp.MustCompile("\x1b\\[(\\d+);(\\d+)R")
)

// Editor interacts with VT100.
type Editor struct {
	In  *bufio.Reader
	Out *bufio.Writer

	Prompt string

	Buffer  []rune // keeps the current user input.
	Cur     int    // current cursor position in Buffer.
	OldCur  int    // previous cursor position in Buffer.
	Cols    int    // width  default 80.
	Rows    int    // height default 24.
	MaxRows int    // height of editor status on the terminal.

	History History

	Complete  func(s string) []string // OPTIONAL; It takes the current user input and returns some completion suggestions.
	Help      func(s string)          // OPTIONAL; Print help.
	Hint      func(s string) *Hint    // OPTIONAL; Hint will be called while user is typing and displayed on the right of the user input.
	WidthChar func(rune) int          // OPTIONAL; Calculates character width on the terminal. (A lot of CJK characters and emojis are twice as wide as ASCII characters.)
}

// Line reads user key strokes and returns a confirmed input line while displaying editor states on the terminal.
func (e *Editor) Line() (string, error) {
	if err := e.editReset(); err != nil {
		return string(e.Buffer), err
	}

	for {
		r, _, err := e.In.ReadRune()
		if err != nil {
			return string(e.Buffer), err
		}

		switch r {
		case enter:
			return string(e.Buffer), nil
		case tab:
			err = e.completeLine()
		case question:
			err = e.printHelp()
		case backspace, ctrlH:
			err = e.editBackspace()
		case ctrlC:
			return string(e.Buffer), errors.New("try again")
		case ctrlD:
			if len(e.Buffer) == 0 {
				return string(e.Buffer), io.EOF
			}
			err = e.editDelete()
		case esc:
			r1, _, err := e.In.ReadRune()
			if err != nil {
				return string(e.Buffer), err
			}

			switch r1 {
			case '[':
				r2, _, err := e.In.ReadRune()
				if err != nil {
					return string(e.Buffer), err
				}

				switch r2 {
				case '0', '1', '2', '4', '5', '6', '7', '8', '9':
					_, _, err = e.In.ReadRune()
				case '3':
					r4, _, err := e.In.ReadRune()
					if err != nil {
						return string(e.Buffer), err
					}

					if r4 == '~' {
						err = e.editDelete()
					}
				case 'A':
					err = e.editHistoryPrev()
				case 'B':
					err = e.editHistoryNext()
				case 'C':
					err = e.editMoveRight()
				case 'D':
					err = e.editMoveLeft()
				case 'H':
					err = e.editMoveHome()
				case 'F':
					err = e.editMoveEnd()
				}
			case 'O':
				r3, _, err := e.In.ReadRune()
				if err != nil {
					return string(e.Buffer), err
				}

				switch r3 {
				case 'H':
					err = e.editMoveHome()
				case 'F':
					err = e.editMoveEnd()
				}
			}
		case ctrlL:
			if err := e.clearScreen(); err != nil {
				return string(e.Buffer), err
			}
			err = e.refreshLine()
		case ctrlW:
			err = e.editDeletePrevWord()
		case ctrlB:
			err = e.editMoveLeft()
		case ctrlF:
			err = e.editMoveRight()
		case ctrlP:
			err = e.editHistoryPrev()
		case ctrlN:
			err = e.editHistoryNext()
		case ctrlU:
			err = e.editReset()
		case ctrlK:
			err = e.editKillForward()
		case ctrlA:
			err = e.editMoveHome()
		case ctrlE:
			err = e.editMoveEnd()
		case ctrlT:
			err = e.editSwap()
		default:
			err = e.editInsert(r)
		}

		if err != nil {
			return string(e.Buffer), err
		}
	}
}

// Adjust queries the terminal about rows and cols and updates Editor's Rows and Cols.
func (e *Editor) Adjust() error {
	// https://groups.google.com/forum/#!topic/comp.os.vms/bDKSY6nG13k
	if _, err := e.Out.WriteString("\x1b7\x1b[999;999H\x1b[6n"); err != nil {
		return err
	}

	if err := e.Out.Flush(); err != nil {
		return err
	}

	res, err := e.In.ReadString('R')
	if err != nil {
		return err
	}

	ms := curPosPattern.FindStringSubmatch(res)
	r, err := strconv.Atoi(ms[1])
	if err != nil {
		return err
	}
	c, err := strconv.Atoi(ms[2])
	if err != nil {
		return err
	}

	if _, err := e.Out.WriteString("\x1b8"); err != nil {
		return err
	}

	e.Cols = c
	e.Rows = r

	return nil
}

func (e *Editor) Write(b []byte) (int, error) {
	e.init()
	ew := errWriter{w: e.Out}
	ew.writeString("\r\x1b[0K")
	ew.write(bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n")))
	ew.flush()
	if ew.err != nil {
		return 0, ew.err
	}
	return len(b), e.refreshLine()
}

//

func (e *Editor) editReset() error {
	e.init()
	e.Buffer = []rune{}
	e.OldCur = 0
	e.Cur = 0
	e.MaxRows = 0
	return e.refreshLine()
}

func (e *Editor) init() {
	if e.Rows == 0 {
		e.Rows = 24
	}
	if e.Cols == 0 {
		e.Cols = 80
	}
}

func (e *Editor) editBackspace() error {
	if e.Cur == 0 {
		return e.beep()
	}
	e.Cur--
	e.Buffer = e.Buffer[:e.Cur+copy(e.Buffer[e.Cur:], e.Buffer[e.Cur+1:])] // Delete https://github.com/golang/go/wiki/SliceTricks
	return e.refreshLine()
}

func (e *Editor) editDelete() error {
	if e.Cur == len(e.Buffer) {
		return e.beep()
	}
	e.Buffer = e.Buffer[:e.Cur+copy(e.Buffer[e.Cur:], e.Buffer[e.Cur+1:])] // Delete https://github.com/golang/go/wiki/SliceTricks
	return e.refreshLine()
}

func (e *Editor) editSwap() error {
	p := e.Cur
	if p == len(e.Buffer) {
		p = len(e.Buffer) - 1
	}

	if p == 0 {
		return e.beep()
	}

	e.Buffer[p-1], e.Buffer[p] = e.Buffer[p], e.Buffer[p-1]

	if e.Cur < len(e.Buffer) {
		e.Cur++
	}

	return e.refreshLine()
}

func (e *Editor) editMoveLeft() error {
	if e.Cur == 0 {
		return e.beep()
	}

	e.Cur--

	return e.refreshLine()
}

func (e *Editor) editMoveRight() error {
	if e.Cur == len(e.Buffer) {
		return e.beep()
	}

	e.Cur++

	return e.refreshLine()
}

func (e *Editor) editHistoryPrev() error {
	e.History.Save(string(e.Buffer))
	if err := e.History.Prev(); err != nil {
		return e.beep()
	}
	e.Buffer = []rune(e.History.Get())
	e.Cur = len(e.Buffer)
	return e.refreshLine()
}

func (e *Editor) editHistoryNext() error {
	if err := e.History.Next(); err != nil {
		return e.beep()
	}
	e.Buffer = []rune(e.History.Get())
	e.Cur = len(e.Buffer)
	return e.refreshLine()
}

func (e *Editor) editKillForward() error {
	e.Buffer = e.Buffer[:e.Cur]
	return e.refreshLine()
}

func (e *Editor) editMoveHome() error {
	if e.Cur == 0 {
		return e.beep()
	}

	e.Cur = 0
	return e.refreshLine()
}

func (e *Editor) editMoveEnd() error {
	if e.Cur == len(e.Buffer) {
		return e.beep()
	}

	e.Cur = len(e.Buffer)
	return e.refreshLine()
}

func (e *Editor) editDeletePrevWord() error {
	var w bool
	var p int
	for i := e.Cur - 1; i >= 0; i-- {
		if e.Buffer[i] != space {
			w = true // found a word to delete
			continue
		}

		if !w {
			continue
		}

		p = i + 1
		break
	}

	e.Buffer = e.Buffer[:p]
	e.Cur = p
	return e.refreshLine()
}

func (e *Editor) editInsert(r rune) error {
	// Insert https://github.com/golang/go/wiki/SliceTricks
	e.Buffer = append(e.Buffer, 0)
	copy(e.Buffer[e.Cur+1:], e.Buffer[e.Cur:])
	e.Buffer[e.Cur] = r

	e.Cur++
	return e.refreshLine()
}

//

func (e *Editor) completeLine() error {
	if e.Complete == nil {
		return e.editInsert(tab)
	}

	opts := e.Complete(string(e.Buffer))
	if len(opts) == 0 {
		return e.beep()
	}
	opts = append(opts, string(e.Buffer))

	pos := 0
	for {
		c := opts[pos]

		if err := e.refreshLineString(c); err != nil {
			return err
		}

		b, err := e.In.Peek(1)
		if err != nil {
			return err
		}

		switch b[0] {
		case tab:
			if _, _, err := e.In.ReadRune(); err != nil {
				return err
			}
			pos = (pos + len(opts) + 1) % len(opts)
		case esc:
			if _, _, err := e.In.ReadRune(); err != nil {
				return err
			}
			if err := e.refreshLine(); err != nil {
				return err
			}
			return nil
		default:
			e.Buffer = []rune(c)
			e.Cur = len(e.Buffer)
			return nil
		}
	}
}

func (e *Editor) printHelp() error {
	fmt.Fprintf(e.Out, "\n\r%s\n", "help")
	e.Out.Flush()
	return e.refreshLine()
}

//

func (e *Editor) refreshLineString(s string) error {
	b := e.Buffer
	p := e.Cur
	e.Buffer = []rune(s)
	e.Cur = len(e.Buffer)
	if err := e.refreshLine(); err != nil {
		return err
	}
	e.Buffer = b
	e.Cur = p
	return nil
}

func (e *Editor) refreshLine() error {
	type pos struct {
		cols, rows int
	}

	h := e.hint()

	f := defaultWidth
	if e.WidthChar != nil {
		f = e.WidthChar
	}

	var pw int
	for _, r := range e.Prompt {
		pw += f(r)
	}

	var bw, cw, ocw int
	for i, r := range e.Buffer {
		if i < e.Cur {
			cw += f(r)
		}
		if i < e.OldCur {
			ocw += f(r)
		}
		bw += f(r)
	}

	var hw int
	for _, r := range h {
		hw += f(r)
	}

	ep := pos{
		// cols: (pw + bw + hw) % e.Cols,
		rows: (pw + bw + hw) / e.Cols,
	}

	cp := pos{
		cols: (pw + cw) % e.Cols,
		rows: (pw + cw) / e.Cols,
	}

	ocp := pos{
		// cols: (pw + ocw) % e.Cols,
		rows: (pw + ocw) / e.Cols,
	}

	ew := &errWriter{w: e.Out}

	oldRows := e.MaxRows
	if ep.rows > e.MaxRows {
		e.MaxRows = ep.rows
	}

	// go to the bottom of editor region
	if oldRows-ocp.rows > 0 {
		ew.writeString(fmt.Sprintf("\x1b[%dB", oldRows-ocp.rows))
	}

	for i := 1; i < oldRows; i++ {
		ew.writeString("\x1b[2K") // kill line
		ew.writeString("\x1b[1A") // go up
	}

	ew.writeString("\r")
	ew.writeString(e.Prompt)
	ew.writeString(string(e.Buffer))
	ew.writeString(h)
	ew.writeString("\x1b[0K")

	// If we are at the right edge,
	// move cursor to the beginning of next line.
	if e.Cur == len(e.Buffer) && cp.cols == 0 {
		ew.writeString("\n\r")
		cp.rows++
		ep.rows++
		if ep.rows > e.MaxRows {
			e.MaxRows = ep.rows
		}
	}

	// Go up till we reach the expected position.
	if ep.rows-cp.rows > 0 {
		ew.writeString(fmt.Sprintf("\x1b[%dA", ep.rows-cp.rows))
	}

	ew.writeString("\r")
	if cp.cols > 0 {
		ew.writeString(fmt.Sprintf("\x1b[%dC", cp.cols))
	}

	ew.flush()

	e.OldCur = e.Cur

	return ew.err
}
func defaultWidth(r rune) int {
	if r == tab {
		return 4
	}
	return 1
}

//

func (e *Editor) clearScreen() error {
	n, err := e.Out.WriteString("\x1b[H\x1b[2J")
	if err != nil {
		return err
	}
	if n != 7 {
		return errors.New("failed to clear screen")
	}
	return nil
}

func (e *Editor) beep() error {
	if _, err := e.Out.WriteString("\a"); err != nil {
		return err
	}
	if err := e.Out.Flush(); err != nil {
		return err
	}
	return nil
}

//

// Hint displays helpful message with styles on the right of user input.
type Hint struct {
	Message string // message to be displayed.
	Color   Color  // text color of the hint.
	Bold    bool   // increases intensity if true.
}

func (e *Editor) hint() string {
	if e.Hint == nil {
		return ""
	}

	h := e.Hint(string(e.Buffer))
	if h == nil {
		return ""
	}

	if h.Color == 0 {
		h.Color = White
	}

	var b int
	if h.Bold {
		b = 1
	}

	return fmt.Sprintf("\x1b[%d;%d;49m%s\x1b[0m", b, h.Color, h.Message)
}

// Color represents text color.
type Color byte

const (
	Black Color = 30 + iota
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	White
)

//

type errWriter struct {
	w   *bufio.Writer
	err error
}

func (ew *errWriter) writeString(s string) {
	if ew.err != nil {
		return
	}
	_, ew.err = ew.w.WriteString(s)
}

func (ew *errWriter) write(b []byte) {
	if ew.err != nil {
		return
	}
	_, ew.err = ew.w.Write(b)
}

func (ew *errWriter) flush() {
	if ew.err != nil {
		return
	}
	ew.err = ew.w.Flush()
}

//

type History struct {
	Lines []string
	Pos   int
}

func (h *History) Add(l string) {
	if len(h.Lines) == 0 {
		h.Lines = []string{""}
	}
	h.Lines[len(h.Lines)-1] = l
	h.Lines = append(h.Lines, "")
	h.Pos = len(h.Lines) - 1
}

func (h *History) Next() error {
	if h.Pos >= len(h.Lines)-1 {
		return errors.New("end of history")
	}
	h.Pos++
	return nil
}

func (h *History) Prev() error {
	if h.Pos <= 0 {
		return errors.New("beginning of history")
	}
	h.Pos--
	return nil
}

func (h *History) Get() string {
	return h.Lines[h.Pos]
}

func (h *History) Save(l string) {
	if len(h.Lines) == 0 {
		h.Lines = []string{""}
	}
	if h.Pos != len(h.Lines)-1 {
		return
	}
	h.Lines[len(h.Lines)-1] = l
}
