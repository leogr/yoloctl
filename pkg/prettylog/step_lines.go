package prettylog

import (
	"log/slog"
	"slices"
)

// Bound the pending text of unusually large or incomplete groups. At the
// limit we release their lines below the progress already printed to stderr.
const maxStepBytes = 4 << 20

type stepLines struct {
	id       string
	parent   *stepLines
	ended    bool
	lines    []string
	children []*stepLines // completion order
}

// groupStepLine processes transitions even when filtered, so quiet output
// still knows which parent owns a step. Called with the Logger lock held.
func (l *Logger) groupStepLine(r slog.Record, st Structure) error {
	if l.stepLines == nil {
		l.stepLines = make(map[string]*stepLines)
	}
	node := l.stepLines[st.Step]
	if node == nil {
		if len(l.stepLines) >= maxDepthIDs {
			l.flushStepLines()
		}
		node = &stepLines{id: st.Step, parent: l.stepLines[st.Parent]}
		l.stepLines[st.Step] = node
	}
	depth := l.depthOf(st)
	visible := l.o.Verbosity.allows(st)
	if st.State == string(StepRunning) {
		if !visible {
			return nil
		}
		l.endRun()
		l.write(l.stepLineText(r, st, depth))
		return l.err
	}

	if visible {
		l.endRun()
		text := l.stepLineText(r, st, depth)
		node.lines = append(node.lines, text)
		l.stepBytes += len(text) + 1
	}
	groupable := st.State == string(StepOK) || st.State == string(StepSkipped)
	if groupable && node.parent != nil && l.stepLines[node.parent.id] == node.parent {
		if !node.ended {
			node.parent.children = append(node.parent.children, node)
		}
		node.ended = true
	} else {
		l.writeStepLines(node)
	}
	if l.stepBytes >= maxStepBytes {
		l.flushStepLines()
	}
	return l.err
}

func (l *Logger) stepLineText(r slog.Record, st Structure, depth int) string {
	if r.Time.IsZero() {
		r.Time = l.now()
	}
	return l.render(r, st, depth).text
}

func (l *Logger) writeStepLines(node *stepLines) {
	delete(l.stepLines, node.id)
	for _, line := range node.lines {
		l.write(line)
		l.stepBytes -= len(line) + 1
	}
	for _, child := range node.children {
		l.writeStepLines(child)
	}
	node.lines, node.children = nil, nil
}

func (l *Logger) flushStepLines() {
	if len(l.stepLines) == 0 {
		return
	}
	l.endRun()
	var open []string
	for id, node := range l.stepLines {
		if !node.ended {
			open = append(open, id)
		}
	}
	slices.Sort(open)
	for _, id := range open {
		l.writeStepLines(l.stepLines[id])
	}
}
