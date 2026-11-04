# README demos

The animated GIFs embedded in the [README](../../README.md) are rendered from
the asciinema recordings in this directory. The `.cast` files are the source of
truth: a GIF is regenerated from its cast, never edited by hand. Play a cast
with `asciinema play docs/demos/01-tui.cast`.

| Files | README section | Shows | Length |
|---|---|---|---|
| `00-intro` | top of the README | a tour cut from demos 01, 03, 04, 05, 07 and 08, with an end card | 50 s |
| `01-tui` | Awesome TUI | palette and agent tabs, chat and space view in split panes, a tab dragged into a 2×2, resize, zoom, swap | 64 s |
| `03-confinement` | Confinement | a read-only reviewer: its policy, `ls` works, `touch` is refused | 60 s |
| `04-sessions` | Sessions | detach and reattach to the same running agent, draft kept | 24 s |
| `05-operator` | Space operator | an operator agent creates a new box, drives it with `box use`, reports its answer and quota | 21 s |
| `06-box-use` | Box use | an operator asks another agent a question and sends it `/compact`, its pane live alongside | 29 s |
| `07-chat` | Chat | the human posts one message to the team; every agent answers, the reviewer hands a follow-up to the coordinator | 32 s |
| `08-devflow` | Development | chat, coordinator, dev and reviewer take a ticket from the chat to a merge | 60 s |

## How they were made

- Recorded with asciinema 2.4.0 in a 120×32 terminal on a showcase space with
  real agent logins. Each demo is driven by a scripted take (the team's
  recording scripts, not part of the repository): the script types the palette
  commands and keys with fixed pauses and waits for the screen to settle, so a
  retake reproduces the same scene. Where a demo uses the mouse, the recorded
  pointer positions are drawn into the cast so the GIF shows the clicks and
  drags.
- Demos 05–08 contain real agent replies. The script marks the waits for
  them, and those spans play 8× faster (05–07) or 16× faster (08) under a
  small `▸▸ 8x` or `▸▸ 16x` badge at the bottom right of the screen, so minutes
  of agent work take seconds. Marked agent waits are accelerated; other idle
  gaps are capped at two seconds in every demo.
- `00-intro` is not a recording of its own. A small montage tool cuts time
  ranges out of the final casts above, opens each clip on a full redraw of the
  source screen at its in-point under a one-line caption bar, joins the clips,
  and draws the end card. The original cast bytes are copied, never changed.
- Rendered with agg 1.9.0 in JetBrains Mono at 16 px on a black background
  with the VS Code terminal palette, then compressed losslessly with
  gifsicle 1.94:

```sh
agg --font-family "JetBrains Mono" --font-size 16 \
  --theme 000000,d0d0d0,000000,cd3131,0dbc79,e5e510,2472c8,bc3fbc,11a8cd,e5e5e5,666666,f14c4c,23d18b,f5f543,3b8eea,d670d6,29b8db,ffffff \
  docs/demos/01-tui.cast 01-tui.gif
gifsicle -O3 01-tui.gif -o docs/demos/01-tui.gif
```
