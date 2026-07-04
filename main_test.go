package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func press(m model, msgs ...tea.Msg) model {
	for _, msg := range msgs {
		nm, _ := m.Update(msg)
		m = nm.(model)
	}
	return m
}

func testFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHexEditAndSave(t *testing.T) {
	path := testFile(t, []byte("hello world"))
	m, err := initialModel(path)
	if err != nil {
		t.Fatal(err)
	}
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	// move right twice, enter edit mode, type "ff"
	m = press(m,
		tea.KeyMsg{Type: tea.KeyRight},
		tea.KeyMsg{Type: tea.KeyRight},
		tea.KeyMsg{Type: tea.KeyEnter},
		keyRune('f'), keyRune('f'),
	)
	if got := m.byteAt(2); got != 0xff {
		t.Fatalf("byteAt(2) = 0x%02x, want 0xff", got)
	}
	if m.cursor != 3 {
		t.Fatalf("cursor = %d, want 3 (advance after full byte)", m.cursor)
	}
	if len(m.edits) != 1 {
		t.Fatalf("dirty count = %d, want 1", len(m.edits))
	}

	// save and verify on disk
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	got, _ := os.ReadFile(path)
	want := []byte("he\xfflo world")
	if string(got) != string(want) {
		t.Fatalf("file = %q, want %q", got, want)
	}
	if len(m.edits) != 0 {
		t.Fatalf("dirty count after save = %d, want 0", len(m.edits))
	}
}

func TestASCIIEditAndUndo(t *testing.T) {
	path := testFile(t, []byte("abc"))
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	// switch to ASCII view (key "3"), edit first byte to 'Z'
	m = press(m, keyRune('3'), tea.KeyMsg{Type: tea.KeyEnter}, keyRune('Z'))
	if got := m.byteAt(0); got != 'Z' {
		t.Fatalf("byteAt(0) = %q, want 'Z'", got)
	}

	// leave edit mode, undo
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc}, keyRune('u'))
	if got := m.byteAt(0); got != 'a' {
		t.Fatalf("after undo byteAt(0) = %q, want 'a'", got)
	}
	if len(m.edits) != 0 {
		t.Fatalf("dirty count after undo = %d, want 0", len(m.edits))
	}
}

func TestBinaryEdit(t *testing.T) {
	path := testFile(t, []byte{0x00})
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	// binary view, edit mode, type 8 bits: 10100101 = 0xa5
	m = press(m, keyRune('2'), tea.KeyMsg{Type: tea.KeyEnter})
	for _, bit := range "10100101" {
		m = press(m, keyRune(bit))
	}
	if got := m.byteAt(0); got != 0xa5 {
		t.Fatalf("byteAt(0) = 0x%02x, want 0xa5", got)
	}
}

func TestQuitGuardWhenDirty(t *testing.T) {
	path := testFile(t, []byte("x"))
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter}, keyRune('0'), keyRune('0'), tea.KeyMsg{Type: tea.KeyEsc})

	nm, cmd := m.Update(keyRune('q'))
	m = nm.(model)
	if cmd != nil {
		t.Fatal("first q with unsaved changes should not quit")
	}
	if !strings.Contains(m.status, "unsaved") {
		t.Fatalf("status = %q, want unsaved warning", m.status)
	}
	_, cmd = m.Update(keyRune('q'))
	if cmd == nil {
		t.Fatal("second q should quit")
	}
}

func TestViewRendersAllModes(t *testing.T) {
	// include multibyte UTF-8 and an invalid byte for the unicode view
	path := testFile(t, append([]byte("héllo wörld — ok"), 0xff, 0x41))
	m, _ := initialModel(path)
	// wide enough that the long temp-dir path doesn't truncate the header
	m = press(m, tea.WindowSizeMsg{Width: 250, Height: 20})

	for mode := range modeCount {
		m = press(m, keyRune(rune('1'+mode)))
		out := m.View()
		if out == "" {
			t.Fatalf("empty view in mode %s", mode)
		}
		if !strings.Contains(out, mode.String()) {
			t.Fatalf("view in mode %s missing mode label", mode)
		}
	}
}

func TestTriModeShowsAllThreePanes(t *testing.T) {
	path := testFile(t, []byte("AB"))
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 20})

	m = press(m, keyRune('5')) // HEX+BIN+ASCII
	out := m.View()
	for _, want := range []string{"41 42", "01000001 01000010", "AB"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tri view missing %q:\n%s", want, out)
		}
	}

	m = press(m, keyRune('6')) // HEX+BIN+UNICODE
	if out := m.View(); !strings.Contains(out, "01000001 01000010") {
		t.Fatalf("tri-unicode view missing binary pane:\n%s", out)
	}
}

