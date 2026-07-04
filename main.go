package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// -------😜--- view modes ----------

type viewMode int

const (
	modeHex viewMode = iota
	modeBin
	modeASCII
	modeUni
	modeTriASCII // hex + binary + ascii, side by side
	modeTriUni   // hex + binary + unicode, side by side
	modeCount
)

// tri reports whether the mode shows hex, binary, and a text pane together.
func (m viewMode) tri() bool { return m == modeTriASCII || m == modeTriUni }

func (m viewMode) String() string {
	switch m {
	case modeHex:
		return "HEX"
	case modeBin:
		return "BIN"
	case modeASCII:
		return "ASCII"
	case modeUni:
		return "UNICODE"
	case modeTriASCII:
		return "HEX+BIN+ASCII"
	case modeTriUni:
		return "HEX+BIN+UNICODE"
	}
	return "?"
}

// bytesPerRow for each mode, chosen to fit a standard 80+ column terminal.
func (m viewMode) bytesPerRow() int {
	switch m {
	case modeHex:
		return 16
	case modeBin:
		return 8
	case modeASCII:
		return 64
	case modeUni:
		return 32
	case modeTriASCII, modeTriUni:
		return 4
	}
	return 16
}

// rowWidth is the rendered width of a row of n bytes: offset column, cells
// (with hex group gaps), and the ASCII sidebar where the mode has one.
func (m viewMode) rowWidth(n int) int {
	const offset = 8 + 2
	switch m {
	case modeHex:
		return offset + 3*n + (n-1)/8 + n + 3
	case modeBin:
		return offset + 9*n + n + 3
	case modeASCII:
		return offset + n
	case modeUni:
		return offset + 2*n
	case modeTriASCII: // hex pane + │ + binary pane + │ + ascii pane
		return offset + 3*n + 2 + 9*n + 2 + n
	case modeTriUni:
		return offset + 3*n + 2 + 9*n + 2 + 2*n
	}
	return offset + 3*n
}

// units of input needed to compose one byte while editing. Tri modes never
// reach here: model.editForm resolves them to the active pane's mode first.
func (m viewMode) editUnits() int {
	switch m {
	case modeHex:
		return 2
	case modeBin:
		return 8
	default:
		return 1
	}
}

// editPane selects which pane of a tri view receives edit input.
type editPane int

const (
	paneHex editPane = iota
	paneBin
	paneText
	paneCount
)

// ---------- model ----------

type editStep struct {
	off int
	old byte
}

// edit is one overwritten byte pending save: the value shown on screen and
// the byte on disk underneath it. Entries whose val equals orig are removed
// as they occur, so len(model.edits) is always the dirty count.
type edit struct {
	val, orig byte
}

const winSize = 64 * 1024

// fileBuf reads the file through a sliding window instead of holding it in
// memory. It is held by pointer so the cached window survives the value
// copies Bubble Tea makes of the model on every Update/View.
type fileBuf struct {
	f        *os.File
	size     int
	readOnly bool
	win      []byte // cached bytes at [winOff, winOff+len(win))
	winOff   int
	err      error // last read failure, surfaced in the status line
}

// at returns the byte at off as stored on disk when the file was read.
func (b *fileBuf) at(off int) byte {
	if off < 0 || off >= b.size {
		return 0
	}
	if off < b.winOff || off >= b.winOff+len(b.win) {
		b.load(off)
	}
	if off < b.winOff || off >= b.winOff+len(b.win) {
		return 0 // read failed; b.err is set
	}
	return b.win[off-b.winOff]
}

// load replaces the window with the winSize-aligned chunk containing off.
func (b *fileBuf) load(off int) {
	start := off - off%winSize
	n := min(winSize, b.size-start)
	if cap(b.win) < n {
		b.win = make([]byte, n)
	}
	got, err := b.f.ReadAt(b.win[:n], int64(start))
	if got < n && err != nil {
		b.err = err
	}
	b.win = b.win[:got]
	b.winOff = start
}

