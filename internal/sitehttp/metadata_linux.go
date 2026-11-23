package sitehttp

import (
	"io/fs"
	"syscall"
)

func fileChangeTime(info fs.FileInfo) (fileChange, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileChange{}, false
	}
	return fileChange{device: uint64(stat.Dev), inode: uint64(stat.Ino), sec: int64(stat.Ctim.Sec), nsec: int64(stat.Ctim.Nsec)}, true
}
