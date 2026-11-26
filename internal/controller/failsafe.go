package controller

import "time"

// controllerFailSafe bounds one single wait that no caller can end: an
// exchange with a client that went silent, or a worker's checkpoint and
// output joins after it exited. It never bounds a whole operation. Tests may
// shorten it.
var controllerFailSafe = 2 * time.Minute
