package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type mode int

const (
	normalMode mode = iota
	insertMode
	commandMode
	searchMode
	visualMode
	visualLineMode
)

type snapshot struct {
	lines  [][]rune
	cx, cy int
}

type shellDoneMsg struct{ err error }

type model struct {
	lines      [][]rune
	cx, cy     int
	mode       mode
	pendingKey   string // operator-pending key (e.g. "d" waiting for motion)
	pendingCount int    // count saved when pendingKey was set
	command    string
	width      int
	height     int
	message    string
	msgIsErr   bool
	filename   string
	dirty      bool
	scrollY    int
	// count prefix
	countStr string
	// undo / redo
	undoStack []snapshot
	redoStack []snapshot
	// unnamed register
	register  [][]rune
	regIsLine bool
	// search
	searchBuf  string
	lastSearch string
	searchFwd  bool
	// visual anchor
	vx, vy int
	// f/F/t/T repeat
	lastFindChar rune
	lastFindType string // "f" "F" "t" "T"
	// . repeat
	dot      func(*model) // last change, nil if none
	dotEntry string       // insert entry type being recorded
	dotTyped []rune       // runes typed in current insert session
	// read-only buffer (e.g. help)
	readonly  bool
	prevModel *model // saved state to return to after closing a readonly buffer
}

// indentStr is used for >> / << and Tab in insert mode.
const indentStr = "\t"

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

var (
	cursorStyle     = lipgloss.NewStyle().Reverse(true)
	searchHighlight = lipgloss.NewStyle().Background(lipgloss.Color("3")).Foreground(lipgloss.Color("0"))
	visualHighlight = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("15"))
	statusNormal    = lipgloss.NewStyle().Background(lipgloss.Color("2")).Foreground(lipgloss.Color("0")).Bold(true).Padding(0, 1)
	statusInsert    = lipgloss.NewStyle().Background(lipgloss.Color("4")).Foreground(lipgloss.Color("0")).Bold(true).Padding(0, 1)
	statusCmd       = lipgloss.NewStyle().Background(lipgloss.Color("3")).Foreground(lipgloss.Color("0")).Bold(true).Padding(0, 1)
	statusSearch    = lipgloss.NewStyle().Background(lipgloss.Color("5")).Foreground(lipgloss.Color("0")).Bold(true).Padding(0, 1)
	statusVisual    = lipgloss.NewStyle().Background(lipgloss.Color("13")).Foreground(lipgloss.Color("0")).Bold(true).Padding(0, 1)
	statusBar    = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("250")).Padding(0, 1)
	lineNum      = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Width(4).Align(lipgloss.Right)
	readonlyTag  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	infoStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
func isSpace(r rune) bool    { return r == ' ' || r == '\t' }

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneLines(src [][]rune) [][]rune {
	out := make([][]rune, len(src))
	for i, l := range src {
		out[i] = append([]rune{}, l...)
	}
	return out
}

func wordForward(line []rune, cx int) int {
	n := len(line)
	if cx >= n-1 {
		return cx
	}
	if isWordRune(line[cx]) {
		for cx < n && isWordRune(line[cx]) {
			cx++
		}
	} else if !isSpace(line[cx]) {
		for cx < n && !isWordRune(line[cx]) && !isSpace(line[cx]) {
			cx++
		}
	}
	for cx < n && isSpace(line[cx]) {
		cx++
	}
	if cx >= n {
		cx = n - 1
	}
	return cx
}

func wordBackward(line []rune, cx int) int {
	if cx == 0 {
		return 0
	}
	cx--
	for cx > 0 && isSpace(line[cx]) {
		cx--
	}
	if isWordRune(line[cx]) {
		for cx > 0 && isWordRune(line[cx-1]) {
			cx--
		}
	} else {
		for cx > 0 && !isWordRune(line[cx-1]) && !isSpace(line[cx-1]) {
			cx--
		}
	}
	return cx
}

func wordEnd(line []rune, cx int) int {
	n := len(line)
	if cx >= n-1 {
		return cx
	}
	cx++
	for cx < n && isSpace(line[cx]) {
		cx++
	}
	if cx < n {
		if isWordRune(line[cx]) {
			for cx+1 < n && isWordRune(line[cx+1]) {
				cx++
			}
		} else {
			for cx+1 < n && !isWordRune(line[cx+1]) && !isSpace(line[cx+1]) {
				cx++
			}
		}
	}
	if cx >= n {
		cx = n - 1
	}
	return cx
}

func firstNonBlank(line []rune) int {
	for i, r := range line {
		if !isSpace(r) {
			return i
		}
	}
	return 0
}

// leadingWhitespace returns a copy of the leading whitespace of line.
func leadingWhitespace(line []rune) []rune {
	for i, r := range line {
		if !isSpace(r) {
			return append([]rune{}, line[:i]...)
		}
	}
	return append([]rune{}, line...) // whole line is whitespace
}

// ---------------------------------------------------------------------------
// File helpers
// ---------------------------------------------------------------------------

func splitLines(content string) [][]rune {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return [][]rune{{}}
	}
	raw := strings.Split(content, "\n")
	out := make([][]rune, len(raw))
	for i, s := range raw {
		out[i] = []rune(s)
	}
	return out
}

func joinLines(lines [][]rune) string {
	strs := make([]string, len(lines))
	for i, r := range lines {
		strs[i] = string(r)
	}
	return strings.Join(strs, "\n") + "\n"
}

// ---------------------------------------------------------------------------
// Model init
// ---------------------------------------------------------------------------

func newModel(filename string) model {
	m := model{
		lines:     [][]rune{{}},
		mode:      normalMode,
		width:     80,
		height:    24,
		filename:  filename,
		searchFwd: true,
	}
	if filename != "" {
		data, err := os.ReadFile(filename)
		if err != nil && !os.IsNotExist(err) {
			m.message = fmt.Sprintf("E: %v", err)
			m.msgIsErr = true
		} else if err == nil {
			m.lines = splitLines(string(data))
			m.message = fmt.Sprintf("\"%s\" %dL", filename, len(m.lines))
		}
	}
	return m
}

func (m model) Init() tea.Cmd { return nil }

// ---------------------------------------------------------------------------
// Undo / redo
// ---------------------------------------------------------------------------

const maxUndo = 200

func (m *model) saveUndo() {
	snap := snapshot{lines: cloneLines(m.lines), cx: m.cx, cy: m.cy}
	m.undoStack = append(m.undoStack, snap)
	if len(m.undoStack) > maxUndo {
		m.undoStack = m.undoStack[1:]
	}
	m.redoStack = m.redoStack[:0]
}

// ---------------------------------------------------------------------------
// Count helpers
// ---------------------------------------------------------------------------

func (m *model) consumeCount() int {
	if m.countStr == "" {
		return 1
	}
	n, err := strconv.Atoi(m.countStr)
	m.countStr = ""
	if err != nil || n <= 0 {
		return 1
	}
	if n > 9999 {
		return 9999
	}
	return n
}

