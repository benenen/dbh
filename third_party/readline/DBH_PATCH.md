# dbh readline patch

Source: `github.com/reeflective/readline` v1.3.0, copied from the Go module
distribution. The original MIT license and production Go sources are retained.
Upstream tests are excluded; dbh validates the affected path through a real PTY
in `tests/terminal_test.go`.

Upstream's `readInputFiltered` sends terminal cursor position reports to an
unbuffered channel. With cursor probing disabled, there is no receiver, so an
unsolicited or delayed report permanently blocks all keyboard input, including
Ctrl-C and exit commands.

Local changes:

- `completions.go`, `internal/completion/completion.go` and `utils.go`: add
  `Completions.NoFilter()` so dbh's substring matches are not discarded by the
  editor's prefix filter. The typed prefix still determines the input replaced
  when a candidate is selected; other completers retain prefix filtering.
- `internal/completion/insert.go`: `InsertCommonPrefix` skips candidates whose
  common prefix does not start with the typed word, so the
  `menu-complete-display-prefix` option cannot delete input for substring
  matches.
- `completion.go`: export `AcceptCompletion` and `CompletionsVisible`, so dbh
  can confirm a menu candidate with Enter without accepting the line, and
  enter the menu with Down only while candidates are displayed.
- `inputrc/bind.go`: normalize three map entry indentations with `gofmt`.
- `internal/core/keys.go`: buffer one cursor reply, allowing a query receiver to
  consume a reply that arrives before it starts waiting.
- `internal/core/keys_unix.go` and `keys_windows.go`: send replies without
  blocking when the channel is full or there is no receiver. Continue returning
  the user input extracted from the same read.
- `internal/display/refresh.go`: when the absolute cursor row is unavailable,
  clear below the input by emitting a newline instead of cursor-down. Newlines
  scroll at the bottom of the screen; cursor-down clamps there and caused the
  input to be erased and the cursor to overlap the previous output line.

The `go.mod` replacement keeps builds reproducible without changing module
caches. Remove this directory and the replacement when an upstream release
handles unsolicited and repeated replies without blocking, keeps the prompt
aligned at the bottom without cursor probing, and lets a completer bypass the
prefix filter. Run dbh's PTY tests when updating
or removing the patch. The layout regression replays the real output controls
with the cursor initially at the top and bottom of the screen.
