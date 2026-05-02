# Architecture

norn is a Vim-inspired terminal text editor written in Go.

## Stack

- **Language:** Go 1.24
- **TUI Framework:** Bubble Tea (Elm architecture: Model-View-Update)
- **Rendering:** Lipgloss (terminal styling)
- **Config:** TOML (`github.com/BurntSushi/toml`)

## Structure

Everything lives in a single file:

```
main.go    # Entire application (~3,000 lines)
```

No subdirectories, packages, or modules. Single `package main`.

## Architecture

Follows Bubble Tea's Elm pattern:

- **Model** — `model` struct holds all state (buffer, cursor, mode, undo/redo stacks, registers, search, visual selection, dot-repeat, multi-buffer list, config, file browser)
- **Update** — `Update()` dispatches to mode-specific handlers (normal, insert, command, search, visual, browser)
- **View** — `View()` renders viewport with line numbers, tab line, status bar, and command line

## Editor Modes

`normalMode` · `insertMode` · `commandMode` · `searchMode` · `visualMode` · `visualLineMode` · `browserMode`

## Key Subsystems (all in `main.go`)

| Subsystem | Purpose |
|-----------|---------|
| Types & constants | Mode enum, model struct, Buffer struct, Config struct, dirEntry |
| Styles | Lipgloss styles for cursor, highlights, status bar, tabs, browser |
| Pure helpers | Word movement, rune utils |
| Config | TOML loading, config paths, defaults |
| Clipboard | System clipboard via pbcopy/xclip/wl-copy auto-detection |
| File helpers | Split/join lines |
| Model init | newModel, Init |
| Buffer management | saveToBuffer, loadFromBuffer, switchToBuffer, openFileInNewBuffer |
| Undo/redo | Snapshot stack (max 200) |
| Normal mode | Motions, operators, counts |
| Insert mode | Text input, auto-indent |
| Dot repeat | Repeat last edit |
| Command mode | Ex-commands (`:w`, `:q`, `:!cmd`, `:e`, `:bn`, `:bp`, `:ls`, `:bd`, `:Ex`, `:config`, etc.) |
| Search | `/`, `?`, `n`, `N` with wrapping |
| Operator+motion | `d`, `y`, `c` + motion ranges |
| Visual mode | Selection, yank, delete, toggle case |
| File browser | Directory listing, navigation, open files |
| Rendering | Line rendering, highlighting, status bar, tab line, browser view |

## Multi-Buffer Design

Buffers are stored as `[]*Buffer` on the model. The current buffer's state (lines, cursor, undo stacks, etc.) lives directly on the model fields. When switching buffers:

1. `saveToBuffer()` — copies model fields back to the current `Buffer` object
2. `loadFromBuffer()` — loads the new buffer's fields into model

This means all existing code operates on `m.lines`, `m.cx`, etc. without indirection.

## Clipboard

Yank operations (yy, yw, visual y) automatically copy to the system clipboard. `gp` and `:paste` paste from the system clipboard. The clipboard command is auto-detected based on OS:
- macOS: `pbcopy` / `pbpaste`
- Linux (Wayland): `wl-copy` / `wl-paste`
- Linux (X11): `xclip` or `xsel`

## File Browser

The browser is a mode (`browserMode`) with its own state fields on the model. The file buffer state is preserved while browsing. Entries are sorted (directories first, then files, alphabetical). Supports hidden file toggle (`.` key).

## Config

Loaded from `~/.config/norn/config.toml` or `~/.nornrc.toml`. Supports tabstop, expandtab, line numbers, relative numbers, and clipboard selection.

## Shell Commands

`:!{cmd}` re-executes the binary with `--run-shell` flag to run shell commands outside the TUI.

## Build & Run

```
go build -o norn .        # compile
go run . [file]           # build & run
```

No Makefile, no tests, no CI.
