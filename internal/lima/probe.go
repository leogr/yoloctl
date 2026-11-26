package lima

import "time"

// GuestProbeTimeout bounds a short VM observation attempt, including the
// limactl and SSH overhead. Command-owned reads that can wait for a reply use
// the caller context.
const GuestProbeTimeout = 10 * time.Second

// BinaryInstallProbeTimeout bounds silent full-file hashes and loader checks
// during executable installation. It is a total budget, not output inactivity;
// it is separate from the short observation attempt.
const BinaryInstallProbeTimeout = 2 * time.Minute