// ---------------------------------------------------------------------------
// Update dispatcher
// ---------------------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.scrollIntoView()
		return m, nil
	case shellDoneMsg:
		if msg.err != nil {
			m.message = fmt.Sprintf("shell: %v", msg.err)
			m.msgIsErr = true
		}
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case normalMode:
			return m.handleNormal(msg)
		case insertMode:
			return m.handleInsert(msg)
		case commandMode:
			return m.handleCommand(msg)
		case searchMode:
			return m.handleSearch(msg)
		case visualMode, visualLineMode:
			return m.handleVisual(msg)
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Normal mode
// ---------------------------------------------------------------------------

func (m model) handleNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.message = ""
	key := msg.String()

	// --- accumulate count digits ---
	// "0" with no prior count means "go to start of line" (handled below)
	if m.pendingKey == "" {
		if (key >= "1" && key <= "9") || (key == "0" && m.countStr != "") {
			m.countStr += key
			return m, nil
		}
	}

	// --- operator-pending resolution ---
	if m.pendingKey != "" {
		pending := m.pendingKey
		cnt := m.pendingCount
		if cnt == 0 {
			cnt = 1
		}
		m.pendingKey = ""
		m.pendingCount = 0

		switch pending {
		case "d":
			if key == "d" {
				m.saveUndo()
				for i := 0; i < cnt; i++ {
					m.deleteLine()
				}
				n := cnt
				m.dot = func(m *model) {
					for i := 0; i < n; i++ {
						m.deleteLine()
					}
				}
			} else {
				m.saveUndo()
				mk := key
				for i := 0; i < cnt; i++ {
					m.applyOperator("d", mk)
				}
				n := cnt
				m.dot = func(m *model) {
					for i := 0; i < n; i++ {
						m.applyOperator("d", mk)
					}
				}
			}
		case "c":
			if key == "c" {
				m.saveUndo()
				m.cx = 0
				m.lines[m.cy] = []rune{}
				m.dirty = true
				m.dotEntry = "cc"
				m.dotTyped = nil
				m.mode = insertMode
			} else {
				m.saveUndo()
				mk := key
				m.applyOperatorRaw("c", mk) // sets mode=insert without dot side-effect
				m.dotEntry = "c" + mk
				m.dotTyped = nil
			}
		case "y":
			if key == "y" {
				m.yankLine()
			} else {
				m.applyOperator("y", key)
			}
		case "g":
			if key == "g" {
				m.cy = 0
				m.cx = 0
				m.scrollIntoView()
			}
		case "r":
			if len(msg.Runes) > 0 && m.cx < len(m.lines[m.cy]) {
				m.saveUndo()
				r := msg.Runes[0]
				m.lines[m.cy][m.cx] = r
				m.dirty = true
				m.dot = func(m *model) {
					if m.cx < len(m.lines[m.cy]) {
						m.lines[m.cy][m.cx] = r
						m.dirty = true
					}
				}
			}
		case "f", "F", "t", "T":
			if len(msg.Runes) > 0 {
				ch := msg.Runes[0]
				m.lastFindChar = ch
				m.lastFindType = pending
				for i := 0; i < cnt; i++ {
					m.doFind(pending, ch)
				}
			}
		case ">":
			if key == ">" {
				m.saveUndo()
				end := m.cy + cnt - 1
				if end >= len(m.lines) {
					end = len(m.lines) - 1
				}
				m.indentLines(m.cy, end, 1)
				n := cnt
				m.dot = func(m *model) {
					e := m.cy + n - 1
					if e >= len(m.lines) {
						e = len(m.lines) - 1
					}
					m.indentLines(m.cy, e, 1)
				}
			}
		case "<":
			if key == "<" {
				m.saveUndo()
				end := m.cy + cnt - 1
				if end >= len(m.lines) {
					end = len(m.lines) - 1
				}
				m.indentLines(m.cy, end, -1)
				n := cnt
				m.dot = func(m *model) {
					e := m.cy + n - 1
					if e >= len(m.lines) {
						e = len(m.lines) - 1
					}
					m.indentLines(m.cy, e, -1)
				}
			}
		}
		return m, nil
	}

	count := m.consumeCount()

	// In a read-only buffer, block all edit operations up-front.
	if m.readonly {
		switch key {
		case "i", "a", "A", "I", "o", "O", "s", "S", "C",
			"x", "D", "r", "~", "p", "P",
			"d", "c", "y", ">", "<", ".", "u", "ctrl+r":
			m.message = "E45: 'readonly' option is set"
			m.msgIsErr = true
			return m, nil
		}
	}

	switch key {
	// --- . repeat ---
	case ".":
		if m.dot != nil {
			for i := 0; i < count; i++ {
				m.saveUndo()
				m.dot(&m)
			}
			m.clampCursor()
		}

	// --- basic movement ---
	case "h":
		for i := 0; i < count; i++ {
			if m.cx > 0 {
				m.cx--
			}
		}
	case "l":
		for i := 0; i < count; i++ {
			if n := len(m.lines[m.cy]); n > 0 && m.cx < n-1 {
				m.cx++
			}
		}
	case "j":
		for i := 0; i < count; i++ {
			if m.cy < len(m.lines)-1 {
				m.cy++
			}
		}
		m.clampCursor()
		m.scrollIntoView()
	case "k":
		for i := 0; i < count; i++ {
			if m.cy > 0 {
				m.cy--
			}
		}
		m.clampCursor()
		m.scrollIntoView()

	// --- line navigation ---
	case "0":
		m.cx = 0
	case "^":
		m.cx = firstNonBlank(m.lines[m.cy])
	case "$":
		if n := len(m.lines[m.cy]); n > 0 {
			m.cx = n - 1
		}

	// --- word movement ---
	case "w":
		for i := 0; i < count; i++ {
			m.cx = wordForward(m.lines[m.cy], m.cx)
		}
	case "b":
		for i := 0; i < count; i++ {
			m.cx = wordBackward(m.lines[m.cy], m.cx)
		}
	case "e":
		for i := 0; i < count; i++ {
			m.cx = wordEnd(m.lines[m.cy], m.cx)
		}

	// --- find on line ---
	case "f", "F", "t", "T":
		m.pendingKey = key
		m.pendingCount = count
	case ";":
		if m.lastFindType != "" {
			for i := 0; i < count; i++ {
				m.doFind(m.lastFindType, m.lastFindChar)
			}
		}
	case ",":
		if m.lastFindType != "" {
			rev := map[string]string{"f": "F", "F": "f", "t": "T", "T": "t"}
			for i := 0; i < count; i++ {
				m.doFind(rev[m.lastFindType], m.lastFindChar)
			}
		}

	// --- file navigation ---
	case "g":
		m.pendingKey = "g"
		m.pendingCount = count
	case "G":
		if count > 1 {
			m.cy = count - 1
			if m.cy >= len(m.lines) {
				m.cy = len(m.lines) - 1
			}
		} else {
			m.cy = len(m.lines) - 1
		}
		m.clampCursor()
		m.scrollIntoView()

	// --- scrolling ---
	case "ctrl+d":
		m.cy += (m.height - 2) / 2 * count
		if m.cy >= len(m.lines) {
			m.cy = len(m.lines) - 1
		}
		m.clampCursor()
		m.scrollIntoView()
	case "ctrl+u":
		m.cy -= (m.height - 2) / 2 * count
		if m.cy < 0 {
			m.cy = 0
		}
		m.clampCursor()
		m.scrollIntoView()
	case "ctrl+f":
		m.cy += (m.height - 2) * count
		if m.cy >= len(m.lines) {
			m.cy = len(m.lines) - 1
		}
		m.clampCursor()
		m.scrollIntoView()
	case "ctrl+b":
		m.cy -= (m.height - 2) * count
		if m.cy < 0 {
			m.cy = 0
		}
		m.clampCursor()
		m.scrollIntoView()

	// --- enter insert mode ---
	case "i":
		m.saveUndo()
		m.dotEntry = "i"
		m.dotTyped = nil
		m.mode = insertMode
	case "a":
		m.saveUndo()
		if n := len(m.lines[m.cy]); n > 0 && m.cx < n {
			m.cx++
		}
		m.dotEntry = "a"
		m.dotTyped = nil
		m.mode = insertMode
	case "A":
		m.saveUndo()
		m.cx = len(m.lines[m.cy])
		m.dotEntry = "A"
		m.dotTyped = nil
		m.mode = insertMode
	case "I":
		m.saveUndo()
		m.cx = firstNonBlank(m.lines[m.cy])
		m.dotEntry = "I"
		m.dotTyped = nil
		m.mode = insertMode
	case "o":
		m.saveUndo()
		m.openLineBelow()
		m.dotEntry = "o"
		m.dotTyped = nil
		m.mode = insertMode
	case "O":
		m.saveUndo()
		m.openLineAbove()
		m.dotEntry = "O"
		m.dotTyped = nil
		m.mode = insertMode
	case "s":
		m.saveUndo()
		line := m.lines[m.cy]
		if m.cx < len(line) {
			m.lines[m.cy] = append(line[:m.cx:m.cx], line[m.cx+1:]...)
			m.dirty = true
		}
		m.dotEntry = "s"
		m.dotTyped = nil
		m.mode = insertMode
	case "S":
		m.saveUndo()
		m.cx = 0
		m.lines[m.cy] = []rune{}
		m.dirty = true
		m.dotEntry = "S"
		m.dotTyped = nil
		m.mode = insertMode
	case "C":
		m.saveUndo()
		m.lines[m.cy] = append([]rune{}, m.lines[m.cy][:m.cx]...)
		m.dirty = true
		m.dotEntry = "C"
		m.dotTyped = nil
		m.mode = insertMode

	// --- single-key edits ---
	case "x":
		line := m.lines[m.cy]
		if m.cx < len(line) {
			m.saveUndo()
			n := count
			if m.cx+n > len(line) {
				n = len(line) - m.cx
			}
			m.lines[m.cy] = append(line[:m.cx:m.cx], line[m.cx+n:]...)
			m.clampCursor()
			m.dirty = true
			deleted := n
			m.dot = func(m *model) {
				line := m.lines[m.cy]
				nn := deleted
				if m.cx+nn > len(line) {
					nn = len(line) - m.cx
				}
				if nn > 0 {
					m.lines[m.cy] = append(line[:m.cx:m.cx], line[m.cx+nn:]...)
					m.clampCursor()
					m.dirty = true
				}
			}
		}
	case "D":
		m.saveUndo()
		m.lines[m.cy] = append([]rune{}, m.lines[m.cy][:m.cx]...)
		m.dirty = true
		m.dot = func(m *model) {
			m.lines[m.cy] = append([]rune{}, m.lines[m.cy][:m.cx]...)
			m.dirty = true
		}
	case "~":
		line := m.lines[m.cy]
		if m.cx < len(line) {
			m.saveUndo()
			for i := 0; i < count && m.cx < len(m.lines[m.cy]); i++ {
				r := m.lines[m.cy][m.cx]
				if unicode.IsUpper(r) {
					m.lines[m.cy][m.cx] = unicode.ToLower(r)
				} else {
					m.lines[m.cy][m.cx] = unicode.ToUpper(r)
				}
				if m.cx < len(m.lines[m.cy])-1 {
					m.cx++
				}
			}
			m.dirty = true
			n := count
			m.dot = func(m *model) {
				for i := 0; i < n && m.cx < len(m.lines[m.cy]); i++ {
					r := m.lines[m.cy][m.cx]
					if unicode.IsUpper(r) {
						m.lines[m.cy][m.cx] = unicode.ToLower(r)
					} else {
						m.lines[m.cy][m.cx] = unicode.ToUpper(r)
					}
					if m.cx < len(m.lines[m.cy])-1 {
						m.cx++
					}
				}
				m.dirty = true
			}
		}

	// --- operator-pending starters ---
	case "r":
		m.pendingKey = "r"
		m.pendingCount = count
	case "d":
		m.pendingKey = "d"
		m.pendingCount = count
	case "c":
		m.pendingKey = "c"
		m.pendingCount = count
	case "y":
		m.pendingKey = "y"
		m.pendingCount = count
	case ">":
		m.pendingKey = ">"
		m.pendingCount = count
	case "<":
		m.pendingKey = "<"
		m.pendingCount = count

	// --- yank / paste ---
	case "p":
		m.saveUndo()
		m.paste(false)
		m.dot = func(m *model) { m.paste(false) }
	case "P":
		m.saveUndo()
		m.paste(true)
		m.dot = func(m *model) { m.paste(true) }

	// --- undo / redo ---
	case "u":
		if len(m.undoStack) > 0 {
			redo := snapshot{lines: cloneLines(m.lines), cx: m.cx, cy: m.cy}
			m.redoStack = append(m.redoStack, redo)
			snap := m.undoStack[len(m.undoStack)-1]
			m.undoStack = m.undoStack[:len(m.undoStack)-1]
			m.lines = snap.lines
			m.cx, m.cy = snap.cx, snap.cy
			m.dirty = true
			m.scrollIntoView()
		} else {
			m.message = "Already at oldest change"
		}
	case "ctrl+r":
		if len(m.redoStack) > 0 {
			undo := snapshot{lines: cloneLines(m.lines), cx: m.cx, cy: m.cy}
			m.undoStack = append(m.undoStack, undo)
			snap := m.redoStack[len(m.redoStack)-1]
			m.redoStack = m.redoStack[:len(m.redoStack)-1]
			m.lines = snap.lines
			m.cx, m.cy = snap.cx, snap.cy
			m.dirty = true
			m.scrollIntoView()
		} else {
			m.message = "Already at newest change"
		}

	// --- visual mode ---
	case "v":
		m.mode = visualMode
		m.vx, m.vy = m.cx, m.cy
	case "V":
		m.mode = visualLineMode
		m.vx, m.vy = m.cx, m.cy

	// --- search ---
	case "/":
		m.mode = searchMode
		m.searchBuf = ""
		m.searchFwd = true
	case "?":
		m.mode = searchMode
		m.searchBuf = ""
		m.searchFwd = false
	case "n":
		for i := 0; i < count; i++ {
			m.jumpToMatch(m.searchFwd)
		}
	case "N":
		for i := 0; i < count; i++ {
			m.jumpToMatch(!m.searchFwd)
		}

	// --- command line ---
	case ":":
		m.mode = commandMode
		m.command = ""
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Find on line (f/F/t/T)
// ---------------------------------------------------------------------------

func (m *model) doFind(typ string, ch rune) {
	line := m.lines[m.cy]
	n := len(line)
	switch typ {
	case "f":
		for i := m.cx + 1; i < n; i++ {
			if line[i] == ch {
				m.cx = i
				return
			}
		}
	case "F":
		for i := m.cx - 1; i >= 0; i-- {
			if line[i] == ch {
				m.cx = i
				return
			}
		}
	case "t":
		for i := m.cx + 1; i < n; i++ {
			if line[i] == ch {
				m.cx = i - 1
				return
			}
		}
	case "T":
		for i := m.cx - 1; i >= 0; i-- {
			if line[i] == ch {
				m.cx = i + 1
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Indent / dedent
// ---------------------------------------------------------------------------

func (m *model) indentLines(start, end, dir int) {
	indent := []rune(indentStr)
	for i := start; i <= end && i < len(m.lines); i++ {
		line := m.lines[i]
		if dir > 0 {
			m.lines[i] = append(indent, line...)
			if m.cy == i {
				m.cx += len(indent)
			}
		} else {
			// remove one indentStr from start (tab or up to len(indentStr) spaces)
			if len(line) > 0 && line[0] == '\t' {
				m.lines[i] = line[1:]
				if m.cy == i && m.cx > 0 {
					m.cx--
				}
			} else {
				sp := 0
				for sp < len(indent) && sp < len(line) && line[sp] == ' ' {
					sp++
				}
				if sp > 0 {
					m.lines[i] = line[sp:]
					if m.cy == i {
						if m.cx >= sp {
							m.cx -= sp
						} else {
							m.cx = 0
						}
					}
				}
			}
		}
	}
	m.clampCursor()
	m.dirty = true
}

// ---------------------------------------------------------------------------
// Insert mode
// ---------------------------------------------------------------------------

func (m model) handleInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = normalMode
		if n := len(m.lines[m.cy]); n > 0 && m.cx >= n {
			m.cx = n - 1
		}
		// finalise dot record for insert session
		if m.dotEntry != "" {
			entry := m.dotEntry
			typed := append([]rune{}, m.dotTyped...)
			m.dotEntry = ""
			m.dotTyped = nil
			m.dot = func(m *model) {
				applyInsertEntry(m, entry)
				for _, r := range typed {
					insertRuneAt(m, r)
				}
				if n := len(m.lines[m.cy]); n > 0 && m.cx >= n {
					m.cx = n - 1
				}
			}
		}

	case "tab":
		ins := []rune(indentStr)
		line := m.lines[m.cy]
		newLine := make([]rune, len(line)+len(ins))
		copy(newLine, line[:m.cx])
		copy(newLine[m.cx:], ins)
		copy(newLine[m.cx+len(ins):], line[m.cx:])
		m.lines[m.cy] = newLine
		m.cx += len(ins)
		m.dirty = true
		m.dotTyped = append(m.dotTyped, ins...)

	case "backspace", "ctrl+h":
		line := m.lines[m.cy]
		if m.cx > 0 {
			m.lines[m.cy] = append(line[:m.cx-1:m.cx-1], line[m.cx:]...)
			m.cx--
			m.dirty = true
		} else if m.cy > 0 {
			prev := m.lines[m.cy-1]
			m.cx = len(prev)
			m.lines[m.cy-1] = append(prev, line...)
			m.lines = append(m.lines[:m.cy], m.lines[m.cy+1:]...)
			m.cy--
			m.dirty = true
			m.scrollIntoView()
		}

	case "ctrl+w":
		line := m.lines[m.cy]
		newCx := wordBackward(line, m.cx)
		m.lines[m.cy] = append(line[:newCx:newCx], line[m.cx:]...)
		m.cx = newCx
		m.dirty = true

	case "enter":
		line := m.lines[m.cy]
		indent := leadingWhitespace(line)
		before := append([]rune{}, line[:m.cx]...)
		after := append(append([]rune{}, indent...), line[m.cx:]...)
		m.lines[m.cy] = before
		rest := make([][]rune, len(m.lines[m.cy+1:]))
		copy(rest, m.lines[m.cy+1:])
		m.lines = append(m.lines[:m.cy+1], append([][]rune{after}, rest...)...)
		m.cy++
		m.cx = len(indent)
		m.dirty = true
		m.scrollIntoView()
		// record Enter + indent prefix in dotTyped so . replay re-indents
		m.dotTyped = append(m.dotTyped, '\n')
		m.dotTyped = append(m.dotTyped, indent...)

	default:
		if len(msg.Runes) > 0 {
			line := m.lines[m.cy]
			newLine := make([]rune, len(line)+len(msg.Runes))
			copy(newLine, line[:m.cx])
			copy(newLine[m.cx:], msg.Runes)
			copy(newLine[m.cx+len(msg.Runes):], line[m.cx:])
			m.lines[m.cy] = newLine
			m.cx += len(msg.Runes)
			m.dirty = true
			m.dotTyped = append(m.dotTyped, msg.Runes...)
		}
	}
	return m, nil
}

// applyInsertEntry positions the cursor (and possibly mutates the buffer) as
// the given insert-entry type would, without switching mode. Used for . replay.
func applyInsertEntry(m *model, entry string) {
	switch entry {
	case "i":
		// cursor stays
	case "a":
		if n := len(m.lines[m.cy]); n > 0 && m.cx < n {
			m.cx++
		}
	case "A":
		m.cx = len(m.lines[m.cy])
	case "I":
		m.cx = firstNonBlank(m.lines[m.cy])
	case "o":
		m.openLineBelow()
	case "O":
		m.openLineAbove()
	case "s":
		line := m.lines[m.cy]
		if m.cx < len(line) {
			m.lines[m.cy] = append(line[:m.cx:m.cx], line[m.cx+1:]...)
			m.dirty = true
		}
	case "S", "cc":
		m.cx = 0
		m.lines[m.cy] = []rune{}
		m.dirty = true
	case "C":
		m.lines[m.cy] = append([]rune{}, m.lines[m.cy][:m.cx]...)
		m.dirty = true
	default:
		// "cw", "ce", "cb", "c0", "c$", "c^"
		if len(entry) >= 2 && entry[0] == 'c' {
			motion := entry[1:]
			m.applyOperator("c_raw", motion) // "c_raw" skips mode change
		}
	}
}

// insertRuneAt inserts a single rune at the current cursor position.
// Newlines in dotTyped are expanded back into actual line splits.
func insertRuneAt(m *model, r rune) {
	if r == '\n' {
		line := m.lines[m.cy]
		before := append([]rune{}, line[:m.cx]...)
		after := append([]rune{}, line[m.cx:]...)
		m.lines[m.cy] = before
		rest := make([][]rune, len(m.lines[m.cy+1:]))
		copy(rest, m.lines[m.cy+1:])
		m.lines = append(m.lines[:m.cy+1], append([][]rune{after}, rest...)...)
		m.cy++
		m.cx = 0
		m.dirty = true
		m.scrollIntoView()
		return
	}
	line := m.lines[m.cy]
	newLine := make([]rune, len(line)+1)
	copy(newLine, line[:m.cx])
	newLine[m.cx] = r
	copy(newLine[m.cx+1:], line[m.cx:])
	m.lines[m.cy] = newLine
	m.cx++
	m.dirty = true
}

// ---------------------------------------------------------------------------
// Command mode
// ---------------------------------------------------------------------------

func (m model) handleCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = normalMode
		m.command = ""
	case "enter":
		cmd := strings.TrimSpace(m.command)
		m.mode = normalMode
		m.command = ""
		return m.execCommand(cmd)
	case "backspace", "ctrl+h":
		if len(m.command) > 0 {
			r := []rune(m.command)
			m.command = string(r[:len(r)-1])
		} else {
			m.mode = normalMode
		}
	default:
		if len(msg.Runes) > 0 {
			m.command += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) execCommand(cmd string) (tea.Model, tea.Cmd) {
	// :!{command} — run a shell command
	if strings.HasPrefix(cmd, "!") {
		shell := strings.TrimSpace(cmd[1:])
		if shell == "" {
			m.message = "E34: No previous command"
			m.msgIsErr = true
			return m, nil
		}
		self, err := os.Executable()
		if err != nil {
			m.message = fmt.Sprintf("E: %v", err)
			m.msgIsErr = true
			return m, nil
		}
		c := exec.Command(self, "--run-shell", shell)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return m, tea.ExecProcess(c, func(err error) tea.Msg {
			return shellDoneMsg{err}
		})
	}

	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return m, nil
	}
	switch parts[0] {
	case "q":
		if m.readonly && m.prevModel != nil {
			m = *m.prevModel
			return m, nil
		}
		if m.dirty {
			m.message = "E37: No write since last change (add ! to override)"
			m.msgIsErr = true
			return m, nil
		}
		return m, tea.Quit
	case "q!":
		if m.readonly && m.prevModel != nil {
			m = *m.prevModel
			return m, nil
		}
		return m, tea.Quit
	case "w":
		return m.writeFile(parts)
	case "wq":
		m2, cmd2 := m.writeFile(parts)
		if m2.(model).msgIsErr {
			return m2, cmd2
		}
		return m2, tea.Quit
	case "noh":
		m.lastSearch = ""
	case "version", "ver":
		m.message = "norn " + version
		m.msgIsErr = false
	case "help", "h":
		m.openHelpBuffer()
	default:
		m.message = "E492: Not an editor command: " + cmd
		m.msgIsErr = true
	}
	return m, nil
}

func (m model) writeFile(parts []string) (tea.Model, tea.Cmd) {
	target := m.filename
	if len(parts) >= 2 {
		target = parts[1]
	}
	if target == "" {
		m.message = "E32: No file name"
		m.msgIsErr = true
		return m, nil
	}
	if err := os.WriteFile(target, []byte(joinLines(m.lines)), 0644); err != nil {
		m.message = fmt.Sprintf("E: %v", err)
		m.msgIsErr = true
		return m, nil
	}
	m.filename = target
	m.dirty = false
	m.message = fmt.Sprintf("\"%s\" %dL written", target, len(m.lines))
	m.msgIsErr = false
	return m, nil
}

// ---------------------------------------------------------------------------
// Search mode
// ---------------------------------------------------------------------------

func (m model) handleSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = normalMode
		m.searchBuf = ""
	case "enter":
		m.lastSearch = m.searchBuf
		m.searchBuf = ""
		m.mode = normalMode
		if m.lastSearch != "" {
			m.jumpToMatch(m.searchFwd)
		}
	case "backspace", "ctrl+h":
		if len(m.searchBuf) > 0 {
			r := []rune(m.searchBuf)
			m.searchBuf = string(r[:len(r)-1])
		} else {
			m.mode = normalMode
		}
	default:
		if len(msg.Runes) > 0 {
			m.searchBuf += string(msg.Runes)
		}
	}
	return m, nil
}