type model struct {
	path string

	buf   *fileBuf
	edits map[int]edit // pending overwrites by offset; len() is the dirty count

	cursor int
	top    int // first visible row
	mode   viewMode
	pane   editPane // which tri-view pane takes edit input; tab cycles it
	fit    bool     // widen rows to fill the terminal instead of the 80-col default

	editing  bool
	pending  int // value being composed from nibbles/bits
	pendingN int // units entered so far

	undo []editStep

	width, height int
	status        string
	quitArmed     bool
}

var (
	styOffset  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styCursor  = lipgloss.NewStyle().Reverse(true)
	styEditing = lipgloss.NewStyle().Reverse(true).Foreground(lipgloss.Color("11"))
	styChanged = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styHeader  = lipgloss.NewStyle().Bold(true)
	styMode    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	styStatus  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styHelp    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func initialModel(path string) (model, error) {
	info, err := os.Stat(path)
	if err != nil {
		return model{}, err
	}
	if info.IsDir() {
		return model{}, fmt.Errorf("%s is a directory", path)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	readOnly := false
	if err != nil {
		if f, err = os.Open(path); err != nil {
			return model{}, err
		}
		readOnly = true
	}
	return model{
		path:  path,
		buf:   &fileBuf{f: f, size: int(info.Size()), readOnly: readOnly},
		edits: make(map[int]edit),
		mode:  modeHex,
	}, nil
}

func (m model) Init() tea.Cmd { return nil }

// ---------- geometry helpers ----------

func (m *model) visRows() int {
	return max(m.height-3, 1) // header + status + help
}

// bytesPerRow is the mode's traditional width; with fit on, rows grow in
// 8-byte groups to fill the terminal (never shrinking below the default).
func (m *model) bytesPerRow() int {
	bpr := m.mode.bytesPerRow()
	if m.fit {
		step := 8
		if m.mode.tri() { // tri rows are ~13 cols/byte; 8-byte jumps overshoot
			step = 4
		}
		for n := bpr + step; m.mode.rowWidth(n) <= m.width; n += step {
			bpr = n
		}
	}
	return bpr
}

func (m *model) rowCount() int {
	bpr := m.bytesPerRow()
	return max((m.buf.size+bpr-1)/bpr, 1)
}

func (m *model) clamp() {
	if m.buf.size == 0 {
		m.cursor = 0
	} else if m.cursor < 0 {
		m.cursor = 0
	} else if m.cursor >= m.buf.size {
		m.cursor = m.buf.size - 1
	}
	bpr := m.bytesPerRow()
	row := m.cursor / bpr
	if row < m.top {
		m.top = row
	}
	if row >= m.top+m.visRows() {
		m.top = row - m.visRows() + 1
	}
	maxTop := max(m.rowCount()-m.visRows(), 0)
	if m.top > maxTop {
		m.top = maxTop
	}
	if m.top < 0 {
		m.top = 0
	}
}

// ---------- editing ----------

// byteAt returns the byte at off as displayed: pending edits win over disk.
func (m model) byteAt(off int) byte {
	if e, ok := m.edits[off]; ok {
		return e.val
	}
	return m.buf.at(off)
}

// bytesAt copies [start,end), clamped to the file, with pending edits applied.
func (m model) bytesAt(start, end int) []byte {
	end = min(end, m.buf.size)
	if start >= end {
		return nil
	}
	out := make([]byte, end-start)
	for i := range out {
		out[i] = m.byteAt(start + i)
	}
	return out
}

func (m *model) writeByte(off int, val byte) {
	cur := m.byteAt(off)
	if cur == val {
		return
	}
	m.undo = append(m.undo, editStep{off, cur})
	m.setByte(off, val)
}

// setByte records val in the overlay, dropping the entry when the byte
// returns to its on-disk value so len(edits) stays the dirty count.
func (m *model) setByte(off int, val byte) {
	orig := m.buf.at(off)
	if e, ok := m.edits[off]; ok {
		orig = e.orig
	}
	if val == orig {
		delete(m.edits, off)
	} else {
		m.edits[off] = edit{val, orig}
	}
}

func (m *model) undoLast() {
	if len(m.undo) == 0 {
		m.status = "nothing to undo"
		return
	}
	step := m.undo[len(m.undo)-1]
	m.undo = m.undo[:len(m.undo)-1]
	m.setByte(step.off, step.old)
	m.cursor = step.off
	m.status = fmt.Sprintf("undid edit at 0x%x", step.off)
}

// save writes the edited bytes back in place — contiguous runs in one
// WriteAt each — rather than rewriting the file.
func (m *model) save() {
	if m.buf.readOnly {
		f, err := os.OpenFile(m.path, os.O_RDWR, 0)
		if err != nil {
			m.status = "save failed: " + err.Error()
			return
		}
		m.buf.f.Close()
		m.buf.f, m.buf.readOnly = f, false
	}
	offs := make([]int, 0, len(m.edits))
	for off := range m.edits {
		offs = append(offs, off)
	}
	sort.Ints(offs)
	n := len(offs)
	for i := 0; i < len(offs); {
		j := i
		for j+1 < len(offs) && offs[j+1] == offs[j]+1 {
			j++
		}
		run := make([]byte, j-i+1)
		for k := i; k <= j; k++ {
			run[k-i] = m.edits[offs[k]].val
		}
		if _, err := m.buf.f.WriteAt(run, int64(offs[i])); err != nil {
			m.status = "save failed: " + err.Error()
			return // unwritten offsets stay in m.edits and remain dirty
		}
		for k := i; k <= j; k++ {
			delete(m.edits, offs[k])
		}
		i = j + 1
	}
	m.buf.win = m.buf.win[:0] // cached window may predate the writes
	m.status = fmt.Sprintf("wrote %d changed byte(s) to %s", n, m.path)
}

// editForm is the mode whose input rules editing follows: the view mode
// itself, or for tri modes the mode of the selected pane.
func (m model) editForm() viewMode {
	if !m.mode.tri() {
		return m.mode
	}
	switch m.pane {
	case paneBin:
		return modeBin
	case paneText:
		if m.mode == modeTriUni {
			return modeUni
		}
		return modeASCII
	}
	return modeHex
}

// feed one unit (nibble, bit, or char) of edit input; returns true if consumed.
func (m *model) feedEdit(r rune) bool {
	if m.buf.size == 0 {
		return false
	}
	switch m.editForm() {
	case modeHex:
		v := hexVal(r)
		if v < 0 {
			return false
		}
		m.pending = m.pending<<4 | v
		m.pendingN++
	case modeBin:
		if r != '0' && r != '1' {
			return false
		}
		m.pending = m.pending<<1 | int(r-'0')
		m.pendingN++
	default: // ASCII / unicode: one printable rune replaces the byte
		if r > 0xff {
			m.status = fmt.Sprintf("%q doesn't fit in one byte (U+%04X)", r, r)
			return true
		}
		m.pending = int(r)
		m.pendingN = 1
	}
	if m.pendingN >= m.editForm().editUnits() {
		m.writeByte(m.cursor, byte(m.pending))
		m.pending, m.pendingN = 0, 0
		if m.cursor < m.buf.size-1 {
			m.cursor++
		}
	}
	return true
}

func hexVal(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return -1
}

// ---------- update ----------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
		return m, nil

	case tea.KeyMsg:
		m.status = ""
		key := msg.String()
		if key != "q" && key != "ctrl+c" {
			m.quitArmed = false
		}
		bpr := m.bytesPerRow()
		page := m.visRows() * bpr

		// Editing-mode input first: printable runes compose the new byte value.
		if m.editing && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			if m.feedEdit(msg.Runes[0]) {
				m.clamp()
				return m, nil
			}
		}

		switch key {
		case "ctrl+c", "q":
			if m.editing && key == "q" {
				break // 'q' is edit input in ascii mode; handled above, ignore here
			}
			if len(m.edits) > 0 && !m.quitArmed {
				m.quitArmed = true
				m.status = fmt.Sprintf("%d unsaved byte(s) — press again to quit without saving, or ctrl+s to save", len(m.edits))
				return m, nil
			}
			return m, tea.Quit

		case "ctrl+s":
			m.save()

		case "esc":
			if m.pendingN > 0 {
				m.pending, m.pendingN = 0, 0
				m.status = "edit cancelled"
			} else if m.editing {
				m.editing = false
			}

		case "enter", "i":
			if !m.editing {
				if m.buf.size == 0 {
					m.status = "file is empty — nothing to edit"
				} else {
					m.editing = true
				}
			} else if key == "i" {
				// in edit mode 'i' may be hex input (handled above) or ignored
			}

		case "tab":
			if m.editing && m.mode.tri() {
				m.setPane((m.pane + 1) % paneCount)
			} else {
				m.setMode((m.mode + 1) % modeCount)
			}
		case "shift+tab":
			if m.editing && m.mode.tri() {
				m.setPane((m.pane + paneCount - 1) % paneCount)
			} else {
				m.setMode((m.mode + modeCount - 1) % modeCount)
			}

		case "left", "h":
			if key == "left" || !m.editing {
				m.pending, m.pendingN = 0, 0
				m.cursor--
			}
		case "right", "l":
			if key == "right" || !m.editing {
				m.pending, m.pendingN = 0, 0
				m.cursor++
			}
		case "up", "k":
			if key == "up" || !m.editing {
				m.pending, m.pendingN = 0, 0
				m.cursor -= bpr
			}
		case "down", "j":
			if key == "down" || !m.editing {
				m.pending, m.pendingN = 0, 0
				m.cursor += bpr
			}
		case "pgup", "ctrl+u":
			m.pending, m.pendingN = 0, 0
			m.cursor -= page
			m.top -= m.visRows()
		case "pgdown", "ctrl+d":
			m.pending, m.pendingN = 0, 0
			m.cursor += page
			m.top += m.visRows()
		case "home":
			m.cursor -= m.cursor % bpr
		case "end":
			m.cursor += bpr - 1 - m.cursor%bpr
		case "g":
			if !m.editing {
				m.cursor = 0
			}
		case "G":
			if !m.editing {
				m.cursor = m.buf.size - 1
			}
		case "u":
			if !m.editing {
				m.undoLast()
			}
		case "w":
			if !m.editing {
				m.fit = !m.fit
				m.top = m.cursor / m.bytesPerRow() // keep cursor on screen; clamp() refines
				if m.fit {
					m.status = fmt.Sprintf("fit width: %d bytes/row", m.bytesPerRow())
				} else {
					m.status = fmt.Sprintf("fixed width: %d bytes/row", m.bytesPerRow())
				}
			}
		case "1", "2", "3", "4", "5", "6":
			if !m.editing {
				m.setMode(viewMode(key[0] - '1'))
			}
		}
		m.clamp()
	}
	return m, nil
}

