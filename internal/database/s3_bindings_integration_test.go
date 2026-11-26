//go:build integration

package database_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
)

func TestS3BindingHTTP(t *testing.T) {
	ctx := t.Context()
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const allowedBucket = "allowed-media"
	const deniedBucket = "private-media"
	const region = "us-east-1"
	const adminKey, adminSecret = "fixture-admin-key", "fixture-admin-secret"
	const writeKey, writeSecret = "fixture-write-key", "fixture-write-secret"
	const image = "chrislusf/seaweedfs:3.97@sha256:bb05d66d2963b1cc48073190781c3dd29e2c4c88a2ae2986bf58e38c86d89c6e"
	identities, err := json.Marshal(map[string]any{"identities": []map[string]any{
		{"name": "admin", "credentials": []map[string]string{{"accessKey": adminKey, "secretKey": adminSecret}},
			"actions": []string{"Admin", "List", "Read", "Write"}},
		{"name": "write-only", "credentials": []map[string]string{{"accessKey": writeKey, "secretKey": writeSecret}},
			"actions": []string{"Write"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	seaweed, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("8333/tcp"),
		testcontainers.WithCmd("server", "-ip=localhost", "-ip.bind=0.0.0.0", "-s3", "-s3.config=/etc/cloudlet-s3.json"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(string(identities)), ContainerFilePath: "/etc/cloudlet-s3.json", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/").WithPort("8333/tcp").WithStatusCodeMatcher(
			func(code int) bool { return code == http.StatusForbidden },
		)),
	)
	testcontainers.CleanupContainer(t, seaweed)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := seaweed.PortEndpoint(ctx, "8333/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	admin := s3.New(s3.Options{BaseEndpoint: aws.String(endpoint), Region: region, UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(adminKey, adminSecret, "")})
	for _, bucket := range []string{allowedBucket, deniedBucket} {
		if _, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Fatal(err)
		}
	}

	const password = "integration-account-password"
	emails := []string{"s3-owner@cloudlet.local", "s3-other@cloudlet.local"}
	owner, other := makeProjectOwners(t, store, emails, password)
	key := make([]byte, 32)
	createSecret := func(project, name, accessKey, secretKey string) uuid.UUID {
		t.Helper()
		value, err := json.Marshal(map[string]string{"access_key_id": accessKey, "secret_access_key": secretKey})
		if err != nil {
			t.Fatal(err)
		}
		version, err := store.CreateProjectSecretVersion(ctx, project, name, string(value), key)
		if err != nil {
			t.Fatal(err)
		}
		return uuid.MustParse(version.ID)
	}
	valid := createSecret(owner.ID, "storage", adminKey, adminSecret)
	denied := createSecret(owner.ID, "write_only", writeKey, writeSecret)
	foreign := createSecret(other.ID, "storage", adminKey, adminSecret)

	handler, cleanup := controlplane.NewHandler(store, config.Config{Instance: "integration", Environment: "development", PublicOrigin: "http://localhost", SecretKey: key}, slog.Default())
	t.Cleanup(cleanup)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ownerHTTP := newSessionClient(t)
	otherHTTP := newSessionClient(t)
	authenticatedAPI(t, server.URL, ownerHTTP, emails[0], password)
	authenticatedAPI(t, server.URL, otherHTTP, emails[1], password)

	root := server.URL + "/api/v1/projects/" + owner.ID + "/s3-bindings"
	body := func(response *http.Response) string {
		t.Helper()
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{adminKey, adminSecret, writeKey, writeSecret} {
			if strings.Contains(string(data), secret) {
				t.Fatal("response contains a secret")
			}
		}
		return string(data)
	}
	input := apigen.S3BindingInput{Name: "media", Endpoint: endpoint, Region: region, Bucket: allowedBucket, SecretVersionId: valid}
	response := memberRequest(t, ownerHTTP, http.MethodPost, root, input, http.StatusCreated)
	if response.Header.Get("Location") != "/api/v1/projects/"+owner.ID+"/s3-bindings/media" {
		t.Fatal("incorrect binding location")
	}
	var created, read apigen.S3Binding
	if err := json.Unmarshal([]byte(body(response)), &created); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body(memberRequest(t, ownerHTTP, http.MethodGet, root+"/media", nil, http.StatusOK))), &read); err != nil {
		t.Fatal(err)
	}
	if created.Name != input.Name || created.Endpoint != input.Endpoint || created.Region != input.Region ||
		created.Bucket != allowedBucket || created.SecretVersionId != valid || created.VerifiedAt.IsZero() || created != read {
		t.Fatalf("unexpected binding: created=%+v read=%+v", created, read)
	}
	body(memberRequest(t, ownerHTTP, http.MethodPost, root, input, http.StatusConflict))
	body(memberRequest(t, otherHTTP, http.MethodGet, root+"/media", nil, http.StatusNotFound))

	input.Name, input.Bucket, input.SecretVersionId = "denied", deniedBucket, denied
	failed := body(memberRequest(t, ownerHTTP, http.MethodPost, root, input, http.StatusUnprocessableEntity))
	if !strings.Contains(failed, "凭据权限") {
		t.Fatalf("expected permission diagnostic, got %s", failed)
	}
	body(memberRequest(t, ownerHTTP, http.MethodGet, root+"/denied", nil, http.StatusNotFound))
	input.Name, input.SecretVersionId = "foreign", foreign
	body(memberRequest(t, ownerHTTP, http.MethodPost, root, input, http.StatusNotFound))
	body(memberRequest(t, ownerHTTP, http.MethodGet, root+"/foreign", nil, http.StatusNotFound))
}