func (m *model) jumpToMatch(fwd bool) {
	query := []rune(m.lastSearch)
	if len(query) == 0 {
		return
	}
	qLen := len(query)
	nLines := len(m.lines)
	if fwd {
		for i := 0; i < nLines; i++ {
			row := (m.cy + i) % nLines
			line := m.lines[row]
			startCol := 0
			if i == 0 {
				startCol = m.cx + 1
			}
			for c := startCol; c+qLen <= len(line); c++ {
				if runesEqual(line[c:c+qLen], query) {
					m.cy, m.cx = row, c
					m.scrollIntoView()
					return
				}
			}
		}
	} else {
		for i := 0; i < nLines; i++ {
			row := (m.cy - i + nLines) % nLines
			line := m.lines[row]
			maxCol := len(line) - qLen
			if i == 0 {
				maxCol = m.cx - 1
			}
			for c := maxCol; c >= 0; c-- {
				if c+qLen <= len(line) && runesEqual(line[c:c+qLen], query) {
					m.cy, m.cx = row, c
					m.scrollIntoView()
					return
				}
			}
		}
	}
	m.message = fmt.Sprintf("Pattern not found: %s", m.lastSearch)
	m.msgIsErr = true
}

// ---------------------------------------------------------------------------
// Operator + motion
// ---------------------------------------------------------------------------

