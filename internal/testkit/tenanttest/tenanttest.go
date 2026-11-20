// Package tenanttest builds the tenant values the job engine and MCP
// endpoint tests share: the limits they run with, SSH options with short
// timings, and a tenant.Server that points at an sshtest server. It is test
// support and is imported only by tests.
package tenanttest

import (
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// Limits returns the limits the in-process suites run with: small waits
// and timeouts, a one-second kill grace.
func Limits() tenant.Limits {
	return tenant.Limits{
		DefaultWaitSeconds: 5, MaxWaitSeconds: 10, DefaultTimeoutSeconds: 30, MaxTimeoutSeconds: 60,
		MaxConcurrentCommands: 4, MaxConcurrentPerServer: 2, MaxCommandBytes: 8192, MaxOutputBytes: 1 << 20,
		MaxResultBytes: 100000, RetentionDays: 7, MaxDataBytes: 1 << 30, MaxUploadBytes: 1 << 20,
		KillGraceSeconds: 1, ConnectTimeoutSeconds: 5, ShutdownGraceSeconds: 10,
	}
}

// SSHOptions has short timings so the suites stay fast; its signature is
// the engine's and the gateway's SSHOptions hook.
func SSHOptions(tenant.Limits) sshclient.Options {
	return sshclient.Options{ConnectTimeout: 5 * time.Second, KillGrace: 300 * time.Millisecond, KillWait: time.Second}
}

// Server describes s as server sid of tenant tid, at access level look,
// with the description "test server <sid>" and the tag "test".
func Server(tid tenant.TenantID, sid tenant.ServerID, s *sshtest.Server) tenant.Server {
	return tenant.Server{
		Tenant: tid, ID: sid, Host: s.Host, Port: s.Port, Access: tenant.AccessLook, HostKey: s.HostKeyLine(),
		Description: "test server " + string(sid), Tags: []string{"test"},
		Identity: tenant.Identity{ID: "look", User: s.User, PrivateKeyFile: s.ClientKeyFile},
	}
}
