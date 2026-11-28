package containerpublish

import (
	"errors"
	"io/fs"
	"testing"
)

func TestContainerS3EndpointRequiresReachableHTTPS(t *testing.T) {
	for _, endpoint := range []string{"https://storage.example.org", "https://[2001:db8::1]:8443"} {
		if err := validateContainerS3Endpoint(endpoint); err != nil {
			t.Errorf("valid endpoint %q: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"https://localhost.", "https://127.0.0.1", "https://[::1]", "http://s3.example.org", "https://"} {
		if err := validateContainerS3Endpoint(endpoint); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("endpoint %q did not fail validation: %v", endpoint, err)
		}
	}
}

func TestValidateRequestRejectsS3ConfigOverrides(t *testing.T) {
	name := "assets"
	for _, modify := range []func(*Request){
		func(r *Request) { r.Env = []string{s3ConfigFileEnv + "=/other"} },
		func(r *Request) { r.DataVolume = &DataVolumeRequest{MountPath: "/" + s3ConfigPath, Directory: "data"} },
	} {
		request := validRequest()
		request.S3Binding = &name
		modify(&request)
		if _, err := ValidateRequest(request); !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("overlapping S3 configuration was not rejected: %v", err)
		}
	}
}
