//go:build integration

package containerpublish_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const businessPassword = "business-fixture-p@ss:/word"
const businessDatabase = "business"
const businessUser = "application"

func businessPostgres(t *testing.T, ctx context.Context, image string) (*pgx.Conn, string, string) {
	t.Helper()
	instance, err := postgres.Run(ctx, image,
		postgres.WithDatabase(businessDatabase), postgres.WithUsername(businessUser),
		postgres.WithPassword(businessPassword), testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, instance)
	if err != nil {
		t.Fatal(err)
	}
	hostAddress, err := instance.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hostPort, err := instance.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	connection := url.URL{Scheme: "postgres", User: url.UserPassword(businessUser, businessPassword),
		Host: net.JoinHostPort(hostAddress, hostPort.Port()), Path: "/" + businessDatabase, RawQuery: "sslmode=disable"}
	database, err := pgx.Connect(ctx, connection.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.WithoutCancel(ctx)); err != nil {
			t.Error(err)
		}
	})
	host, err := instance.ContainerIP(ctx)
	if err != nil || net.ParseIP(host) == nil {
		t.Fatalf("business PostgreSQL container IP %q: %v", host, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Cloudlet business fixture"},
		IPAddresses: []net.IP{net.ParseIP(host)}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := instance.CopyToContainer(ctx, ca, "/tmp/cloudlet-business.crt", 0644); err != nil {
		t.Fatal(err)
	}
	if err := instance.CopyToContainer(ctx, privateKey, "/tmp/cloudlet-business.key", 0600); err != nil {
		t.Fatal(err)
	}
	code, output, err := instance.Exec(ctx, []string{"chown", "postgres", "/tmp/cloudlet-business.key"})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		log, readErr := io.ReadAll(output)
		t.Fatalf("set TLS key owner: %d: %s (%v)", code, log, readErr)
	}
	for _, statement := range []string{
		"ALTER SYSTEM SET ssl_cert_file = '/tmp/cloudlet-business.crt'",
		"ALTER SYSTEM SET ssl_key_file = '/tmp/cloudlet-business.key'",
		"ALTER SYSTEM SET ssl = 'on'",
		"SELECT pg_reload_conf()",
	} {
		if _, err := database.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var mode string
		if err := database.QueryRow(ctx, "SHOW ssl").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode == "on" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("business PostgreSQL did not enable TLS")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return database, host, string(ca)
}

// The application runs psql as its non-root container user and starts a real
// HTTP listener only after the business database write and query succeed.
const businessAppCommand = `
printf '%s' "$FIXTURE_CA" > /tmp/cloudlet-test-ca.pem
export SSL_CERT_FILE=/tmp/cloudlet-test-ca.pem
test ! -w "$CLOUDLET_POSTGRES_URL_FILE"
uri=$(cat "$CLOUDLET_POSTGRES_URL_FILE")
psql --no-psqlrc --no-password --set=ON_ERROR_STOP=1 --dbname="$uri" -q -c 'CREATE TABLE IF NOT EXISTS issue172_business_rows (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY)' -c 'INSERT INTO issue172_business_rows DEFAULT VALUES'
count=$(psql --no-psqlrc --no-password --set=ON_ERROR_STOP=1 --dbname="$uri" -Atc 'SELECT count(*) FROM issue172_business_rows')
exec perl -MIO::Socket::INET -e '
    my $server = IO::Socket::INET->new(LocalPort => 8080, Listen => 8, ReuseAddr => 1) or die $!;
    while (my $client = $server->accept()) {
        while (my $line = <$client>) { last if $line eq "\r\n"; }
        print $client "HTTP/1.1 200 OK\r\nContent-Length: ".length($ARGV[0])."\r\nConnection: close\r\n\r\n$ARGV[0]";
        close $client;
    }
' "$count"
`

func assertBusinessRows(t *testing.T, ctx context.Context, store *pgx.Conn, want int) {
	t.Helper()
	var count int
	if err := store.QueryRow(ctx, "SELECT count(*) FROM issue172_business_rows").Scan(&count); err != nil || count != want {
		t.Fatalf("business row count = %d, want %d: %v", count, want, err)
	}
}