// setPane switches which tri-view pane takes edit input, discarding any
// partially composed byte since the input units differ between panes.
func (m *model) setPane(p editPane) {
	m.pane = p
	m.pending, m.pendingN = 0, 0
	m.status = "editing " + m.editForm().String() + " pane"
}

func (m *model) setMode(mode viewMode) {
	m.mode = mode
	m.pending, m.pendingN = 0, 0
	bpr := m.bytesPerRow()
	m.top = m.cursor / bpr // keep cursor on screen; clamp() refines
	m.clamp()
}

// ---------- view ----------

func (m model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	var b strings.Builder

	// header
	dirty := ""
	if len(m.edits) > 0 {
		dirty = styChanged.Render(fmt.Sprintf("  [+%d modified]", len(m.edits)))
	}
	editFlag := ""
	if m.editing {
		label := " EDIT "
		if m.mode.tri() {
			label = " EDIT:" + m.editForm().String() + " "
		}
		editFlag = styEditing.Render(label)
	}
	fit := ""
	if m.fit {
		fit = styDim.Render(fmt.Sprintf(" fit:%d/row", m.bytesPerRow()))
	}
	header := fmt.Sprintf("%s  %s  %s%s%s %s",
		styHeader.Render(m.path),
		styDim.Render(fmt.Sprintf("%d bytes", m.buf.size)),
		styMode.Render("["+m.mode.String()+"]"),
		fit, dirty, editFlag)
	b.WriteString(truncate(header, m.width))
	b.WriteString("\n")

	// body
	bpr := m.bytesPerRow()
	rows := m.visRows()
	uni := map[int]string(nil)
	if m.mode == modeUni || m.mode == modeTriUni {
		uni = m.uniCells(m.top*bpr, min((m.top+rows)*bpr, m.buf.size))
	}
	for r := range rows {
		row := m.top + r
		off := row * bpr
		if off >= m.buf.size && !(off == 0 && m.buf.size == 0) {
			b.WriteString("\n")
			continue
		}
		b.WriteString(m.renderRow(off, bpr, uni))
		b.WriteString("\n")
	}

	// status line
	b.WriteString(truncate(m.statusLine(), m.width))
	b.WriteString("\n")
	// help line
	b.WriteString(truncate(styHelp.Render(m.helpLine()), m.width))
	return b.String()
}

