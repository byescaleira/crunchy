# internal/web — embedded control-panel UI

The server UI is server-rendered with [templ](https://templ.guide) + HTMX and
styled with Tailwind v4 + DaisyUI. Both the generated templ Go and the
compiled CSS are **committed**, so `go build` produces a single self-contained
binary with no Node or codegen at build time.

## Files

- `*.templ` — templ sources (layout, components, pages).
- `*_templ.go` — **generated** by `templ generate`; committed so builds don't
  need the templ CLI. Regenerate after editing a `.templ` file.
- `static/app.css` — **compiled** from `input.css` (Tailwind v4 + DaisyUI);
  committed and `go:embed`ded.
- `static/htmx.min.js` — vendored htmx; committed and `go:embed`ded.
- `input.css`, `package.json` — the CSS build pipeline (build-only).

## Regenerating

Install the templ CLI once:

```sh
go install github.com/a-h/templ/cmd/templ@latest
```

Then, after editing templates:

```sh
templ generate        # from the repo root, rewrites *_templ.go
```

To recompile the CSS (only when `input.css` or DaisyUI changes):

```sh
cd internal/web
npm install                       # one-time
npx tailwindcss -i input.css -o static/app.css --minify
```

`node_modules/` is gitignored; never commit it. Only `static/app.css` is shipped.