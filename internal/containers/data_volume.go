package containers

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/moby/moby/client"
)

// DataVolume is a controller-owned Docker volume with one writable directory.
type DataVolume struct {
	Name      string
	MountPath string
	Directory string
}

func (v DataVolume) validate(user string) error {
	if err := validateIdentifier("volume name", v.Name); err != nil {
		return err
	}
	if !path.IsAbs(v.MountPath) || v.MountPath == "/" || path.Clean(v.MountPath) != v.MountPath ||
		!fs.ValidPath(strings.TrimPrefix(v.MountPath, "/")) || strings.ContainsAny(v.MountPath, "\\:\x00") ||
		v.Directory == "." || !fs.ValidPath(v.Directory) || path.Base(v.Directory) != v.Directory || strings.ContainsAny(v.Directory, "\\:\x00") {
		return fmt.Errorf("invalid writable volume directory %q/%q: %w", v.MountPath, v.Directory, fs.ErrInvalid)
	}
	_, _, err := volumeUser(user)
	return err
}

func volumeUser(user string) (int, int, error) {
	userID, groupID, ok := strings.Cut(user, ":")
	uid, uidErr := strconv.ParseUint(userID, 10, 31)
	gid, gidErr := strconv.ParseUint(groupID, 10, 31)
	if !ok || uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
		return 0, 0, fmt.Errorf("writable volume requires numeric non-root user and group IDs: %w", fs.ErrInvalid)
	}
	return int(uid), int(gid), nil
}

func (a *Adapter) initializeVolumeDirectory(ctx context.Context, id string, volume DataVolume, user string) error {
	uid, gid, err := volumeUser(user)
	if err != nil {
		return err
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: volume.Directory, Typeflag: tar.TypeDir, Mode: 0o700, Uid: uid, Gid: gid}); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if _, err := a.docker.CopyToContainer(ctx, id, client.CopyToContainerOptions{
		DestinationPath: volume.MountPath, Content: &archive, CopyUIDGID: true,
	}); err != nil {
		return fmt.Errorf("initialize volume %s directory %s in container %s: %w", volume.Name, volume.Directory, id, err)
	}
	return nil
}