func (m model) statusLine() string {
	if m.status != "" {
		return styStatus.Render(m.status)
	}
	if m.buf.err != nil {
		return styChanged.Render("read error: " + m.buf.err.Error())
	}
	if m.buf.size == 0 {
		return styDim.Render("(empty file)")
	}
	v := m.byteAt(m.cursor)
	ch := "·"
	if v >= 0x20 && v < 0x7f {
		ch = string(rune(v))
	}
	return fmt.Sprintf("offset 0x%08x (%d)   byte 0x%02x  %3d  0b%08b  %q",
		m.cursor, m.cursor, v, v, v, ch)
}

func (m model) helpLine() string {
	if m.editing {
		var s string
		switch m.editForm() {
		case modeHex:
			s = "EDIT: type hex digits (2 per byte)"
		case modeBin:
			s = "EDIT: type bits 0/1 (8 per byte)"
		default:
			s = "EDIT: type a character to overwrite byte"
		}
		if m.mode.tri() {
			s += " · tab next pane"
		}
		return s + " · arrows move · esc done · ctrl+s save"
	}
	return "↑↓←→/hjkl move · pgup/pgdn · g/G start/end · tab or 1-6 view · w fit width · enter edit · u undo · ctrl+s save · q quit"
}

// renderRow renders one row of bytes starting at offset off.
func (m model) renderRow(off, bpr int, uni map[int]string) string {
	if m.mode.tri() {
		return m.renderTriRow(off, bpr, uni)
	}
	var b strings.Builder
	b.WriteString(styOffset.Render(fmt.Sprintf("%08x", off)))
	b.WriteString("  ")

	end := off + bpr
	for i := off; i < end; i++ {
		if m.mode == modeHex && i > off && (i-off)%8 == 0 {
			b.WriteString(" ")
		}
		if i >= m.buf.size {
			b.WriteString(strings.Repeat(" ", m.cellWidth()))
			if m.mode == modeHex || m.mode == modeBin {
				b.WriteString(" ")
			}
			continue
		}
		b.WriteString(m.renderCell(i, uni))
		if m.mode == modeHex || m.mode == modeBin {
			b.WriteString(" ")
		}
	}

	// ASCII sidebar for hex and binary modes
	if m.mode == modeHex || m.mode == modeBin {
		b.WriteString(" ")
		b.WriteString(styDim.Render("|"))
		for i := off; i < end && i < m.buf.size; i++ {
			ch := "·"
			if v := m.byteAt(i); v >= 0x20 && v < 0x7f {
				ch = string(rune(v))
			}
			b.WriteString(m.styleFor(i, false).Render(ch))
		}
		b.WriteString(styDim.Render("|"))
	}
	return b.String()
}

