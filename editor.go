// Package linenoisy provides readline-like line editing functionality over io.Reader & io.Writer.
package linenoisy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
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
	backspace = 127
)

var (
	Black   = []byte{esc, '[', '3', '0', 'm'}
	Red     = []byte{esc, '[', '3', '1', 'm'}
	Green   = []byte{esc, '[', '3', '2', 'm'}
	Yellow  = []byte{esc, '[', '3', '3', 'm'}
	Blue    = []byte{esc, '[', '3', '4', 'm'}
	Magenta = []byte{esc, '[', '3', '5', 'm'}
	Cyan    = []byte{esc, '[', '3', '6', 'm'}
	White   = []byte{esc, '[', '3', '7', 'm'}
	Reset   = []byte{esc, '[', '0', 'm'}

	SupportedTerms = []string{"dumb", "cons25", "emacs"} // SupportedTerms is a list of supported terminals.
	curPosPattern  = regexp.MustCompile("\x1b\\[(\\d+);(\\d+)R")
)

// Terminal interacts with VT100.
type Terminal struct {
	Inp *bufio.Reader
	Out *bufio.Writer
	Raw io.ReadWriteCloser

	Prompt string

	Buffer  []rune // keeps the current user input.
	Cur     int    // current cursor position in Buffer.
	OldCur  int    // previous cursor position in Buffer.
	Cols    int    // width  default 80.
	Rows    int    // height default 24.
	MaxRows int    // height of editor status on the terminal.

	History History

	Complete  func(line string) []string    // OPTIONAL; It takes the current user input and returns some completion suggestions.
	Help      func(line string) [][2]string // OPTIONAL; Print help.
	Hint      func(line string) string      // OPTIONAL; Hint will be called while user is typing and displayed on the right of the user input.
	WidthChar func(rune) int                // OPTIONAL; Calculates character width on the terminal. (A lot of CJK characters and emojis are twice as wide as ASCII characters.)
}

func NewTerminal(channel io.ReadWriteCloser, prompt string) *Terminal {
	return &Terminal{
		Inp:    bufio.NewReader(channel),
		Out:    bufio.NewWriter(channel),
		Raw:    channel,
		Prompt: prompt,
		Cols:   80,
		Rows:   24,
	}
}

