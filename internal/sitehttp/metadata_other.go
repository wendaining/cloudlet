//go:build !linux && !darwin

package sitehttp

import "io/fs"

func fileChangeTime(fs.FileInfo) (fileChange, bool) {
	return fileChange{}, false
}