// renderTriRow renders one row as three panes over the same bytes — hex,
// binary, and ascii or unicode — separated by │. The pane selected for edit
// input (model.pane) shows partial input and the editing highlight; the
// cursor highlights in all three.
func (m model) renderTriRow(off, bpr int, uni map[int]string) string {
	var b strings.Builder
	b.WriteString(styOffset.Render(fmt.Sprintf("%08x", off)))
	b.WriteString("  ")
	end := off + bpr
	partial := func(i int, pane editPane) bool {
		return m.editing && i == m.cursor && m.pendingN > 0 && m.pane == pane
	}

	for i := off; i < end; i++ {
		if i >= m.buf.size {
			b.WriteString("   ")
			continue
		}
		s := fmt.Sprintf("%02x", m.byteAt(i))
		if partial(i, paneHex) {
			s = fmt.Sprintf("%x_", m.pending)
		}
		b.WriteString(m.styleFor(i, m.pane == paneHex).Render(s))
		b.WriteString(" ")
	}
	b.WriteString(styDim.Render("│"))
	b.WriteString(" ")

	for i := off; i < end; i++ {
		if i >= m.buf.size {
			b.WriteString(strings.Repeat(" ", 9))
			continue
		}
		s := fmt.Sprintf("%08b", m.byteAt(i))
		if partial(i, paneBin) {
			s = fmt.Sprintf("%0*b", m.pendingN, m.pending) + strings.Repeat("_", 8-m.pendingN)
		}
		b.WriteString(m.styleFor(i, m.pane == paneBin).Render(s))
		b.WriteString(" ")
	}
	b.WriteString(styDim.Render("│"))
	b.WriteString(" ")

	for i := off; i < end && i < m.buf.size; i++ {
		var s string
		if m.mode == modeTriUni {
			if s = uni[i]; s == "" {
				s = "· "
			}
		} else {
			s = "·"
			if v := m.byteAt(i); v >= 0x20 && v < 0x7f {
				s = string(rune(v))
			}
		}
		b.WriteString(m.styleFor(i, m.pane == paneText).Render(s))
	}
	return b.String()
}