// operatorRange returns [lo, hi) for the line at cy and a cursor position after.
func (m model) operatorRange(motionKey string) (lo, hi, cur int, ok bool) {
	line := m.lines[m.cy]
	cx := m.cx
	n := len(line)
	if n == 0 {
		return 0, 0, 0, false
	}
	switch motionKey {
	case "w":
		tgt := wordForward(line, cx)
		if tgt == cx {
			tgt = n
		}
		return cx, tgt, cx, true
	case "e":
		tgt := wordEnd(line, cx)
		return cx, tgt + 1, cx, true
	case "b":
		tgt := wordBackward(line, cx)
		if tgt == cx {
			return 0, 0, 0, false
		}
		return tgt, cx, tgt, true
	case "0":
		if cx == 0 {
			return 0, 0, 0, false
		}
		return 0, cx, 0, true
	case "$":
		return cx, n, cx, true
	case "^":
		fnb := firstNonBlank(line)
		if fnb < cx {
			return fnb, cx, fnb, true
		}
		if fnb > cx {
			return cx, fnb, cx, true
		}
		return 0, 0, 0, false
	}
	return 0, 0, 0, false
}

// applyOperator executes d/y and handles saveUndo internally.
// For "c" use applyOperatorRaw (which also sets mode=insert).
func (m *model) applyOperator(op, motionKey string) {
	lo, hi, cur, ok := m.operatorRange(motionKey)
	if !ok {
		return
	}
	line := m.lines[m.cy]
	switch op {
	case "d":
		m.saveUndo()
		m.lines[m.cy] = append(line[:lo:lo], line[hi:]...)
		m.cx = cur
		m.clampCursor()
		m.dirty = true
	case "c_raw":
		// internal use for dot replay: like "c" but no mode change, no saveUndo
		m.lines[m.cy] = append(line[:lo:lo], line[hi:]...)
		m.cx = cur
		m.clampCursor()
		m.dirty = true
	case "y":
		if hi > lo {
			m.register = [][]rune{append([]rune{}, line[lo:hi]...)}
			m.regIsLine = false
			m.message = fmt.Sprintf("%d chars yanked", hi-lo)
		}
	}
}

