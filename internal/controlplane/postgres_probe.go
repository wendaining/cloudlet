package controlplane

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wendaining/cloudlet/internal/apigen"
)

var postgresBindingName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var postgresDatabaseName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,62}$`)
var postgresDNSName = regexp.MustCompile(`(?i)^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validPostgresBindingInput(input apigen.PostgresBindingInput) bool {
	return len(input.Name) <= 63 && postgresBindingName.MatchString(input.Name) &&
		validPostgresHost(input.Host) && input.Port >= 1 && input.Port <= 65535 &&
		postgresDatabaseName.MatchString(input.Database) && input.SecretVersionId != uuid.Nil
}

func validPostgresHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == ""
	}
	if len(host) > 253 {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if !postgresDNSName.MatchString(part) {
			return false
		}
	}
	return true
}

func verifyPostgresBinding(ctx context.Context, input apigen.PostgresBindingInput, value string, development bool) (string, error) {
	var secret struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(value), &secret); err != nil {
		return "PostgreSQL 凭据格式无效", fmt.Errorf("decode database credentials: %w", err)
	}
	if secret.Username == "" || secret.Password == "" || strings.ContainsRune(secret.Username, 0) || strings.ContainsRune(secret.Password, 0) {
		return "PostgreSQL 凭据缺少有效的用户名或密码", errors.New("invalid database credentials")
	}

	endpoint := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(secret.Username, secret.Password),
		Host:     net.JoinHostPort(input.Host, strconv.Itoa(input.Port)),
		Path:     "/" + input.Database,
		RawQuery: "sslmode=disable&sslnegotiation=postgres&sslcertmode=disable&target_session_attrs=any",
	}
	cfg, err := pgx.ParseConfig(endpoint.String())
	if err != nil {
		return "PostgreSQL 连接配置无效", fmt.Errorf("build database connection: %w", err)
	}
	cfg.RuntimeParams = nil
	cfg.Fallbacks = nil
	ip, _ := netip.ParseAddr(input.Host)
	local := ip.Unmap().IsLoopback() || strings.EqualFold(strings.TrimSuffix(input.Host, "."), "localhost")
	if !(development && local) {
		cfg.TLSConfig = &tls.Config{ServerName: input.Host, MinVersion: tls.VersionTLS12}
	}
	dialer := &net.Dialer{}
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse resolved database address: %w", err)
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return nil, fmt.Errorf("read resolved database address: %w", err)
		}
		ip = ip.Unmap()
		// Restrict plaintext probes to loopback after resolution.
		switch {
		case ip.IsLoopback() && development && local, ip.IsGlobalUnicast() && cfg.TLSConfig != nil:
			return dialer.DialContext(ctx, network, address)
		default:
			return nil, fmt.Errorf("database address %s is prohibited", ip)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "28P01", "28000":
				return "PostgreSQL 身份验证失败", fmt.Errorf("authenticate database connection: %w", err)
			case "3D000":
				return "PostgreSQL 数据库不存在", fmt.Errorf("select database: %w", err)
			}
		}
		return "无法连接 PostgreSQL，请检查主机、端口和 TLS 配置", fmt.Errorf("connect to database: %w", err)
	}
	pingErr := conn.Ping(ctx)
	closeErr := conn.Close(ctx)
	if err := errors.Join(pingErr, closeErr); err != nil {
		return "PostgreSQL 连接检查失败", fmt.Errorf("check and close database connection: %w", err)
	}
	return "", nil
}
