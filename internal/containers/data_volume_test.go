package containers

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestDataVolumeNameIsStableAndBounded(t *testing.T) {
	first := DataVolumeName("instance", "project", "web")
	second := DataVolumeName("instance", "project", "web")
	if first != second {
		t.Fatalf("DataVolumeName is not deterministic: %q != %q", first, second)
	}
	if len(first) > maxIdentifierLength || !strings.HasPrefix(first, "cloudlet-vol-") {
		t.Fatalf("DataVolumeName = %q", first)
	}
	if other := DataVolumeName("instance", "project", "other"); other == first {
		t.Fatalf("different containers share volume name %q", first)
	}
	if err := validateIdentifier("data volume", "name", first); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDataVolume(t *testing.T) {
	valid := DataVolume{Name: DataVolumeName("instance", "project", "web"), MountPath: "/data", Directory: "state"}
	if err := ValidateDataVolume(valid, "1000:1000"); err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		volume DataVolume
		user   string
	}{
		"root mount":        {DataVolume{Name: valid.Name, MountPath: "/", Directory: "state"}, "1000:1000"},
		"relative mount":    {DataVolume{Name: valid.Name, MountPath: "data", Directory: "state"}, "1000:1000"},
		"unclean mount":     {DataVolume{Name: valid.Name, MountPath: "/data/../data", Directory: "state"}, "1000:1000"},
		"nested directory":  {DataVolume{Name: valid.Name, MountPath: "/data", Directory: "a/b"}, "1000:1000"},
		"dot directory":     {DataVolume{Name: valid.Name, MountPath: "/data", Directory: "."}, "1000:1000"},
		"non numeric user":  {DataVolume{Name: valid.Name, MountPath: "/data", Directory: "state"}, "pwuser"},
		"root user":         {DataVolume{Name: valid.Name, MountPath: "/data", Directory: "state"}, "0:1000"},
		"root group":        {DataVolume{Name: valid.Name, MountPath: "/data", Directory: "state"}, "1000:0"},
		"missing group":     {DataVolume{Name: valid.Name, MountPath: "/data", Directory: "state"}, "1000"},
		"invalid volume":    {DataVolume{Name: "", MountPath: "/data", Directory: "state"}, "1000:1000"},
		"invalid directory": {DataVolume{Name: valid.Name, MountPath: "/data", Directory: "a\\b"}, "1000:1000"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateDataVolume(test.volume, test.user); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("ValidateDataVolume() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}