// applyOperatorRaw is like applyOperator("c") but is called from the pending
// handler after saveUndo has already been called. It sets mode=insertMode.
func (m *model) applyOperatorRaw(op, motionKey string) {
	lo, hi, cur, ok := m.operatorRange(motionKey)
	if !ok {
		m.mode = insertMode
		return
	}
	line := m.lines[m.cy]
	m.lines[m.cy] = append(line[:lo:lo], line[hi:]...)
	m.cx = cur
	m.clampCursor()
	m.dirty = true
	if op == "c" {
		m.mode = insertMode
	}
}

// ---------------------------------------------------------------------------
// Buffer mutations
// ---------------------------------------------------------------------------

func (m *model) openLineBelow() {
	indent := leadingWhitespace(m.lines[m.cy])
	tail := make([][]rune, len(m.lines[m.cy+1:]))
	copy(tail, m.lines[m.cy+1:])
	m.lines = append(m.lines[:m.cy+1], append([][]rune{append([]rune{}, indent...)}, tail...)...)
	m.cy++
	m.cx = len(indent)
	m.dirty = true
	m.scrollIntoView()
}

func (m *model) openLineAbove() {
	indent := leadingWhitespace(m.lines[m.cy])
	tail := make([][]rune, len(m.lines[m.cy:]))
	copy(tail, m.lines[m.cy:])
	m.lines = append(m.lines[:m.cy], append([][]rune{append([]rune{}, indent...)}, tail...)...)
	m.cx = len(indent)
	m.dirty = true
	m.scrollIntoView()
}

