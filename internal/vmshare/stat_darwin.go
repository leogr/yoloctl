package vmshare

import (
	"os"
	"syscall"
)

func accessTime(st os.FileInfo) uint32 {
	if stat, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint32(stat.Atimespec.Sec)
	}
	return uint32(st.ModTime().Unix())
}
