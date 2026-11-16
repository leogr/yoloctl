package lima

import "time"

// GuestProbeTimeout includes starting limactl, its SSH transport, sudo and the
// guest interpreter. The in-guest socket deadline remains one second. Ten
// seconds accommodates cold transport while keeping diagnostic reads bounded.
const GuestProbeTimeout = 10 * time.Second

// BinaryInstallProbeTimeout bounds silent full-file hashes and loader checks
// during executable installation. It is a total budget, not output inactivity;
// ordinary diagnostic readers continue to use GuestProbeTimeout.
const BinaryInstallProbeTimeout = 2 * time.Minute