func TestTriModeHexEdit(t *testing.T) {
	path := testFile(t, []byte("ab"))
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 20})

	// tri mode edits like hex: two nibbles per byte, then auto-advance
	m = press(m, keyRune('5'), tea.KeyMsg{Type: tea.KeyEnter}, keyRune('f'), keyRune('f'))
	if got := m.byteAt(0); got != 0xff {
		t.Fatalf("byteAt(0) = 0x%02x, want 0xff", got)
	}
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (advance after full byte)", m.cursor)
	}
}

func TestTriModeEditPaneSwitching(t *testing.T) {
	path := testFile(t, []byte("abcd"))
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 20})

	// tri mode starts editing in the hex pane
	m = press(m, keyRune('5'), tea.KeyMsg{Type: tea.KeyEnter}, keyRune('f'), keyRune('f'))
	if got := m.byteAt(0); got != 0xff {
		t.Fatalf("hex pane: byteAt(0) = 0x%02x, want 0xff", got)
	}

	// tab moves input to the binary pane: 8 bits per byte
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	for _, bit := range "10100101" {
		m = press(m, keyRune(bit))
	}
	if got := m.byteAt(1); got != 0xa5 {
		t.Fatalf("bin pane: byteAt(1) = 0x%02x, want 0xa5", got)
	}

	// tab again: text pane, one character per byte
	m = press(m, tea.KeyMsg{Type: tea.KeyTab}, keyRune('Z'))
	if got := m.byteAt(2); got != 'Z' {
		t.Fatalf("text pane: byteAt(2) = %q, want 'Z'", got)
	}

	// tab wraps back around to the hex pane
	m = press(m, tea.KeyMsg{Type: tea.KeyTab}, keyRune('0'), keyRune('0'))
	if got := m.byteAt(3); got != 0x00 {
		t.Fatalf("wrapped hex pane: byteAt(3) = 0x%02x, want 0x00", got)
	}

	// outside edit mode tab still cycles the view mode
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc}, tea.KeyMsg{Type: tea.KeyTab})
	if m.mode != modeTriUni {
		t.Fatalf("mode after tab = %s, want %s", m.mode, modeTriUni)
	}
}

func TestTriModePaneSwitchDiscardsPartialInput(t *testing.T) {
	path := testFile(t, []byte{0x41})
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 20})

	// one nibble entered, then a pane switch: the half-typed byte is dropped
	m = press(m, keyRune('5'), tea.KeyMsg{Type: tea.KeyEnter}, keyRune('f'), tea.KeyMsg{Type: tea.KeyTab})
	if m.pendingN != 0 {
		t.Fatalf("pendingN after pane switch = %d, want 0", m.pendingN)
	}
	if got := m.byteAt(0); got != 0x41 {
		t.Fatalf("byteAt(0) = 0x%02x, want unchanged 0x41", got)
	}
}

func TestTriModeFitWidth(t *testing.T) {
	data := make([]byte, 256)
	path := testFile(t, data)
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 120, Height: 20})

	// tri rows cost 13 cols/byte + 14 overhead; 120 cols fits 8 bytes/row
	m = press(m, keyRune('5'), keyRune('w'))
	if got := m.bytesPerRow(); got != 8 {
		t.Fatalf("tri fit bytesPerRow = %d, want 8", got)
	}
	for line := range strings.SplitSeq(m.View(), "\n") {
		if w := lipgloss.Width(line); w > 120 {
			t.Fatalf("rendered line is %d cols wide: %q", w, line)
		}
	}
}

func TestScrolling(t *testing.T) {
	data := make([]byte, 16*100) // one hundred hex rows
	path := testFile(t, data)
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 20})

	m = press(m, keyRune('G')) // jump to end
	if m.cursor != len(data)-1 {
		t.Fatalf("cursor = %d, want %d", m.cursor, len(data)-1)
	}
	if m.top == 0 {
		t.Fatal("view did not scroll to show cursor")
	}
	m = press(m, keyRune('g'))
	if m.cursor != 0 || m.top != 0 {
		t.Fatalf("after g: cursor=%d top=%d, want 0,0", m.cursor, m.top)
	}
}

