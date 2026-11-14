package agentbox

// LauncherSupport is how a box's container takes a managed session (stop-box
// route M-b, contract sections 2, 7 and 8; directive 56).
type LauncherSupport struct {
	// NeedsRecreate: a confined box whose record does not say that its main
	// process is the launcher (created before it), or one on the old
	// upstream runtime.
	NeedsRecreate bool
	// NeedsRestart: a running privileged box whose endpoint refuses the
	// main-session request (started before it).
	NeedsRestart bool
	// Exec: the session starts with today's exec and no warning (a privileged
	// box while its main-session request is not available in this build).
	Exec bool
}

// RecreateWarning is the directive-56 warning for a box that needs recreate.
func RecreateWarning(name string) string {
	return "box " + name + " was created before this version; recreate it (box recreate " + name + "): data stored only inside the container is lost"
}

// RestartWarning is the warning for a privileged box that needs a restart but
// runs something now.
func RestartWarning(name string) string {
	return "box " + name + " runs an endpoint from before this version; restart it (box restart " + name + ") when nothing runs in it"
}
