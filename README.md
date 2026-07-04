# hexed

A terminal byte editor built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).
Open a file, inspect its bytes in four different views, edit them in place, and
write the result back to disk.

## Build & run

```sh
go build
./hexed <file>
```

## Views

Cycle with `tab` / `shift+tab`, or jump directly with `1`–`4`:

| Key | View    | Layout                                                  |
|-----|---------|---------------------------------------------------------|
| 1   | HEX     | 16 bytes/row as hex pairs, with an ASCII sidebar        |
| 2   | BIN     | 8 bytes/row as 8-bit binary, with an ASCII sidebar      |
| 3   | ASCII   | 64 bytes/row as printable characters (`·` = unprintable)|
| 4   | UNICODE | UTF-8 decoded runes (`·` = continuation, `✗` = invalid) |

## Keys

| Key                  | Action                                          |
|----------------------|-------------------------------------------------|
| arrows / `hjkl`      | move cursor (byte-wise)                         |
| `pgup`/`pgdn`, `ctrl+u`/`ctrl+d` | page up / down                      |
| `home` / `end`       | start / end of row                              |
| `g` / `G`            | start / end of file                             |
| `enter` or `i`       | enter edit mode                                 |
| `esc`                | cancel pending input / leave edit mode          |
| `u`                  | undo last byte edit                             |
| `ctrl+s`             | save file (preserves original permissions)      |
| `q` / `ctrl+c`       | quit (asks twice if there are unsaved changes)  |

## Editing

Editing overwrites bytes in place (no insert/delete). What you type depends on
the active view:

- **HEX** — type two hex digits per byte; a partial entry shows as `a_`.
- **BIN** — type eight `0`/`1` bits per byte, most significant bit first.
- **ASCII / UNICODE** — type a character to replace the byte under the cursor
  (must fit in a single byte, i.e. U+0000–U+00FF).

The cursor advances automatically after each completed byte, so you can type a
run of values in one go. Modified bytes are highlighted until saved.