func TestFitWidth(t *testing.T) {
	data := make([]byte, 16*100)
	path := testFile(t, data)
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 200, Height: 20})

	// 200 cols in hex mode fits 40 bytes/row (multiples of 8)
	m = press(m, keyRune('w'))
	if got := m.bytesPerRow(); got != 40 {
		t.Fatalf("fit bytesPerRow = %d, want 40", got)
	}
	if w := modeHex.rowWidth(m.bytesPerRow()); w > 200 {
		t.Fatalf("fitted row renders %d cols, exceeds 200", w)
	}

	// vertical movement uses the widened row
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 40 {
		t.Fatalf("cursor after down = %d, want 40", m.cursor)
	}

	// rendered rows must not exceed the terminal width
	for line := range strings.SplitSeq(m.View(), "\n") {
		if w := lipgloss.Width(line); w > 200 {
			t.Fatalf("rendered line is %d cols wide: %q", w, line)
		}
	}

	// toggling off restores the traditional layout
	m = press(m, keyRune('w'))
	if got := m.bytesPerRow(); got != 16 {
		t.Fatalf("bytesPerRow after toggle off = %d, want 16", got)
	}

	// fit never shrinks below the traditional width on narrow terminals
	m = press(m, tea.WindowSizeMsg{Width: 60, Height: 20}, keyRune('w'))
	if got := m.bytesPerRow(); got != 16 {
		t.Fatalf("fit bytesPerRow at 60 cols = %d, want 16", got)
	}
}

func TestFitWidthIgnoredWhileEditing(t *testing.T) {
	path := testFile(t, []byte("abcdef"))
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 200, Height: 20})

	// ASCII mode: 'w' while editing is edit input, not a layout toggle
	m = press(m, keyRune('3'), tea.KeyMsg{Type: tea.KeyEnter}, keyRune('w'))
	if m.fit {
		t.Fatal("'w' during ASCII edit toggled fit mode")
	}
	if got := m.byteAt(0); got != 'w' {
		t.Fatalf("byteAt(0) = %q, want 'w'", got)
	}

	// hex mode: 'w' is not a hex digit and must not toggle fit either
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc}, keyRune('1'), tea.KeyMsg{Type: tea.KeyEnter}, keyRune('w'))
	if m.fit {
		t.Fatal("'w' during hex edit toggled fit mode")
	}
}

func TestLargeFileWindowing(t *testing.T) {
	data := make([]byte, winSize*3+123)
	for i := range data {
		data[i] = byte(i)
	}
	path := testFile(t, data)
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 20})

	// jump to the end: bytes past the first window read correctly
	m = press(m, keyRune('G'))
	last := len(data) - 1
	if got := m.byteAt(last); got != data[last] {
		t.Fatalf("byteAt(%d) = 0x%02x, want 0x%02x", last, got, data[last])
	}
	if len(m.buf.win) > winSize {
		t.Fatalf("window holds %d bytes, want ≤ %d", len(m.buf.win), winSize)
	}

	// overwrite the last byte and save in place
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter}, keyRune('0'), keyRune('0'))
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if len(m.edits) != 0 {
		t.Fatalf("dirty count after save = %d, want 0", len(m.edits))
	}
	got, _ := os.ReadFile(path)
	if got[last] != 0x00 {
		t.Fatalf("file[%d] = 0x%02x, want 0x00", last, got[last])
	}
	got[last] = data[last]
	if string(got) != string(data) {
		t.Fatal("save changed bytes other than the edited one")
	}
	// saved value now reads back through the (invalidated) window
	if v := m.byteAt(last); v != 0x00 {
		t.Fatalf("byteAt(%d) after save = 0x%02x, want 0x00", last, v)
	}
}

func TestEditBackToOriginalClearsDirty(t *testing.T) {
	path := testFile(t, []byte{0x41})
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 20})

	m = press(m, tea.KeyMsg{Type: tea.KeyEnter}, keyRune('f'), keyRune('f'))
	if len(m.edits) != 1 {
		t.Fatalf("dirty count = %d, want 1", len(m.edits))
	}
	// retype the original value: the overlay entry must disappear
	m = press(m, keyRune('4'), keyRune('1'))
	if len(m.edits) != 0 {
		t.Fatalf("dirty count after restoring byte = %d, want 0", len(m.edits))
	}
	if got := m.byteAt(0); got != 0x41 {
		t.Fatalf("byteAt(0) = 0x%02x, want 0x41", got)
	}
}

func TestEmptyFile(t *testing.T) {
	path := testFile(t, nil)
	m, _ := initialModel(path)
	m = press(m, tea.WindowSizeMsg{Width: 100, Height: 20})
	// should not panic navigating or trying to edit
	m = press(m,
		tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyRight},
		tea.KeyMsg{Type: tea.KeyEnter}, keyRune('f'),
		keyRune('4'), // unicode mode
	)
	_ = m.View()
}