// LineEditor reads user key strokes and returns a confirmed input line while displaying editor states on the terminal.
func (e *Terminal) LineEditor() (string, error) {
	if err := e.LineReset(); err != nil {
		return string(e.Buffer), err
	}

	for {
		r, _, err := e.Inp.ReadRune()
		if err != nil {
			return string(e.Buffer), err
		}

		switch r {
		case enter:
			return string(e.Buffer), nil
		case tab:
			err = e.completeLine()
		case '?':
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
			r1, _, err := e.Inp.ReadRune()
			if err != nil {
				return string(e.Buffer), err
			}

			switch r1 {
			case '[':
				r2, _, err := e.Inp.ReadRune()
				if err != nil {
					return string(e.Buffer), err
				}

				switch r2 {
				case '0', '1', '2', '4', '5', '6', '7', '8', '9':
					_, _, err = e.Inp.ReadRune()
				case '3':
					r4, _, err := e.Inp.ReadRune()
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
				r3, _, err := e.Inp.ReadRune()
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
			err = e.LineReset()
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
func (e *Terminal) Adjust() error {
	// https://groups.google.com/forum/#!topic/comp.os.vms/bDKSY6nG13k
	if _, err := e.Out.WriteString("\x1b7\x1b[999;999H\x1b[6n"); err != nil {
		return err
	}

	if err := e.Out.Flush(); err != nil {
		return err
	}

	res, err := e.Inp.ReadString('R')
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

func (e *Terminal) WriteOut(b []byte) (int, error) {
	e.notZero()
	ew := errWriter{w: e.Out}
	ew.writeString("\r\x1b[0K")
	ew.write(bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n")))
	ew.flush()
	if ew.err != nil {
		return 0, ew.err
	}
	return len(b), e.refreshLine()
}

func (e *Terminal) Write(buf []byte) (written int, err error) {
	for len(buf) > 0 {
		todo := len(buf)

		i := bytes.IndexByte(buf, '\n')
		if i >= 0 {
			todo = i
		}

		nn, err := e.Raw.Write(buf[:todo])
		written += nn
		if err != nil {
			return written, err
		}

		buf = buf[todo:]

		if i >= 0 {
			if _, err = e.Raw.Write([]byte{'\r', '\n'}); err != nil {
				return written, err
			}
			written++
			buf = buf[1:]
		}
	}
	return written, nil
}

func (e *Terminal) LineReset() error {
	e.notZero()
	e.Buffer = []rune{}
	e.OldCur = 0
	e.Cur = 0
	e.MaxRows = 0
	return e.refreshLine()
}

//

func (e *Terminal) notZero() {
	if e.Rows == 0 {
		e.Rows = 24
	}
	if e.Cols == 0 {
		e.Cols = 80
	}
}

func (e *Terminal) editBackspace() error {
	if e.Cur == 0 {
		return e.beep()
	}
	e.Cur--
	e.Buffer = e.Buffer[:e.Cur+copy(e.Buffer[e.Cur:], e.Buffer[e.Cur+1:])] // Delete https://github.com/golang/go/wiki/SliceTricks
	return e.refreshLine()
}

func (e *Terminal) editDelete() error {
	if e.Cur == len(e.Buffer) {
		return e.beep()
	}
	e.Buffer = e.Buffer[:e.Cur+copy(e.Buffer[e.Cur:], e.Buffer[e.Cur+1:])] // Delete https://github.com/golang/go/wiki/SliceTricks
	return e.refreshLine()
}

func (e *Terminal) editSwap() error {
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

func (e *Terminal) editMoveLeft() error {
	if e.Cur == 0 {
		return e.beep()
	}

	e.Cur--

	return e.refreshLine()
}

func (e *Terminal) editMoveRight() error {
	if e.Cur == len(e.Buffer) {
		return e.beep()
	}

	e.Cur++

	return e.refreshLine()
}

func (e *Terminal) editHistoryPrev() error {
	e.History.Save(string(e.Buffer))
	if err := e.History.Prev(); err != nil {
		return e.beep()
	}
	e.Buffer = []rune(e.History.Get())
	e.Cur = len(e.Buffer)
	return e.refreshLine()
}

func (e *Terminal) editHistoryNext() error {
	if err := e.History.Next(); err != nil {
		return e.beep()
	}
	e.Buffer = []rune(e.History.Get())
	e.Cur = len(e.Buffer)
	return e.refreshLine()
}

func (e *Terminal) editKillForward() error {
	e.Buffer = e.Buffer[:e.Cur]
	return e.refreshLine()
}

func (e *Terminal) editMoveHome() error {
	if e.Cur == 0 {
		return e.beep()
	}

	e.Cur = 0
	return e.refreshLine()
}

func (e *Terminal) editMoveEnd() error {
	if e.Cur == len(e.Buffer) {
		return e.beep()
	}

	e.Cur = len(e.Buffer)
	return e.refreshLine()
}

func (e *Terminal) editDeletePrevWord() error {
	var w bool
	var p int
	for i := e.Cur - 1; i >= 0; i-- {
		if e.Buffer[i] != ' ' {
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

func (e *Terminal) editInsert(r rune) error {
	// Insert https://github.com/golang/go/wiki/SliceTricks
	e.Buffer = append(e.Buffer, 0)
	copy(e.Buffer[e.Cur+1:], e.Buffer[e.Cur:])
	e.Buffer[e.Cur] = r

	e.Cur++
	return e.refreshLine()
}

//

func (e *Terminal) completeLine() error {
	if e.Complete == nil {
		return e.editInsert(tab)
	}

	var (
		opts     = e.Complete(string(e.Buffer))
		opts_len = len(opts)
	)
	switch opts_len {
	case 0:
		return e.beep()
	case 1:
		e.Buffer = []rune(opts[0])
		e.Cur = len(e.Buffer)
		return e.refreshLine()
	}
	// fmt.Fprintf(e.Out, "\n\r    %s\n", strings.Join(opts, "   ")); e.Out.Flush()
	// const size = 3
	// var tabl [][]string
	// for i := 0; i < opts_len; i += size {
	// tabl = append(tabl, opts[i:min(i+size, opts_len)])
	// }

	tw := new(tabwriter.Writer)
	tw.Init(e.Out, 0, 0, 4, ' ', 0)
	for chunk := range slices.Chunk(opts, 3) {
		fmt.Fprintf(tw, "\n\r    %s\t", strings.Join(chunk, "\t"))
	}
	fmt.Fprintln(tw)
	tw.Flush()

	return e.refreshLine()
	/*
		pos := 0
		for {
			c := opts[pos]

			if err := e.refreshLineByString(c); err != nil {
				return err
			}

			b, err := e.Inp.Peek(1)
			if err != nil {
				return err
			}

			switch b[0] {
			case tab:
				if _, _, err := e.Inp.ReadRune(); err != nil {
					return err
				}
				pos = (pos + len(opts) + 1) % len(opts)
			case esc:
				if _, _, err := e.Inp.ReadRune(); err != nil {
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
	// */
}

func (e *Terminal) printHelp() error {
	if e.Help == nil {
		return e.editInsert('?')
	}

	var (
		dict = e.Help(string(e.Buffer))
		tw   = new(tabwriter.Writer)
	)
	tw.Init(e.Out, 0, 0, 3, ' ', 0)
	for _, v := range dict {
		fmt.Fprintf(tw, "\n\r  %s\t%s\t", v[0], v[1])
	}
	fmt.Fprintln(tw)
	tw.Flush() // e.Out.Flush()

	return e.refreshLine()
}

func (e *Terminal) hint() string {
	if e.Hint == nil {
		return ""
	}
	return e.Hint(string(e.Buffer))
}

//

/*
// replace Buffer by String and refreshLine()
func (e *Terminal) refreshLineByString(s string) error {
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
// */

func (e *Terminal) refreshLine() error {
	type pos struct {
		cols, rows int
	}

	hintStr := e.hint()

	if e.WidthChar == nil {
		e.WidthChar = defaultWidth
	}

	//

	// var pw int
	// for _, r := range e.Prompt {
	// 	pw += e.WidthChar(r)
	// }
	pw := visualWidth([]rune(e.Prompt))

	var bw, cw, ocw int
	for i, r := range e.Buffer {
		if i < e.Cur {
			cw += e.WidthChar(r)
		}
		if i < e.OldCur {
			ocw += e.WidthChar(r)
		}
		bw += e.WidthChar(r)
	}

	var hw int
	for _, r := range hintStr {
		hw += e.WidthChar(r)
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
	ew.writeString(hintStr)
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
func visualWidth(runes []rune) (length int) {
	inEscSeq := false
	for _, r := range runes {
		switch {
		case inEscSeq:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscSeq = false
			}
		case r == '\x1b':
			inEscSeq = true
		default:
			length++
		}
	}
	return
}

//

func (e *Terminal) clearScreen() error {
	n, err := e.Out.WriteString("\x1b[H\x1b[2J")
	if err != nil {
		return err
	}
	if n != 7 {
		return errors.New("failed to clear screen")
	}
	return nil
}

func (e *Terminal) beep() error {
	if _, err := e.Out.WriteString("\a"); err != nil {
		return err
	}
	if err := e.Out.Flush(); err != nil {
		return err
	}
	return nil
}

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
