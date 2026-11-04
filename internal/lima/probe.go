package lima

import "time"

// GuestProbeTimeout includes starting limactl, its SSH transport, sudo and the
// guest interpreter. The in-guest socket deadline remains one second. Ten
// seconds accommodates cold transport while keeping diagnostic reads bounded.
const GuestProbeTimeout = 10 * time.Second
