# README demos

The animated GIFs embedded in the [README](../../README.md) are rendered from
the asciinema recordings in this directory. The `.cast` files are the source of
truth: a GIF is regenerated from its cast, never edited by hand. Play a cast
with `asciinema play docs/demos/01-tui.cast`.

| Files | README section | Shows | Cast length (approx.) |
|---|---|---|---|
| `00-intro` | top of the README | one continuous take with real agents: a fresh yoloctl creates a space and one box, the mouse builds a four-pane view, the space operator reads another box's quota with `box use` and posts it as a table, the team takes a ticket from the chat to a merge, the human quotes the result | 2 min 30 s |
| `01-tui` | Awesome TUI | cut from `00`: the space chip, `box list`, tabs dragged into a 2×2, the pane menu zooming the chat, a plan link opening follow with its diagram, the space view scrolled | 63 s |
| `03-confinement` | Confinement | a read-only reviewer: its policy, `ls` works, `touch` is refused | 58 s |
| `04-sessions` | Sessions | detach and reattach to the same running agent, draft kept | 29 s |
| `05-operator` | Space operator | cut from `00`: the space operator reads another box's quota with `box use`, posts it in the chat and hands out a ticket | 29 s |
| `06-box-use` | Box use | cut from `00`: another agent's usage screen opened, read and closed through `box use`, then the quota table | 16 s |
| `07-chat` | Chat | cut from `00`: the zoomed chat with a ticket, threaded answers, the For me filter and a quote | 37 s |
| `08-devflow` | Development | cut from `00`: a ticket, the plan with its drawn diagram, the test and the review, the MERGED report | 23 s |

## How they were made

- Recorded on the v0.4.0 release, in a fresh yoloctl home with a throwaway Firecracker space, with asciinema 2.4.0
  in a 120×32 terminal. Agent logins were imported off camera. A scripted take types the commands and keys at a
  human pace, drives the mouse (the recorded pointer positions are drawn into the cast), and waits for the screen.
  Steps done off camera (logins, chat enrollment, the agents' one setup line) show only as a capped pause.
- `00-intro` is one continuous take with real agents: a Claude coder and Codex agents for the space operator, the
  reviewer and the tester. `01` and `05`–`08` are cut from it with a small montage tool, which replays the source up
  to each in-point and copies the clips' events unchanged. `03` and `04` are separate takes on Codex boxes, with no
  agent turn.
- Waits for agent work play faster under a small badge that shows the speed (`▸▸ 16x`; the quota scene plays at
  `▸▸ 4x`). Other idle gaps are capped at two seconds.
- Every demo opens on a title card, and `00-intro` also ends on an end card.
- The quote's mouse selection writes the text to the terminal clipboard (OSC 52). That one control sequence is
  removed from the published casts, so `asciinema play` never writes to the viewer's clipboard. The rest is the
  recorded output.
- Rendered with agg 1.9.0 in JetBrains Mono at 16 px on a black background
  with the VS Code terminal palette, then compressed losslessly with
  gifsicle 1.94:

```sh
agg --font-family "JetBrains Mono" --font-size 16 \
  --theme 000000,d0d0d0,000000,cd3131,0dbc79,e5e510,2472c8,bc3fbc,11a8cd,e5e5e5,666666,f14c4c,23d18b,f5f543,3b8eea,d670d6,29b8db,ffffff \
  docs/demos/01-tui.cast 01-tui.gif
gifsicle -O3 01-tui.gif -o docs/demos/01-tui.gif
```