func (m *model) deleteLine() {
	if len(m.lines) == 1 {
		m.lines[0] = []rune{}
		m.cx = 0
	} else {
		m.lines = append(m.lines[:m.cy], m.lines[m.cy+1:]...)
		if m.cy >= len(m.lines) {
			m.cy = len(m.lines) - 1
		}
		m.clampCursor()
		m.scrollIntoView()
	}
	m.dirty = true
}

func (m *model) yankLine() {
	m.register = [][]rune{append([]rune{}, m.lines[m.cy]...)}
	m.regIsLine = true
	m.message = "1 line yanked"
}

func (m *model) paste(before bool) {
	if len(m.register) == 0 {
		return
	}
	if m.regIsLine {
		at := m.cy + 1
		if before {
			at = m.cy
		}
		pasted := cloneLines(m.register)
		tail := make([][]rune, len(m.lines[at:]))
		copy(tail, m.lines[at:])
		m.lines = append(m.lines[:at], append(pasted, tail...)...)
		m.cy = at
		m.cx = 0
	} else if len(m.register[0]) > 0 {
		line := m.lines[m.cy]
		ins := m.register[0]
		at := m.cx
		if !before && len(line) > 0 {
			at++
		}
		newLine := make([]rune, len(line)+len(ins))
		copy(newLine, line[:at])
		copy(newLine[at:], ins)
		copy(newLine[at+len(ins):], line[at:])
		m.lines[m.cy] = newLine
		m.cx = at + len(ins) - 1
	}
	m.dirty = true
	m.scrollIntoView()
}

// ---------------------------------------------------------------------------
// Cursor helpers
// ---------------------------------------------------------------------------

func (m *model) clampCursor() {
	n := len(m.lines[m.cy])
	if n == 0 {
		m.cx = 0
	} else if m.cx >= n {
		m.cx = n - 1
	}
}

func (m *model) scrollIntoView() {
	editorHeight := m.height - 2
	if editorHeight <= 0 {
		editorHeight = 1
	}
	if m.cy < m.scrollY {
		m.scrollY = m.cy
	} else if m.cy >= m.scrollY+editorHeight {
		m.scrollY = m.cy - editorHeight + 1
	}
}

// ---------------------------------------------------------------------------
// Visual mode
// ---------------------------------------------------------------------------

type selRange struct {
	startRow, startCol int
	endRow, endCol     int
	linewise           bool
}

func (m model) visualSel() selRange {
	if m.mode == visualLineMode {
		s, e := m.vy, m.cy
		if s > e {
			s, e = e, s
		}
		return selRange{startRow: s, startCol: 0, endRow: e, endCol: len(m.lines[e]), linewise: true}
	}
	sr, sc, er, ec := m.vy, m.vx, m.cy, m.cx
	if sr > er || (sr == er && sc > ec) {
		sr, sc, er, ec = er, ec, sr, sc
	}
	return selRange{startRow: sr, startCol: sc, endRow: er, endCol: ec}
}

func (m model) isInSel(row, col int) bool {
	if m.mode != visualMode && m.mode != visualLineMode {
		return false
	}
	sel := m.visualSel()
	if row < sel.startRow || row > sel.endRow {
		return false
	}
	if sel.linewise {
		return true
	}
	if row == sel.startRow && row == sel.endRow {
		return col >= sel.startCol && col <= sel.endCol
	}
	if row == sel.startRow {
		return col >= sel.startCol
	}
	if row == sel.endRow {
		return col <= sel.endCol
	}
	return true
}

func (m *model) yankSel(sel selRange) {
	if sel.linewise {
		m.register = cloneLines(m.lines[sel.startRow : sel.endRow+1])
		m.regIsLine = true
		m.message = fmt.Sprintf("%d lines yanked", sel.endRow-sel.startRow+1)
		return
	}
	if sel.startRow == sel.endRow {
		line := m.lines[sel.startRow]
		end := sel.endCol + 1
		if end > len(line) {
			end = len(line)
		}
		m.register = [][]rune{append([]rune{}, line[sel.startCol:end]...)}
		m.regIsLine = false
		m.message = fmt.Sprintf("%d chars yanked", end-sel.startCol)
		return
	}
	var reg [][]rune
	for row := sel.startRow; row <= sel.endRow; row++ {
		line := m.lines[row]
		switch row {
		case sel.startRow:
			reg = append(reg, append([]rune{}, line[sel.startCol:]...))
		case sel.endRow:
			end := sel.endCol + 1
			if end > len(line) {
				end = len(line)
			}
			reg = append(reg, append([]rune{}, line[:end]...))
		default:
			reg = append(reg, append([]rune{}, line...))
		}
	}
	m.register = reg
	m.regIsLine = false
	m.message = fmt.Sprintf("%d lines yanked", sel.endRow-sel.startRow+1)
}

