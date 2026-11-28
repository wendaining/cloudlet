//go:build integration

package containerpublish_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const s3AllowedBucket = "allowed-assets"
const s3DeniedBucket = "foreign-assets"
const s3AccessKey = "fixture-application-key"
const s3SecretKey = "fixture-application-secret"

func integrationS3(t *testing.T, ctx context.Context) (string, string, *s3.Client) {
	t.Helper()
	const image = "chrislusf/seaweedfs:3.97@sha256:bb05d66d2963b1cc48073190781c3dd29e2c4c88a2ae2986bf58e38c86d89c6e"
	const adminAccess, adminSecret = "fixture-admin-key", "fixture-admin-secret"
	policy := `{"identities":[{"name":"admin","credentials":[{"accessKey":"` + adminAccess + `","secretKey":"` + adminSecret + `"}],"actions":["Admin","Read","Write","List"]},{"name":"application","credentials":[{"accessKey":"` + s3AccessKey + `","secretKey":"` + s3SecretKey + `"}],"actions":["Read:` + s3AllowedBucket + `","Write:` + s3AllowedBucket + `","List:` + s3AllowedBucket + `"]}]}`
	configuration := filepath.Join(t.TempDir(), "identities.json")
	if err := os.WriteFile(configuration, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	instance, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("8443/tcp"),
		testcontainers.WithEntrypoint("/bin/sh"),
		testcontainers.WithCmd("-ec", "until test -f /tmp/cloudlet-start; do sleep 0.2; done; exec weed server -ip=localhost -ip.bind=0.0.0.0 -s3 -s3.port.https=8443 -s3.cert.file=/tmp/cloudlet.crt -s3.key.file=/tmp/cloudlet.key -s3.config=/tmp/cloudlet-s3.json"),
		testcontainers.WithFiles(testcontainers.ContainerFile{HostFilePath: configuration, ContainerFilePath: "/tmp/cloudlet-s3.json", FileMode: 0o644}),
	)
	testcontainers.CleanupContainer(t, instance)
	if err != nil {
		t.Fatal(err)
	}
	address, err := instance.ContainerIP(ctx)
	if err != nil || net.ParseIP(address) == nil {
		t.Fatalf("S3 container address %q: %v", address, err)
	}
	host, err := instance.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := instance.MappedPort(ctx, "8443/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ca, privateKey := s3FixtureCertificate(t, address, host)
	if err := instance.CopyToContainer(ctx, ca, "/tmp/cloudlet.crt", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := instance.CopyToContainer(ctx, privateKey, "/tmp/cloudlet.key", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := instance.CopyToContainer(ctx, []byte("ready"), "/tmp/cloudlet-start", 0o644); err != nil {
		t.Fatal(err)
	}
	endpoint := url.URL{Scheme: "https", Host: net.JoinHostPort(host, port.Port())}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("invalid S3 fixture CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	ready, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		response, err := httpClient.Get(endpoint.String())
		if err == nil {
			response.Body.Close()
			break
		}
		select {
		case <-ready.Done():
			t.Fatalf("S3 fixture did not respond over HTTPS: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	admin := s3.New(s3.Options{
		Credentials:  credentials.NewStaticCredentialsProvider(adminAccess, adminSecret, ""),
		BaseEndpoint: aws.String(endpoint.String()), HTTPClient: httpClient, Region: "us-east-1", UsePathStyle: true,
	})
	for _, bucket := range []string{s3AllowedBucket, s3DeniedBucket} {
		if _, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Fatal(err)
		}
	}
	privateEndpoint := url.URL{Scheme: "https", Host: net.JoinHostPort(address, "8443")}
	return privateEndpoint.String(), string(ca), admin
}

func s3FixtureCertificate(t *testing.T, hosts ...string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Cloudlet S3 fixture"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			cert.IPAddresses = append(cert.IPAddresses, ip)
		} else {
			cert.DNSNames = append(cert.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func serveS3TestApp(t *testing.T, ctx context.Context, image string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "s3app")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, "./testdata/s3app")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build application fixture: %v: %s", err, output)
	}
	instance, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath: binary, ContainerFilePath: "/usr/share/nginx/html/s3app", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/s3app").WithPort("8080/tcp")),
	)
	testcontainers.CleanupContainer(t, instance)
	if err != nil {
		t.Fatal(err)
	}
	ip, err := instance.ContainerIP(ctx)
	if err != nil || ip == "" {
		t.Fatalf("application fixture network address %q: %v", ip, err)
	}
	return "http://" + net.JoinHostPort(ip, "8080") + "/s3app"
}
