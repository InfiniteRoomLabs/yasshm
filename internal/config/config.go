// Package config loads and validates the gateway's one YAML file (spec 5.1).
//
// Decoding is strict: an unknown key, a duplicate key, a wrong type, a
// missing required key, an anchor or an alias is an error. Every problem in
// the file is reported in one pass, one line each, in the form
//
//	path.to.key: message (line N)
//
// A list item is written with its index and, when it has a valid id, the id
// in parentheses: tenants[0](default).servers[1](lab-1).host_key. A key of a
// mapping whose keys are ids is written as a path element:
// tenants[0](default).identities.lab-look.user. A key that is not a plain
// id is quoted. Problems about the file as a whole use the path "config".
// Problems are sorted by line, then path, then message.
//
// The loader reads private key files to check that they parse, and never
// logs, keeps or returns their content: an error names the path and the
// problem only. Nothing in a Config is key material.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// MaxTenants is how many tenants v0.1 accepts (spec D7). Tests that need
// more build the tenants in code.
const MaxTenants = 1

// MaxFileBytes is the largest config file Load reads.
const MaxFileBytes = 1 << 20

// ProviderCloudflareAccess is the only auth provider in v0.1.
const ProviderCloudflareAccess = "cloudflare-access"

// PlaceholderPrefix starts every value of deploy/config.example.yaml that the
// operator must replace; the loader refuses a string value that still starts
// with it ("replace the placeholder value").
const PlaceholderPrefix = "REPLACE_"

// DefaultSSHPort is the port of a server that does not name one.
const DefaultSSHPort = 22

// Config is a loaded, validated config.
type Config struct {
	// Version is the config format version; always 1.
	Version int
	// Gateway holds the gateway's own settings (the `server` mapping).
	Gateway ServerSettings
	// Auth holds the origin authentication settings.
	Auth Auth
	// Tenants holds exactly MaxTenants tenants.
	Tenants []tenant.Tenant
}

// ServerSettings is the `server` mapping: where the gateway listens and
// writes.
type ServerSettings struct {
	// Listen is a host:port; loopback unless AllowNonLoopback.
	Listen string
	// AllowNonLoopback permits a Listen address that is not loopback.
	AllowNonLoopback bool
	// DataDir is the absolute path of the database and output directory.
	DataDir string
	// AuditLog is the absolute path of the JSONL audit log.
	AuditLog string
}

// Auth is the `auth` mapping.
type Auth struct {
	// Provider is ProviderCloudflareAccess.
	Provider string
	// CloudflareAccess holds that provider's settings.
	CloudflareAccess CloudflareAccess
}

// CloudflareAccess holds the Access application's identifiers. They are not
// secrets, but `config check` does not print them.
type CloudflareAccess struct {
	// TeamDomain is the team's host name, for example
	// example-team.cloudflareaccess.com.
	TeamDomain string
	// Audience is the Access application's AUD tag.
	Audience string
	// CertsURL overrides the JWKS URL; empty means the team domain's.
	CertsURL string
}

// Tenant returns the tenant with the given id.
func (c *Config) Tenant(id tenant.TenantID) (*tenant.Tenant, bool) {
	for i := range c.Tenants {
		if c.Tenants[i].ID == id {
			return &c.Tenants[i], true
		}
	}
	return nil, false
}

// Server returns the server with the given id in the given tenant. A server
// of any other tenant is never returned.
func (c *Config) Server(t tenant.TenantID, id tenant.ServerID) (*tenant.Server, bool) {
	tn, ok := c.Tenant(t)
	if !ok {
		return nil, false
	}
	return tn.Server(id)
}

// Problem is one thing wrong with a config file.
type Problem struct {
	// Path locates the key, for example tenants[0](default).servers[0](lab-1).port.
	Path string
	// Line is the 1-based line in the file, or 0 when unknown.
	Line int
	// Message says what is wrong. It never holds key material.
	Message string
}

// String renders the problem as one line: "path: message (line N)".
func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("%s: %s (line %d)", p.Path, p.Message, p.Line)
	}
	return p.Path + ": " + p.Message
}

// Error carries every problem found in a config file.
type Error struct {
	// Problems is sorted by line, then path, then message.
	Problems []Problem
}

// Error renders one problem per line.
func (e *Error) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}

// newError sorts and de-duplicates problems; it returns nil when there are
// none.
func newError(probs []Problem) error {
	if len(probs) == 0 {
		return nil
	}
	sort.SliceStable(probs, func(i, j int) bool {
		a, b := probs[i], probs[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Message < b.Message
	})
	out := probs[:1]
	for _, p := range probs[1:] {
		if p != out[len(out)-1] {
			out = append(out, p)
		}
	}
	return &Error{Problems: out}
}

// Load reads, decodes and validates the config file at path, including the
// checks that need the file system (private key files and known_hosts
// files). It returns either a valid config or an *Error with every problem.
// It never connects anywhere and never writes.
func Load(path string) (*Config, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, &Error{Problems: []Problem{{Path: "config", Message: err.Error()}}}
	}
	cfg, refs, probs := decode(data)
	probs = append(probs, checkFiles(refs)...)
	if err := newError(probs); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Parse decodes and validates a config from memory without touching the
// file system: private key and known_hosts files are not checked. Use Load
// for a real config.
func Parse(data []byte) (*Config, error) {
	if len(data) > MaxFileBytes {
		return nil, &Error{Problems: []Problem{{Path: "config", Message: fmt.Sprintf("the file is larger than %d bytes", MaxFileBytes)}}}
	}
	cfg, _, probs := decode(data)
	if err := newError(probs); err != nil {
		return nil, err
	}
	return cfg, nil
}

func readFile(path string) ([]byte, error) {
	// Opening the path the operator passed with --config is the point of
	// this function.
	f, err := os.Open(path) //nolint:gosec // G304: the operator names the config file on the command line
	if err != nil {
		return nil, fmt.Errorf("cannot read the file: %w", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot read the file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("is not a regular file")
	}
	// Whoever can write the config decides what the gateway trusts; the
	// mode is read from the open file, so the check holds for root too.
	if perm := fi.Mode().Perm(); perm&0o022 != 0 {
		return nil, fmt.Errorf("is writable by group or other (mode %04o); it must not be", perm)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read the file: %w", err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("the file is larger than %d bytes", MaxFileBytes)
	}
	return data, nil
}