func (m *model) deleteSel(sel selRange) {
	if sel.linewise {
		m.lines = append(m.lines[:sel.startRow], m.lines[sel.endRow+1:]...)
		if len(m.lines) == 0 {
			m.lines = [][]rune{{}}
		}
		if sel.startRow >= len(m.lines) {
			sel.startRow = len(m.lines) - 1
		}
		m.cy, m.cx = sel.startRow, 0
		m.clampCursor()
		m.scrollIntoView()
		m.dirty = true
		return
	}
	if sel.startRow == sel.endRow {
		line := m.lines[sel.startRow]
		end := sel.endCol + 1
		if end > len(line) {
			end = len(line)
		}
		m.lines[sel.startRow] = append(line[:sel.startCol:sel.startCol], line[end:]...)
		m.cy, m.cx = sel.startRow, sel.startCol
		m.clampCursor()
		m.dirty = true
		return
	}
	first := m.lines[sel.startRow]
	last := m.lines[sel.endRow]
	end := sel.endCol + 1
	if end > len(last) {
		end = len(last)
	}
	merged := append(first[:sel.startCol:sel.startCol], last[end:]...)
	tail := make([][]rune, len(m.lines[sel.endRow+1:]))
	copy(tail, m.lines[sel.endRow+1:])
	m.lines = append(m.lines[:sel.startRow], append([][]rune{merged}, tail...)...)
	m.cy, m.cx = sel.startRow, sel.startCol
	m.clampCursor()
	m.scrollIntoView()
	m.dirty = true
}

func (m *model) toggleCaseSel(sel selRange) {
	for row := sel.startRow; row <= sel.endRow; row++ {
		line := m.lines[row]
		colStart, colEnd := 0, len(line)-1
		if !sel.linewise {
			if row == sel.startRow {
				colStart = sel.startCol
			}
			if row == sel.endRow {
				colEnd = sel.endCol
			}
		}
		for c := colStart; c <= colEnd && c < len(line); c++ {
			r := line[c]
			if unicode.IsUpper(r) {
				line[c] = unicode.ToLower(r)
			} else if unicode.IsLower(r) {
				line[c] = unicode.ToUpper(r)
			}
		}
	}
	m.dirty = true
}

