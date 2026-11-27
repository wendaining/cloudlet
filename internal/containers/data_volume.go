package containers

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	volumetypes "github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// DataVolume is a controller-owned Docker volume with one writable directory.
type DataVolume struct {
	Name      string
	MountPath string
	Directory string
}

// DataVolumeName derives the stable Docker volume name for one logical
// container. The hash keeps the name within the managed identifier limit.
func DataVolumeName(environment, projectID, containerName string) string {
	sum := sha256.Sum256([]byte(environment + "\x00" + projectID + "\x00" + containerName))
	return "cloudlet-vol-" + hex.EncodeToString(sum[:16])
}

// ValidateDataVolume checks a declared writable volume and the numeric
// non-root identity that will own its directory.
func ValidateDataVolume(volume DataVolume, user string) error {
	return volume.validate(user)
}

func (v DataVolume) validateLayout() error {
	if err := validateIdentifier("data volume", "name", v.Name); err != nil {
		return err
	}
	if !path.IsAbs(v.MountPath) || v.MountPath == "/" || path.Clean(v.MountPath) != v.MountPath ||
		!fs.ValidPath(strings.TrimPrefix(v.MountPath, "/")) || strings.ContainsAny(v.MountPath, "\\:\x00") ||
		v.Directory == "." || !fs.ValidPath(v.Directory) || path.Base(v.Directory) != v.Directory || strings.ContainsAny(v.Directory, "\\:\x00") {
		return fmt.Errorf("invalid writable volume directory %q/%q: %w", v.MountPath, v.Directory, fs.ErrInvalid)
	}
	if len(v.MountPath) > 4096 || len(v.Directory) > 255 {
		return fmt.Errorf("writable volume path or directory exceeds its size limit: %w", fs.ErrInvalid)
	}
	return nil
}

func (v DataVolume) validate(user string) error {
	if err := v.validateLayout(); err != nil {
		return err
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

// EnsureDataVolume creates the managed volume when absent and verifies its
// ownership labels. created is true only when this call created the volume.
func (a *Adapter) EnsureDataVolume(ctx context.Context, ownership Ownership, volume DataVolume) (bool, error) {
	if err := ownership.validate(); err != nil {
		return false, err
	}
	if err := volume.validateLayout(); err != nil {
		return false, err
	}
	result, err := a.docker.VolumeInspect(ctx, volume.Name, client.VolumeInspectOptions{})
	if err == nil {
		return false, verifyVolume(result.Volume, ownership)
	}
	if !cerrdefs.IsNotFound(err) {
		return false, fmt.Errorf("%w: inspect data volume %s: %w", ErrDataVolume, volume.Name, err)
	}
	if _, err := a.docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volume.Name, Labels: ownership.volumeLabels()}); err != nil {
		// A concurrent creator may have won. Verify the existing volume before
		// reporting the create failure.
		if existing, inspectErr := a.docker.VolumeInspect(ctx, volume.Name, client.VolumeInspectOptions{}); inspectErr == nil {
			return false, verifyVolume(existing.Volume, ownership)
		}
		return false, fmt.Errorf("%w: create data volume %s: %w", ErrDataVolume, volume.Name, err)
	}
	result, err = a.docker.VolumeInspect(ctx, volume.Name, client.VolumeInspectOptions{})
	if err != nil {
		return false, fmt.Errorf("%w: inspect created data volume %s: %w", ErrDataVolume, volume.Name, err)
	}
	return true, verifyVolume(result.Volume, ownership)
}

// FindDataVolume reports whether the managed volume exists and is owned by the
// requested ownership. A missing volume is not an error.
func (a *Adapter) FindDataVolume(ctx context.Context, ownership Ownership, volume DataVolume) (bool, error) {
	if err := ownership.validate(); err != nil {
		return false, err
	}
	if err := volume.validateLayout(); err != nil {
		return false, err
	}
	result, err := a.docker.VolumeInspect(ctx, volume.Name, client.VolumeInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("%w: inspect data volume %s: %w", ErrDataVolume, volume.Name, err)
	}
	return true, verifyVolume(result.Volume, ownership)
}

// RemoveDataVolume removes the managed volume after verifying its ownership
// labels. A missing volume is treated as already removed.
func (a *Adapter) RemoveDataVolume(ctx context.Context, ownership Ownership, volume DataVolume) error {
	found, err := a.FindDataVolume(ctx, ownership, volume)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if _, err := a.docker.VolumeRemove(ctx, volume.Name, client.VolumeRemoveOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("%w: remove data volume %s: %w", ErrDataVolume, volume.Name, err)
	}
	if _, err := a.docker.VolumeInspect(ctx, volume.Name, client.VolumeInspectOptions{}); err == nil {
		return fmt.Errorf("%w: remove data volume %s: volume still exists: %w", ErrDataVolume, volume.Name, fs.ErrInvalid)
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: verify removal of data volume %s: %w", ErrDataVolume, volume.Name, err)
	}
	return nil
}

func (o Ownership) volumeLabels() map[string]string {
	return map[string]string{
		LabelManaged:     "true",
		LabelEnvironment: o.Environment,
		LabelComponent:   ComponentRuntime,
		LabelProject:     o.Project,
		LabelResource:    o.Resource,
		LabelVolume:      "writable",
	}
}

func verifyVolume(info volumetypes.Volume, ownership Ownership) error {
	if info.Name == "" {
		return fmt.Errorf("verify data volume: name is missing: %w", ErrNotOwned)
	}
	for key, want := range ownership.volumeLabels() {
		got, found := info.Labels[key]
		if !found || got != want {
			return fmt.Errorf("%w: data volume %s label %s is %q, want %q", ErrNotOwned, info.Name, key, got, want)
		}
	}
	return nil
}
