# norn

Text editor for the agentic age. Highly inspired by vim — modal editing, composable commands, zero bloat.

## Features

- **Modal editing** — normal, insert, visual, command, search modes
- **Vim motions** — hjkl, w/b/e, f/F/t/T, gg/G, 0/$/^, counts
- **Multiple buffers** — `:e`, `:bn`, `:bp`, `:ls`, `:bd`
- **File browser** — `:Ex` to browse and open files
- **System clipboard** — auto-copy on yank, `gp` / `:paste` to paste
- **Configurable** — TOML config file at `~/.config/norn/config.toml`
- **Dot repeat** — `.` repeats last edit
- **Undo/redo** — `u` / `ctrl+r`
- **Search** — `/`, `?`, `n`, `N` with highlighting
- **Visual mode** — character and line selection
- **Shell commands** — `:!cmd` to run shell commands

## Quick Start

```bash
go build -o norn .
./norn [file]
```

Inside the editor, type `:help` for the full keybinding reference.

## Configuration

Create `~/.config/norn/config.toml`:

```toml
[editor]
tabstop = 4
expandtab = false
number = true
relativenumber = false
clipboard = "auto"
```

Run `:config` inside the editor to open/create the config file.

### Options

| Option | Default | Description |
|--------|---------|-------------|
| `tabstop` | `4` | Spaces per tab (for `>>`/`<<` and `expandtab`) |
| `expandtab` | `false` | Insert spaces instead of tabs |
| `number` | `true` | Show line numbers |
| `relativenumber` | `false` | Show relative line numbers |
| `clipboard` | `"auto"` | Clipboard integration (`"auto"`, `"none"`, `"pbcopy"`, `"xclip"`, `"xsel"`, `"wl-copy"`) |

## Commands

| Command | Description |
|---------|-------------|
| `:w [file]` | Save file |
| `:q` | Quit (error if unsaved) |
| `:q!` | Force quit |
| `:wq` | Save and quit |
| `:e {file}` | Open file (or directory for browser) |
| `:Ex` / `:E` | Open file browser |
| `:bn` / `:bp` | Next / previous buffer |
| `:ls` | List open buffers |
| `:bd` / `:bd!` | Close buffer |
| `:b {n}` | Switch to buffer n |
| `:reload` | Reload file from disk |
| `:paste` | Paste from system clipboard |
| `:config` | Open config file |
| `:help` | Show keybindings |

## Tech

Written in Go with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lipgloss](https://github.com/charmbracelet/lipgloss).

## Contribution

PRs are welcome.

## License

MIT