func (m model) handleVisual(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc", "v", "V":
		m.mode = normalMode
		return m, nil

	case "h":
		if m.cx > 0 {
			m.cx--
		}
	case "l":
		if n := len(m.lines[m.cy]); n > 0 && m.cx < n-1 {
			m.cx++
		}
	case "j":
		if m.cy < len(m.lines)-1 {
			m.cy++
			m.clampCursor()
			m.scrollIntoView()
		}
	case "k":
		if m.cy > 0 {
			m.cy--
			m.clampCursor()
			m.scrollIntoView()
		}
	case "0":
		m.cx = 0
	case "^":
		m.cx = firstNonBlank(m.lines[m.cy])
	case "$":
		if n := len(m.lines[m.cy]); n > 0 {
			m.cx = n - 1
		}
	case "w":
		m.cx = wordForward(m.lines[m.cy], m.cx)
	case "b":
		m.cx = wordBackward(m.lines[m.cy], m.cx)
	case "e":
		m.cx = wordEnd(m.lines[m.cy], m.cx)
	case "G":
		m.cy = len(m.lines) - 1
		m.clampCursor()
		m.scrollIntoView()
	case "ctrl+d":
		m.cy += (m.height - 2) / 2
		if m.cy >= len(m.lines) {
			m.cy = len(m.lines) - 1
		}
		m.clampCursor()
		m.scrollIntoView()
	case "ctrl+u":
		m.cy -= (m.height - 2) / 2
		if m.cy < 0 {
			m.cy = 0
		}
		m.clampCursor()
		m.scrollIntoView()

	case "d", "x":
		sel := m.visualSel()
		m.saveUndo()
		m.deleteSel(sel)
		m.mode = normalMode
	case "y":
		sel := m.visualSel()
		m.yankSel(sel)
		m.cy, m.cx = sel.startRow, sel.startCol
		m.clampCursor()
		m.scrollIntoView()
		m.mode = normalMode
	case "c":
		sel := m.visualSel()
		m.saveUndo()
		m.deleteSel(sel)
		m.dotEntry = "i" // after deletion cursor is in position; treat as "i"
		m.dotTyped = nil
		m.mode = insertMode
	case "~":
		sel := m.visualSel()
		m.saveUndo()
		m.toggleCaseSel(sel)
		m.cy, m.cx = sel.startRow, sel.startCol
		m.clampCursor()
		m.mode = normalMode
	case "p":
		sel := m.visualSel()
		m.saveUndo()
		m.deleteSel(sel)
		m.paste(false)
		m.mode = normalMode
	case ">":
		sel := m.visualSel()
		m.saveUndo()
		m.indentLines(sel.startRow, sel.endRow, 1)
		m.mode = normalMode
	case "<":
		sel := m.visualSel()
		m.saveUndo()
		m.indentLines(sel.startRow, sel.endRow, -1)
		m.mode = normalMode
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Help buffer
// ---------------------------------------------------------------------------

// helpText returns the help content as plain-text lines loaded into a
// read-only norn buffer. All normal-mode navigation (hjkl, gg/G, /, n/N …)
// works naturally inside it. :q returns to the previous file.
func helpText() [][]rune {
	raw := `================================================================================
                           norn  KEY BINDINGS
================================================================================
  Use normal-mode navigation to browse: hjkl, gg/G, ctrl+d/u, /search, etc.
  :q  to return to your file.
================================================================================

MOVEMENT
  h / l                     Move cursor left / right
  j / k                     Move cursor down / up
  w / b                     Word forward / backward
  e                         Word end (forward)
  0 / $                     Start / end of line
  ^                         First non-blank character of line
  gg / G                    Jump to first / last line of file
  {n}G                      Jump to line n
  ctrl+d / ctrl+u           Scroll half-page down / up
  ctrl+f / ctrl+b           Scroll full-page down / up

FIND ON LINE
  f{c} / F{c}               Find character forward / backward on line
  t{c} / T{c}               Move till character forward / backward
  ; / ,                     Repeat / reverse last f/F/t/T find

SEARCH
  /{pattern}                Search forward
  ?{pattern}                Search backward
  n / N                     Jump to next / previous match
  :noh                      Clear search highlight

ENTERING INSERT MODE
  i / a                     Insert before / after cursor
  I / A                     Insert at line start / end
  o / O                     Open new line below / above
  s / S                     Substitute character / whole line
  C                         Change from cursor to end of line
  r{c}                      Replace single character under cursor

INSERT MODE
  Esc                       Return to normal mode
  Enter                     New line (copies leading indent)
  Tab                       Insert indent
  Backspace                 Delete character before cursor
  ctrl+w                    Delete word backward

EDITING  (normal mode)
  x                         Delete character under cursor
  D                         Delete to end of line
  ~                         Toggle case of character (advances cursor)
  dd  /  {n}dd              Delete line(s)
  dw  de  db  d$  d0  d^    Delete by motion
  cc  cw  ce  cb  c$  c0    Change by motion (deletes then enters insert)
  yy  /  {n}yy              Yank (copy) line(s)
  yw  ye  yb  y$  y0        Yank by motion
  >>  /  <<                 Indent / dedent current line
  {n}>>  /  {n}<<           Indent / dedent n lines

PASTE & UNDO
  p / P                     Paste after / before cursor
  u                         Undo
  ctrl+r                    Redo
  .                         Repeat last change

VISUAL MODE
  v                         Enter visual (character) mode
  V                         Enter visual line mode
  Esc  /  v  /  V           Exit visual mode
  h j k l                   Extend selection
  w  b  e  $  0  ^          Extend selection by motion
  G  ctrl+d  ctrl+u         Extend selection to jump targets
  d / x                     Delete selection
  y                         Yank selection
  c                         Change selection (delete + insert)
  > / <                     Indent / dedent selection
  ~                         Toggle case of selection
  p                         Paste over selection

COUNT PREFIXES
  {n}{motion}               Repeat motion n times  (e.g. 3j, 5w, 10l)
  {n}dd / {n}>>             Delete / indent n lines
  {n}.                      Repeat last change n times

COMMANDS  (:)
  :w  [file]                Save (optionally to a different file)
  :q                        Quit (blocked if there are unsaved changes)
  :q!                       Force quit without saving
  :wq                       Save and quit
  :noh                      Clear search highlight
  :help  or  :h             Open this help buffer

================================================================================`
	return splitLines(raw)
}

// openHelpBuffer swaps in the help text as a read-only buffer, saving the
// current model so :q can restore it.
func (m *model) openHelpBuffer() {
	prev := *m
	prev.prevModel = nil // don't chain saves
	m.lines = helpText()
	m.filename = "[Help]"
	m.dirty = false
	m.readonly = true
	m.cx, m.cy = 0, 0
	m.scrollY = 0
	m.mode = normalMode
	m.message = ":q to return to your file"
	m.msgIsErr = false
	m.prevModel = &prev
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m model) searchMatchSet(lineIdx int) map[int]bool {
	if m.lastSearch == "" {
		return nil
	}
	query := []rune(m.lastSearch)
	qLen := len(query)
	line := m.lines[lineIdx]
	if len(line) < qLen {
		return nil
	}
	set := map[int]bool{}
	for c := 0; c+qLen <= len(line); c++ {
		if runesEqual(line[c:c+qLen], query) {
			for k := c; k < c+qLen; k++ {
				set[k] = true
			}
		}
	}
	return set
}

func (m model) renderLine(lineIdx int) string {
	line := m.lines[lineIdx]
	isCursorLine := lineIdx == m.cy
	matchAt := m.searchMatchSet(lineIdx)
	n := len(line)

	var sb strings.Builder
	for i := 0; i <= n; i++ {
		isCursor := isCursorLine && i == m.cx
		var ch string
		if i < n {
			ch = string(line[i])
		} else if isCursor {
			ch = " "
		} else {
			break
		}
		switch {
		case isCursor:
			sb.WriteString(cursorStyle.Render(ch))
		case m.isInSel(lineIdx, i):
			sb.WriteString(visualHighlight.Render(ch))
		case matchAt[i]:
			sb.WriteString(searchHighlight.Render(ch))
		default:
			sb.WriteString(ch)
		}
	}
	return sb.String()
}

func (m model) View() string {
	var sb strings.Builder
	editorHeight := m.height - 2

	for i := 0; i < editorHeight; i++ {
		lineIdx := m.scrollY + i
		var num, content string
		if lineIdx < len(m.lines) {
			num = lineNum.Render(fmt.Sprintf("%d", lineIdx+1))
			content = m.renderLine(lineIdx)
		} else {
			num = lineNum.Render("~")
		}
		sb.WriteString(num + " " + content + "\n")
	}

	var modeLabel string
	var modeStyle lipgloss.Style
	switch m.mode {
	case normalMode:
		modeLabel = " NORMAL "
		modeStyle = statusNormal
	case insertMode:
		modeLabel = " INSERT "
		modeStyle = statusInsert
	case commandMode:
		modeLabel = " COMMAND "
		modeStyle = statusCmd
	case searchMode:
		modeLabel = " SEARCH "
		modeStyle = statusSearch
	case visualMode:
		modeLabel = " VISUAL "
		modeStyle = statusVisual
	case visualLineMode:
		modeLabel = " VISUAL LINE "
		modeStyle = statusVisual
	}

	fname := m.filename
	if fname == "" {
		fname = "[No Name]"
	}
	if m.readonly {
		fname += " " + readonlyTag.Render("[RO]")
	} else if m.dirty {
		fname += " [+]"
	}
	// show count prefix in status bar while typing a number
	if m.countStr != "" {
		fname = m.countStr + " | " + fname
	}
	pos := fmt.Sprintf("%d:%d", m.cy+1, m.cx+1)
	middle := " " + fname + " "
	gap := m.width - lipgloss.Width(modeLabel) - len(middle) - len(pos) - 2
	if gap < 0 {
		gap = 0
	}
	sb.WriteString(modeStyle.Render(modeLabel) +
		statusBar.Render(middle+strings.Repeat(" ", gap)+pos) + "\n")

	switch m.mode {
	case commandMode:
		sb.WriteString(":" + m.command)
	case searchMode:
		if m.searchFwd {
			sb.WriteString("/" + m.searchBuf)
		} else {
			sb.WriteString("?" + m.searchBuf)
		}
	default:
		if m.message != "" {
			if m.msgIsErr {
				sb.WriteString(errStyle.Render(m.message))
			} else {
				sb.WriteString(infoStyle.Render(m.message))
			}
		}
	}

	return sb.String()
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

const version = "0.1.0"

// runShellAndPause is the vim-style internal subcommand used by :!{cmd}.
// norn calls itself with --run-shell so that the pause ("Hit ENTER") is
// handled in Go code rather than a shell `read`, matching how vim does it.
func runShellAndPause(shell string) {
	c := exec.Command("sh", "-c", shell)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	_ = c.Run()

	fmt.Print("\n\033[33m-- Hit ENTER to return to norn --\033[0m")
	// Read one byte directly from stdin. By the time this runs, BubbleTea
	// has fully released the terminal, so this blocks cleanly on user input.
	buf := make([]byte, 1)
	os.Stdin.Read(buf) //nolint:errcheck
}

func main() {
	// Internal subcommand: run a shell command then pause (used by :!)
	if len(os.Args) >= 3 && os.Args[1] == "--run-shell" {
		runShellAndPause(strings.Join(os.Args[2:], " "))
		os.Exit(0)
	}

	filename := ""
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version", "-version", "-v":
			fmt.Printf("norn %s\n", version)
			os.Exit(0)
		case "--help", "-help", "-h":
			fmt.Println("Usage: norn [file]")
			fmt.Println("       norn --version")
			fmt.Println("Open file in the norn editor. Run :help inside the editor for key bindings.")
			os.Exit(0)
		default:
			if filename == "" {
				filename = arg
			}
		}
	}
	p := tea.NewProgram(newModel(filename), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
