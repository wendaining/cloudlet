package containers

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
)

// Image is a Docker image prepared from an immutable digest reference.
// Reference is the canonical repository@digest reference, Digest is the
// manifest digest that identifies the image content, and ID is the local image
// identifier reported by Docker.
type Image struct {
	Reference string
	Digest    string
	ID        string
}

// PrepareImage ensures that the image identified by reference is present on the
// Docker Engine and verifies that the local image reports the requested digest.
// The reference must contain an immutable digest; a tag alone is rejected.
//
// A local image whose digest was already verified is reused without contacting
// the registry. Otherwise the image is pulled by digest; the caller controls the
// pull deadline through ctx. Pull failures wrap ErrImagePull, and identity
// failures wrap ErrImageDigestMismatch.
func (a *Adapter) PrepareImage(ctx context.Context, value string) (Image, error) {
	canonical, want, err := parseImageReference(value)
	if err != nil {
		return Image{}, err
	}
	if prepared, found, err := a.inspectPreparedImage(ctx, canonical, want); err != nil {
		return Image{}, err
	} else if found {
		return prepared, nil
	}
	response, err := a.docker.ImagePull(ctx, canonical, client.ImagePullOptions{})
	if err != nil {
		return Image{}, fmt.Errorf("pull image %s: %w: %w", canonical, ErrImagePull, err)
	}
	if err := response.Wait(ctx); err != nil {
		return Image{}, fmt.Errorf("pull image %s: %w: %w", canonical, ErrImagePull, err)
	}
	prepared, err := a.verifyPulledImage(ctx, canonical, want)
	if err != nil {
		return Image{}, err
	}
	return prepared, nil
}

// inspectPreparedImage returns a verified local image. A missing image is not an
// error; found is false so the caller can pull it.
func (a *Adapter) inspectPreparedImage(ctx context.Context, canonical string, want digest.Digest) (Image, bool, error) {
	result, err := a.docker.ImageInspect(ctx, canonical)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return Image{}, false, nil
		}
		return Image{}, false, fmt.Errorf("inspect image %s: %w", canonical, err)
	}
	prepared, err := verifyImage(result.InspectResponse, canonical, want)
	if err != nil {
		return Image{}, false, err
	}
	return prepared, true, nil
}

// verifyPulledImage inspects the image after a successful pull. Docker only
// reports the image through the requested reference after the manifest has been
// stored locally, so a missing image is a failed pull.
func (a *Adapter) verifyPulledImage(ctx context.Context, canonical string, want digest.Digest) (Image, error) {
	result, err := a.docker.ImageInspect(ctx, canonical)
	if err != nil {
		return Image{}, fmt.Errorf("inspect pulled image %s: %w", canonical, err)
	}
	return verifyImage(result.InspectResponse, canonical, want)
}

// verifyImage confirms that the inspected image carries the requested manifest
// digest. RepoDigests is the portable identity across Docker image stores; the
// image ID may be either the manifest or the configuration digest depending on
// the store. Repository names are not compared because registry mirrors may
// record an additional repository name for the same content.
func verifyImage(info image.InspectResponse, canonical string, want digest.Digest) (Image, error) {
	if info.ID == "" {
		return Image{}, fmt.Errorf("verify image %s: image ID is missing: %w", canonical, ErrImageDigestMismatch)
	}
	if _, err := digest.Parse(info.ID); err != nil {
		return Image{}, fmt.Errorf("verify image %s: image ID %q is not a valid digest: %w: %w", canonical, info.ID, ErrImageDigestMismatch, err)
	}
	for _, repoDigest := range info.RepoDigests {
		_, encoded, found := strings.Cut(repoDigest, "@")
		if !found {
			continue
		}
		got, err := digest.Parse(encoded)
		if err != nil {
			continue
		}
		if got == want {
			return Image{Reference: canonical, Digest: want.String(), ID: info.ID}, nil
		}
	}
	return Image{}, fmt.Errorf("verify image %s: Docker reports digests %v, want %s: %w", canonical, info.RepoDigests, want, ErrImageDigestMismatch)
}

// parseImageReference returns the canonical repository@digest reference and the
// requested digest. Tag-only references are rejected because they can resolve
// to different content over time.
func parseImageReference(value string) (string, digest.Digest, error) {
	if value == "" {
		return "", "", fmt.Errorf("validate image reference: reference is required: %w", fs.ErrInvalid)
	}
	if len(value) > maxImageLength {
		return "", "", fmt.Errorf("validate image reference %q: exceeds %d bytes: %w", value, maxImageLength, fs.ErrInvalid)
	}
	named, err := reference.ParseDockerRef(value)
	if err != nil {
		return "", "", fmt.Errorf("validate image reference %q: %w: %w", value, fs.ErrInvalid, err)
	}
	canonical, ok := named.(reference.Canonical)
	if !ok {
		return "", "", fmt.Errorf("validate image reference %q: an immutable digest is required: %w", value, fs.ErrInvalid)
	}
	want := canonical.Digest()
	if err := want.Validate(); err != nil {
		return "", "", fmt.Errorf("validate image reference %q: invalid digest: %w: %w", value, fs.ErrInvalid, err)
	}
	return canonical.String(), want, nil
}
