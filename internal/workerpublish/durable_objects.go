package workerpublish

import (
	"context"
	"fmt"
	"maps"

	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/durableobjects"
)

func (s *Service) claimDurableVolume(ctx context.Context, execution database.WorkerExecution, namespaces []durableobjects.Namespace) (*containers.DataVolume, error) {
	storage, err := s.store.ClaimWorkerDurableStorage(ctx, execution, namespaces)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{
		containers.LabelManaged: "true", containers.LabelEnvironment: s.instance, containers.LabelComponent: "durable-objects",
		containers.LabelProject: execution.ProjectID.String(), containers.LabelResource: execution.WorkerID.String(),
	}
	result, err := s.docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: storage.VolumeName, Driver: "local", Labels: labels})
	if err != nil {
		return nil, fmt.Errorf("prepare Worker Durable Object volume %s: %w", storage.VolumeName, err)
	}
	if result.Volume.Name != storage.VolumeName || result.Volume.Driver != "local" || len(result.Volume.Options) != 0 || !maps.Equal(result.Volume.Labels, labels) {
		return nil, fmt.Errorf("Worker Durable Object volume %s does not match its recorded ownership", storage.VolumeName)
	}
	return &containers.DataVolume{Name: storage.VolumeName, MountPath: durableobjects.StoragePath, Directory: durableobjects.StorageDirectory}, nil
}
