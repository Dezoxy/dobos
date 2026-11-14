# slogify

A codemod that moves the daemon's logging from `log.Logger` to `log/slog`
(through [`internal/flog`](../../internal/flog)). It exists so the conversion
can be redone on a newer `main` after other branches have changed the same
files: reset, run the tool, re-apply the hand edits.

```sh
go run ./scripts/slogify -n .   # dry run: files that would change, and the MANUAL list
go run ./scripts/slogify .      # rewrite in place
```

It walks `cmd/` and `internal/`, skipping `internal/anacrolix-torrent`
(hard rule 4), `internal/gostorm` (it logs through its own `TLogln`) and
`internal/flog`. Test files are converted too. It parses with `go/ast` and
splices replacements into the original bytes, so comments, line breaks and
everything it does not recognise stay as they were; `gofmt` runs over a
changed file and the imports it needs are added. A converted tree is a fixed
point: running it again changes nothing.

## What it rewrites

| Before | After |
| --- | --- |
| `logger.Printf("[X] a %s: %v", h, err)` | `logger.Info(fmt.Sprintf("[X] a %s: %v", h, err), "h", h, "err", err)` |
| `logger.Printf("WARNING: ...")` | `logger.Warn(...)` |
| `logger.Printf("... ERROR: ...")`, `[PANIC]` | `logger.Error(...)` |
| `logger.Printf("[DEBUG] ...")` | `logger.Debug(...)` |
| `logger.Printf(format, args...)` (format not a literal) | `flog.Printf(logger, format, args...)`, level taken from the rendered text |
| `logger.Print(x)`, `logger.Println("lit")` | `logger.Info(...)` |
| `f := logger.Printf` (method value) | `f := flog.Logf(logger)` |
| `log.Printf(...)` (standard package) | `flog.Std.<Level>(...)` |
| `*log.Logger` | `*slog.Logger` |
| `log.New(w, "", 0)` | `flog.NewText(w)` |
| `log.New(w, "[Name] ", log.LstdFlags)` | `flog.New(w, "Name", true)` |
| `log.Default()` | `flog.Std` |

Rules worth knowing:

- **The message is the old rendered text.** Operators grep these lines, so
  the tool never rewrites a message; it renders the same string the old call
  printed. Fields are added next to it.
- **Level** comes from the format string, by `flog.Classify`: a leading
  `[DEBUG]` is Debug, `ERROR` or `[PANIC]` in the first 60 bytes is Error,
  `WARNING` or `Warning` there is Warn, everything else Info. The tool
  imports `flog.Classify`, so the rule lives in one place.
- **Fields** are added only for arguments that are side-effect free and
  whose verb is `%s %v %d %q`: a variable (`err`), a selector (`rec.Path`
  becomes `path`), a slice (`hash[:8]` becomes `hash`) or
  `filepath.Base(x)` (becomes `file`). Anything else stays in the message
  only. A format that starts with a verb (`"%s%s"`, a prefix and a payload,
  as in the documented JSON lines) gets no fields: the message is the
  payload. The text handler does not repeat a field whose value the message
  already shows, so text output stays what it was.
- A call nested in the arguments of another converted call is converted on a
  second internal pass.

## What it leaves for a person

The tool prints each as `MANUAL: file:line: reason` and changes nothing
there:

- an interface with a `Printf` method (`vfs.Logger`, `metadb.Logger`): give
  the type a `*slog.Logger` instead;
- `log.Fatal*` and `log.Panic*`: slog has no equivalent;
- `.SetOutput`, `.Writer` and the like on a converted logger (tests that
  capture output): build the logger over the buffer with `flog.NewText`;
- a `log.New` or a `Print`/`Println` with arguments it does not recognise.

After a run, build and fix what the compiler reports, then run the tests.
The receivers it treats as a logger are in `recvRE` in `main.go`; a new
logger variable with another name needs a line there.

## Hand edits after the tool

What the first conversion needed by hand, so a rerun on a newer tree can
repeat it (the compiler finds each one):

- `internal/metadb/db.go` and `internal/vfs/inodemap.go`: the `Logger`
  interface becomes `*slog.Logger` (nil means discard); `vfs.loggerWrapper`
  and `daemon.vfsLogger` go away.
- `internal/daemon/main.go`: `log.Fatal(err)` after the FUSE mount becomes an
  `Error` line and `os.Exit(1)`; `applyLogConfig` (new) sets format and level
  after each config load; the now-unused `log` import goes.
- `internal/daemon/warm_add.go`: the warmer's `Log` callback uses
  `flog.Printf(logger, "%s", line)` so a `WARNING:` line from the warmer is a
  Warn and a `[Warm]` line stays Info.
- `internal/config/config.go`: the `log_format` key, its env var and its
  validation.
- Tests that swapped a logger's output (`internal/warmup/cache_dir_test.go`,
  `evict_test.go`) assign `flog.New(buf, name, true)` to the package variable
  instead; test doubles with a `Printf` method (`nopLogger`, `discardLogger`)
  become `slog.New(slog.DiscardHandler)`.

## Tests

`go test ./scripts/slogify` covers each rewrite above, the import handling,
the fixed point and the MANUAL list.
