package smb_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restic/restic/internal/backend/smb"
	"github.com/restic/restic/internal/backend/test"
	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/options"
	"github.com/restic/restic/internal/restic"
	rtest "github.com/restic/restic/internal/test"
)

func mkdir(t testing.TB, dir string) {
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		t.Fatal(err)
	}
}

func runSamba(ctx context.Context, t testing.TB, dir, key, secret string) func() {
	mkdir(t, filepath.Join(dir, "config"))
	mkdir(t, filepath.Join(dir, "root"))

	cmd := exec.CommandContext(ctx, "minio",
		"server",
		"--address", "127.0.0.1:9000",
		"--config-dir", filepath.Join(dir, "config"),
		filepath.Join(dir, "root"))
	cmd.Env = append(os.Environ(),
		"MINIO_ACCESS_KEY="+key,
		"MINIO_SECRET_KEY="+secret,
	)
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		t.Fatal(err)
	}

	// wait until the TCP port is reachable
	var success bool
	for i := 0; i < 100; i++ {
		time.Sleep(200 * time.Millisecond)

		c, err := net.Dial("tcp", "localhost:9000")
		if err == nil {
			success = true
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			break
		}
	}

	if !success {
		t.Fatal("unable to connect to minio server")
		return nil
	}

	return func() {
		err = cmd.Process.Kill()
		if err != nil {
			t.Fatal(err)
		}

		// ignore errors, we've killed the process
		_ = cmd.Wait()
	}
}

func newRandomCredentials(t testing.TB) (key, secret string) {
	buf := make([]byte, 10)
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		t.Fatal(err)
	}
	key = hex.EncodeToString(buf)

	_, err = io.ReadFull(rand.Reader, buf)
	if err != nil {
		t.Fatal(err)
	}
	secret = hex.EncodeToString(buf)

	return key, secret
}

func findSMBServerBinary() string {
	for _, dir := range strings.Split(rtest.TestSMBPath, ":") {
		testpath := filepath.Join(dir, "samba")
		_, err := os.Stat(testpath)
		if !errors.Is(err, os.ErrNotExist) {
			return testpath
		}
	}

	return ""
}

var smbServer = findSMBServerBinary()

func newTestSuite(t testing.TB) *test.Suite {
	return &test.Suite{
		// NewConfig returns a config for a new temporary backend that will be used in tests.
		NewConfig: func() (interface{}, error) {
			dir, err := os.MkdirTemp(rtest.TestTempDir, "restic-test-smb-")
			if err != nil {
				return nil, err
			}
			// smbcfg, err := smb.ParseConfig(os.Getenv("RESTIC_TEST_SMB_REPOSITORY"))
			// if err != nil {
			// 	t.Fatal(err)
			// }

			t.Logf("create new backend at %v", dir)

			cfg := smb.NewConfig()
			cfg.Path = dir
			cfg.User = os.Getenv("RESTIC_TEST_SMB_USERNAME")
			cfg.Password = options.NewSecretString(os.Getenv("RESTIC_TEST_SMB_PASSWORD"))
			cfg.Connections = smb.DefaultConnections
			cfg.IdleTimeout = smb.DefaultIdleTimeout
			cfg.Domain = smb.DefaultDomain

			return cfg, nil
		},

		// CreateFn is a function that creates a temporary repository for the tests.
		Create: func(config interface{}) (restic.Backend, error) {
			cfg := config.(smb.Config)
			return smb.Create(context.TODO(), cfg)
		},

		// OpenFn is a function that opens a previously created temporary repository.
		Open: func(config interface{}) (restic.Backend, error) {
			cfg := config.(smb.Config)
			return smb.Open(context.TODO(), cfg)
		},

		// CleanupFn removes data created during the tests.
		Cleanup: func(config interface{}) error {
			cfg := config.(smb.Config)
			if !rtest.TestCleanupTempDirs {
				t.Logf("leaving test backend dir at %v", cfg.Path)
			}

			rtest.RemoveAll(t, cfg.Path)
			return nil
		},
	}
}

func TestBackendSMB(t *testing.T) {
	defer func() {
		if t.Skipped() {
			rtest.SkipDisallowed(t, "restic/backend/smb.TestBackendSMB")
		}
	}()

	if smbServer == "" {
		t.Skip("smb server binary not found")
	}
	vars := []string{
		"RESTIC_TEST_SMB_USERNAME",
		"RESTIC_TEST_SMB_PASSWORD",
		"RESTIC_TEST_SMB_REPOSITORY",
	}

	for _, v := range vars {
		if os.Getenv(v) == "" {
			t.Skipf("environment variable %v not set", v)
			return
		}
	}

	t.Logf("run tests")

	newTestSuite(t).RunTests(t)
}

func BenchmarkBackendSMB(t *testing.B) {
	if smbServer == "" {
		t.Skip("smb server binary not found")
	}
	vars := []string{
		"RESTIC_TEST_SMB_USERNAME",
		"RESTIC_TEST_SMB_PASSWORD",
		"RESTIC_TEST_SMB_REPOSITORY",
	}

	for _, v := range vars {
		if os.Getenv(v) == "" {
			t.Skipf("environment variable %v not set", v)
			return
		}
	}

	newTestSuite(t).RunBenchmarks(t)
}
