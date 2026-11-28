// The integration fixture checks object access from a managed application
// container using the AWS SDK and the injected connection file.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/wendaining/cloudlet/internal/s3binding"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	path := os.Getenv("CLOUDLET_S3_CONFIG_FILE")
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o222 != 0 {
		return errors.New("S3 configuration file is writable")
	}
	var config struct {
		Endpoint string `json:"endpoint"`
		Region   string `json:"region"`
		Bucket   string `json:"bucket"`
		s3binding.Credentials
	}
	if err := json.NewDecoder(file).Decode(&config); err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(os.Getenv("FIXTURE_CA"))) {
		return errors.New("invalid S3 test CA")
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(config.Endpoint), Region: config.Region, UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials:                credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, config.SessionToken),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		HTTPClient:                 &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const object = "uploaded-picture"
	const content = "actual object contents"
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(object), Body: strings.NewReader(content)}); err != nil {
		return fmt.Errorf("write authorized bucket: %w", err)
	}
	result, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String(object)})
	if err != nil {
		return fmt.Errorf("read authorized bucket: %w", err)
	}
	data, err := io.ReadAll(result.Body)
	if err := errors.Join(err, result.Body.Close()); err != nil {
		return err
	}
	if string(data) != content {
		return errors.New("retrieved object differs from uploaded object")
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(os.Getenv("FIXTURE_DENIED_BUCKET")), Key: aws.String(object), Body: strings.NewReader(content)})
	var response *smithyhttp.ResponseError
	if !errors.As(err, &response) || response.HTTPStatusCode() != http.StatusForbidden {
		return fmt.Errorf("foreign bucket did not return 403: %v", err)
	}
	server := http.Server{
		Addr: ":8080", ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "S3 bucket access verified")
		}),
	}
	return server.ListenAndServe()
}
