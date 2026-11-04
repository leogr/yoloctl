package spacetelemetry

import "golang.org/x/sys/unix"

// StatfsUsage reports a filesystem's size and the space available to
// unprivileged writers.
func StatfsUsage(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	return Usage{Total: st.Blocks * uint64(st.Bsize), Free: st.Bavail * uint64(st.Bsize)}, nil
}
