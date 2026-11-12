| Ticket | Owner | Stage | Branch / SHA | Note |
|---|---|---|---|---|
| feat-pretty-logger A foundation | opus-dev2 | ▶️ Build | `feat/prettylog-foundation` cand4 @ `ac125ba` (on `85f67be`) | leogr chose --log-level debug/info/warn/error + -q (-v stays version) → cand5, then delta review + gate; B/C/E rebase after; foundation done (core, adapter, -q/-v/-vv/--color, error hook, chat glyphs, digest), make check PASS except known in-box test; Parallel Build: B builds (opus-dev3), C CLI (sol-dev3), E background logs + rotation (sol-dev); D Falco+TUI (sol-dev2) after A merges |
| feat-pretty-logger B builds | opus-dev3 | ⏳ QA | `feat/prettylog-builds` cand5 @ `564fab4` (on A cand4) | astra SOURCE ACCEPT; gate + live trial held until rebased on A cand5 (--log-level); log transport VM→host (protocol 3), image builds as collapsible steps, inline step block; make check PASS except known in-box test; review after A; needs a live VM/BuildKit run in QA |
| feat-pretty-logger C CLI | sol-dev3 | ⏳ QA | `6bb6102` (cand2) | accepted; rebase on A, then gate + Mac live trial |
| feat-pretty-logger E background | astra-architect | ▶️ Review | tree `ad64feb` | log rotation, timestamped background logs; rebase on A |
| fix-follow-title-space | astra-architect | ▶️ Review | `74fdfe7` | follow title = "follow · <path>" |
| fix-spaceview-scrollbar-border | astra-architect | ▶️ Review | `56a57bc` | scroll bar overlaps the space frame |
| fix-markdown-wide-tables | sol-dev3 | ⏸ Plan | — | wide tables break; preview for leogr first |
| feat-falco-audit | — | ⏸ Plan | `d112d6a` | Mac trial PASS; rebase after the logger |
| feat-falco-escape-rules | — | ⏳ Build | — | leogr GO; after the logger Falco part |
| feat-chat-images | opus-dev2 | ⏳ Build | — | after the logger foundation; copy option B check first |
| feat-mermaid-render | sol-dev2 | ▶️ Build | — | Phase 0: reproducible WASM reached; pauses for logger D |
| feat-falco-rules-manage | — | ⚪ Plan | — | queued |
| feat-chat-export | — | ⏸ Plan | — | later, not urgent |
| feat-agent-recipes | — | ⏸ Plan | — | very low priority |
| feat-quickstart-v2 | — | ⏸ Plan | — | on hold |
