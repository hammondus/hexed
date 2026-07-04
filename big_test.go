package main

// Temporary stress test: drive the app headlessly against a multi-GiB file.
// Run with: HEXED_BIG_FILE=/path/to/big.bin go test -run TestBigFile -v

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// plain strips ANSI styling so markers straddling the styled cursor cell match.
func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

func heapMB() float64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapAlloc) / (1 << 20)
}

func TestBigFile(t *testing.T) {
	path := os.Getenv("HEXED_BIG_FILE")
	if path == "" {
		t.Skip("set HEXED_BIG_FILE to run")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	size := int(info.Size())
	t.Logf("file size: %d bytes (%.1f GiB)", size, float64(size)/(1<<30))
	baseline := heapMB()

	start := time.Now()
	m, err := initialModel(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("open: %v, heap now %.1f MB (baseline %.1f MB)", time.Since(start), heapMB(), baseline)

	m = press(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// first render shows the start marker
	start = time.Now()
	out := m.View()
	t.Logf("first View(): %v", time.Since(start))
	if !strings.Contains(plain(out), "HEXED-START-MARK") {
		t.Fatal("start marker not visible in first screen")
	}

	// jump to the middle marker: position cursor via pgdn-free path — set
	// cursor directly like the 'g/G' handlers do, then clamp through Update
	m.cursor = size / 2
	m = press(m, tea.KeyMsg{Type: tea.KeyRight}) // forces clamp/scroll
	start = time.Now()
	out = m.View()
	t.Logf("View() at 2 GiB offset: %v", time.Since(start))
	if !strings.Contains(plain(out), "HEXED-MIDDLE-MAR") {
		t.Fatal("middle marker not visible at 2 GiB")
	}

	// jump to end
	m = press(m, keyRune('G'))
	start = time.Now()
	out = m.View()
	t.Logf("View() at end: %v", time.Since(start))
	if !strings.Contains(plain(out), "HEXED-EN") {
		t.Fatal("end marker not visible at end of file")
	}
	if m.cursor != size-1 {
		t.Fatalf("cursor = %d, want %d", m.cursor, size-1)
	}

	// scroll through 500 screens from the top, timing renders
	m = press(m, keyRune('g'))
	start = time.Now()
	for range 500 {
		m = press(m, tea.KeyMsg{Type: tea.KeyPgDown})
		_ = m.View()
	}
	t.Logf("500 pgdn+render: %v (%.2f ms/frame)", time.Since(start), float64(time.Since(start).Milliseconds())/500)

	// edit the last byte and one mid-file byte, save in place
	m = press(m, keyRune('G'), tea.KeyMsg{Type: tea.KeyEnter}, keyRune('a'), keyRune('b'))
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	m.cursor = 1 << 30
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter}, keyRune('c'), keyRune('d'), tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.edits) != 2 {
		t.Fatalf("dirty count = %d, want 2", len(m.edits))
	}
	start = time.Now()
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	t.Logf("save (2 dirty bytes in 4 GiB): %v — status: %s", time.Since(start), m.status)
	if len(m.edits) != 0 {
		t.Fatalf("dirty count after save = %d", len(m.edits))
	}

	// verify on disk without reading the whole file
	f, _ := os.Open(path)
	defer f.Close()
	b := make([]byte, 1)
	f.ReadAt(b, int64(size-1))
	if b[0] != 0xab {
		t.Fatalf("last byte on disk = 0x%02x, want 0xab", b[0])
	}
	f.ReadAt(b, 1<<30)
	if b[0] != 0xcd {
		t.Fatalf("byte at 1 GiB on disk = 0x%02x, want 0xcd", b[0])
	}
	// neighbours untouched
	f.ReadAt(b, int64(size-2))
	if b[0] != 'N' { // last byte of "HEXED-EN|D" before the edited D
		t.Logf("note: byte before end marker = %q", b[0])
	}

	if len(m.buf.win) > winSize {
		t.Fatalf("window grew to %d bytes", len(m.buf.win))
	}
	t.Logf("final heap: %.1f MB, window cache: %d KB", heapMB(), len(m.buf.win)/1024)
	fmt.Fprintln(os.Stderr, "OK")
}
