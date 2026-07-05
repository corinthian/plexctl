# Porting conventions — plex-voice (Python) → plexctl (Go)

Read this before touching any file. The behavior spec is the Python `plex-voice` repo — its module source, `DOCS.md`, `STATUS.md`, `LLM_REFERENCE.md`. This is a behavior-frozen port: mirror the Python line-for-line, including things you would do differently. Do not fix bugs, do not improve, do not add features. If Python omits a key in one branch and includes it as null in another, Go must too.

## Core mapping

- Python dict ↔ `jsonx.J` (`map[string]any`). Parse PMS JSON into `J`, project fields exactly like the Python does. Do not invent structs.
- `d.get(k, {})` → `jsonx.GetMap(d, k)`; `d.get(k, []) or []` iterated as dicts → `jsonx.MapList(d, k)`; truthiness (`if not x`) → `!jsonx.Truthy(x)`; numeric sort keys / counters → `jsonx.Num(v)`.
- `str(v)` on JSON scalars → `jsonx.AsStr(v)` (renders integral float64 without `.0` — PMS numbers arrive as float64).
- Python `None`-able params: `season: int | None` → `season *int`; optional strings → `""` means unset.
- `cli._out(result)` → `output.Out(result)`. Paths that `print(json.dumps(...))` directly (bypassing `_out`) → `output.Print(result)`. NDJSON → `output.EmitNDJSON(rows iter.Seq[jsonx.J], summary)`.
- `api.get/post/put/delete` → `api.Get/Post/Put/Delete(path, url.Values)` (print-and-exit). `api.try_get/try_put/try_delete` → `api.TryGet/TryPut/TryDelete` returning `(jsonx.J, error)`; the error is `*api.Error` with `.Message` (Python `e.message`) and `.Kind` (`"timeout"`/`"error"`). `api.plex_tv_get` → `api.PlexTVGet` (returns `any`; devices.json is a list — use `jsonx.Maps(v.([]any))` guardedly).
- Params: build `url.Values`; ints via `strconv.Itoa`. Order in the encoded URL is alphabetical — PMS does not care.
- Config: `config.Load()`, `config.Require(key)` (print-and-exit), `config.StringOr(cfg, key, default)`, `config.Defaults`.
- Error message strings must match the Python **exactly** where they are literals (e.g. `"no active queue on %s"`, the smart-refusal strings, `"ambiguous client name '%s' — ..."`). Where Python interpolates an exception's text, match the **prefix** exactly and let the Go error text differ after the colon.

## Rules

- Exit-code discipline lives in `output.Out` / `api.ExitOnError` — never call `os.Exit` yourself; call `output.Exit` only if you must mirror a bare `sys.exit(1)` after a hand-printed error (pattern: `output.Print(...)` + `output.Exit(1)` — see `clients.resolve`).
- Comments: keep them sparse and only for PMS quirks, mirroring the Python comments' content (the quirk documentation is part of the port).
- No new dependencies. go.mod is frozen: cobra, go-toml/v2, x/term.
- Do not run git commands. Do not touch files outside your assigned list.
- Do not modify: go.mod, go.sum, internal/{jsonx,config,api,output,testutil}, internal/commands/root.go, or another domain's files.
- Replace your package's `stub.go` with your real implementation file(s) — keep the **exact** public signatures from the stub (other packages compile against them). Delete stub.go when done.
- Your cobra command file(s) go in `package commands` (internal/commands/<domain>.go) and self-register:

```go
func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(newPauseCmd(), ...)
	})
}
```

- Flags mirror click exactly: names, shorthands (`--client`/`-c`, `--section`/`-s`, `--limit`/`-n`), defaults, choice validation (invalid choice → cobra error, which exits 2 upstream — that matches click's UsageError). Variadic args (`nargs=-1, required=True`) → `cobra.MinimumNArgs(1)`; optional trailing arg → `cobra.MaximumNArgs(1)` / `RangeArgs`.
- `click.IntRange(0,100)` → validate in RunE and return an error (exits 2 upstream).

## Tests

Use `testutil.Setup(t, srv.URL)` (httptest server + redirected `PLEXCTL_CONFIG_DIR`) and `testutil.Capture(t, fn)` (captures stdout, returns simulated exit code; -1 = no exit). Port the *intent* of the Python tests listed in your assignment — every JSON shape, error string, and quirk-guard assertion. Table tests preferred. Fake PMS handlers return recorded-style payloads; keep fixtures inline. Never talk to a real server in unit tests.

Run only your own package tests plus a full `go build ./...`:

```
go build ./... && go test ./internal/<yourpkg>/... ./internal/commands/
```

(Command-file tests for your domain go in `internal/commands/<domain>_test.go`; note other domains' commands may be stubs that panic — never invoke commands you don't own.)
