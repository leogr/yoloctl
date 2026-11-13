| Situation | Before | After |
|---|---|---|
| Agent exits | box keeps running ("exited") | box stops ("stopped · exited 0") |
| Session holder lost/killed | box keeps running, agent maybe orphaned | box stops |
| Agent fails at launch | error, box keeps running | error, box stops |
| attach after the agent exited | new session in the running container | starts the box, then one session (resumes the conversation) |
| --no-attach, exec-only, agentless without session_command | keeps running | same (no main process) |
| agentless with session_command, workload ends | keeps running | box stops |
| box exec shells, port forwards at session end | keep running | end with the box |
| Codex app-server between sessions | killed by the reaper | ends with the box; reaper removed |
| box stop | succeeds only if every step was acknowledged, else uncertain | reads the container: not running → stopped, running → stop failed |
| box stop during another operation on the box | waits | cancels it, then stops |
| two stops at once | second waits | second joins the first |
| box start fails | can be left half started, or uncertain | stops what it started, reports the state it read |
| old-runtime box | sessions + reaper | next launch says "recreate this box" |
| session.daemons in a profile | accepted | rejected |
