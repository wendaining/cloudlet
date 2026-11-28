package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/s3binding"
)

var s3BindingName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var s3BucketPart = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func validS3BindingInput(input apigen.S3BindingInput) bool {
	_, err := s3BindingEndpoint(input.Endpoint)
	if err != nil || len(input.Name) > 63 || !s3BindingName.MatchString(input.Name) ||
		len(input.Region) > 63 || !s3BindingName.MatchString(input.Region) ||
		len(input.Bucket) < 3 || len(input.Bucket) > 63 || input.SecretVersionId == uuid.Nil {
		return false
	}
	if _, err := netip.ParseAddr(input.Bucket); err == nil {
		return false
	}
	for _, part := range strings.Split(input.Bucket, ".") {
		if !s3BucketPart.MatchString(part) {
			return false
		}
	}
	return true
}

func s3BindingEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse S3 endpoint: %w", err)
	}
	if len(raw) > 512 || (endpoint.Scheme != "https" && endpoint.Scheme != "http") ||
		endpoint.Hostname() == "" || endpoint.User != nil || endpoint.ForceQuery ||
		endpoint.RawQuery != "" || strings.Contains(raw, "#") ||
		(endpoint.EscapedPath() != "" && endpoint.EscapedPath() != "/") {
		return nil, errors.New("invalid S3 endpoint")
	}
	if port := endpoint.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("invalid S3 endpoint port")
		}
	}
	return endpoint, nil
}

func verifyS3Binding(ctx context.Context, input apigen.S3BindingInput, value string, development bool) (string, error) {
	endpoint, err := s3BindingEndpoint(input.Endpoint)
	if err != nil {
		return "S3 端点配置无效", err
	}
	ip, _ := netip.ParseAddr(endpoint.Hostname())
	local := ip.Zone() == "" && (ip.Unmap().IsLoopback() || strings.EqualFold(strings.TrimSuffix(endpoint.Hostname(), "."), "localhost"))
	if endpoint.Scheme == "http" && !(development && local) {
		return "S3 端点需要使用 HTTPS", errors.New("plaintext S3 endpoint outside development loopback")
	}
	secret, err := s3binding.DecodeCredentials(value)
	if errors.Is(err, s3binding.ErrIncompleteCredentials) {
		return "S3 凭据缺少有效的访问密钥", err
	}
	if err != nil {
		return "S3 凭据格式无效", err
	}

	dialer := &net.Dialer{
		ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("parse resolved S3 address: %w", err)
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return fmt.Errorf("read resolved S3 address: %w", err)
			}
			ip = ip.Unmap()
			if (ip.IsLoopback() && development && local) || (ip.IsGlobalUnicast() && endpoint.Scheme == "https") {
				return nil
			}
			return fmt.Errorf("S3 address %s is prohibited", ip)
		},
	}
	transport := &http.Transport{DialContext: dialer.DialContext}
	defer transport.CloseIdleConnections()
	client := s3.New(s3.Options{
		BaseEndpoint:     aws.String(endpoint.String()),
		Credentials:      credentials.NewStaticCredentialsProvider(secret.AccessKeyID, secret.SecretAccessKey, secret.SessionToken),
		Region:           input.Region,
		UsePathStyle:     true,
		RetryMaxAttempts: 1,
		HTTPClient: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(input.Bucket)}); err != nil {
		var response *smithyhttp.ResponseError
		if errors.As(err, &response) {
			switch response.HTTPStatusCode() {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
				return "S3 存储桶不可用，请检查桶名称和凭据权限", fmt.Errorf("check S3 bucket access: %w", err)
			}
		}
		return "无法验证 S3 存储桶，请检查端点、区域和 TLS 配置", fmt.Errorf("check S3 bucket: %w", err)
	}
	return "", nil
}