func (m model) cellWidth() int {
	switch m.mode {
	case modeHex:
		return 2
	case modeBin:
		return 8
	case modeASCII:
		return 1
	case modeUni:
		return 2
	}
	return 2
}

func (m model) styleFor(i int, mainPanel bool) lipgloss.Style {
	switch {
	case i == m.cursor && m.editing && mainPanel:
		return styEditing
	case i == m.cursor:
		return styCursor
	}
	if _, edited := m.edits[i]; edited {
		return styChanged
	}
	return lipgloss.NewStyle()
}

func (m model) renderCell(i int, uni map[int]string) string {
	v := m.byteAt(i)
	var s string
	partial := m.editing && i == m.cursor && m.pendingN > 0
	switch m.mode {
	case modeHex:
		if partial {
			s = fmt.Sprintf("%x_", m.pending)
		} else {
			s = fmt.Sprintf("%02x", v)
		}
	case modeBin:
		if partial {
			s = fmt.Sprintf("%0*b", m.pendingN, m.pending) + strings.Repeat("_", 8-m.pendingN)
		} else {
			s = fmt.Sprintf("%08b", v)
		}
	case modeASCII:
		if v >= 0x20 && v < 0x7f {
			s = string(rune(v))
		} else {
			s = "·"
		}
	case modeUni:
		s = uni[i]
		if s == "" {
			s = "· "
		}
	}
	return m.styleFor(i, true).Render(s)
}

// uniCells decodes UTF-8 over [start,end) and returns a 2-column-wide cell
// string per byte offset: the rune at its first byte, "· " for continuation
// bytes, and "✗ " for bytes that aren't valid UTF-8.
func (m model) uniCells(start, end int) map[int]string {
	cells := make(map[int]string, end-start)
	if start >= m.buf.size {
		return cells
	}
	// materialize the range plus ≤3 bytes either side: back-up so sequences
	// straddling the window start decode correctly, look-ahead so a rune
	// beginning just before end isn't cut short
	lo := max(start-3, 0)
	buf := m.bytesAt(lo, end+3)
	p := start
	for p > lo && buf[p-lo]&0xc0 == 0x80 {
		p--
	}
	for p < end {
		r, size := utf8.DecodeRune(buf[p-lo:])
		if r == utf8.RuneError && size == 1 {
			cells[p] = "✗ "
			p++
			continue
		}
		cell := "· "
		if unicode.IsPrint(r) {
			cell = runewidth.FillRight(string(r), 2)
			if runewidth.StringWidth(string(r)) > 2 {
				cell = "? "
			}
		}
		cells[p] = cell
		for j := 1; j < size; j++ {
			cells[p+j] = "· "
		}
		p += size
	}
	return cells
}

func truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// ---------- main ----------

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: hexed <file>")
		os.Exit(2)
	}
	m, err := initialModel(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "hexed:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "hexed:", err)
		os.Exit(1)
	}
}
