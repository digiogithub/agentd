# AGED-US-0002: logical-line reflow of screen and scrollback

Links: [[plans/api-gateway-plan]] [[design/api/semantic-model]] (section 3)

## What changed
`internal/screen` can now join soft-wrapped display rows into logical lines, as a pure
function over `*Snapshot` (scrollback and screen merged into one stream on the main screen;
screen only on the alternate screen).

## Files and symbols
- `internal/screen/reflow.go`: `Reflow`, `ReflowRows`, `StreamRows`, `ReflowOpts{Rules, Skip}`,
  `Line{Text, Role, Rows, Joined, Row}`, `Role*` and `Joined*` constants, internal `rowView`,
  `canJoin`, `markerHang`.
- `internal/screen/rules.go`: `Rules`, `GhostRules`, `DefaultRules` (WrapMargin, NoJoin,
  prompt marker, menu pointers feed the join rule).
- `internal/screen/ghost.go`: cell/column helpers shared with the region split (see US-0003).
- Tests: `internal/screen/reflow_test.go` (synthetic grids, widths 80/120/160, wide chars, NBSP,
  hard breaks, frames, scrollback), `regions_fixture_test.go` (`fake-wrap-80` vs `-120` give
  identical text).

## Heuristic (design 3.3)
Rows join when: both non-blank, same frame; row r is edge-filled (autowrap, no separator) or the
next word would not have fit within `avail - wrap_margin` (word wrap, one space); r+1 has the
logical line's content indent (hanging indent after `⏺ - * • ⎿ │ 1.`); r+1 is not a list marker,
prompt marker, menu pointer, box-drawing row or `NoJoin` match; neither row is verbatim (whole-row
non-default background) or a diff-gutter row. `Row.Wrapped` joins directly when set. Frame borders
`│┃║` at both ends of a row are stripped with one padding cell. Wide characters: column
arithmetic uses cell widths (Width 0 continuation cells skipped; the older layout without them
also works).

## Why
The harnesses word-wrap themselves and x/vt keeps no wrap flag, so API consumers (view/transcript)
need width-independent text.

## Verification
`gofmt -l .`, `go vet`, `go test -race ./internal/screen ./internal/session ./internal/config`.

## Known limits
- A row exactly as wide as the terminal followed by a short row is assumed to be a wrap.
- In a stream that shows the word-wrap shape (some row stops short of the edge), a full
  multi-word row is joined with a space; in a stream with no such evidence a full row joins
  without separator (plain autowrap, mid-word cut) and the continuation may start with the blank
  the cut fell on (kept).
- History (section 3.5) is a later story.
