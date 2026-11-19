package config

import "github.com/InfiniteRoomLabs/yasshm/internal/tenant"

// limitSpec is one row of the limits table: the key under a tenant's
// `limits`, its default, its allowed range (inclusive) and where it lives in
// tenant.Limits.
type limitSpec struct {
	key      string
	def      int64
	min, max int64
	field    func(*tenant.Limits) *int64
}

const (
	kib = 1024
	mib = 1024 * kib
	gib = 1024 * mib
)

// limitTable is the one table of limits (spec 5.1 and its STATE AS OF
// 2026-11-19 callout, which repeats it). The defaults are the spec's; the
// ranges are this package's choice, and the reasons are on each row.
var limitTable = []limitSpec{
	// 0 means "return at once with status running". Bounded by max_wait_seconds.
	{"default_wait_seconds", 30, 0, 290, func(l *tenant.Limits) *int64 { return &l.DefaultWaitSeconds }},
	// Below the client's 300 second tool-call limit (F3). The edge may cut a
	// silent request at about 100 seconds (F15), which is why the default is 60.
	{"max_wait_seconds", 60, 1, 290, func(l *tenant.Limits) *int64 { return &l.MaxWaitSeconds }},
	// One day at most: a longer job is not what a tool call is for.
	{"default_timeout_seconds", 600, 1, 86400, func(l *tenant.Limits) *int64 { return &l.DefaultTimeoutSeconds }},
	{"max_timeout_seconds", 3600, 1, 86400, func(l *tenant.Limits) *int64 { return &l.MaxTimeoutSeconds }},
	// One connection per command (D13): keep the fan-out modest.
	{"max_concurrent_commands", 4, 1, 64, func(l *tenant.Limits) *int64 { return &l.MaxConcurrentCommands }},
	{"max_concurrent_per_server", 2, 1, 64, func(l *tenant.Limits) *int64 { return &l.MaxConcurrentPerServer }},
	// 64 KiB is far above any command a person types and below ARG_MAX.
	{"max_command_bytes", 8192, 1, 64 * kib, func(l *tenant.Limits) *int64 { return &l.MaxCommandBytes }},
	{"max_output_bytes", 16 * mib, 1 * kib, 1 * gib, func(l *tenant.Limits) *int64 { return &l.MaxOutputBytes }},
	// The client caps a tool result at about 150,000 characters (F3); leave
	// room for the structured fields around the output.
	{"max_result_bytes", 100000, 1 * kib, 140000, func(l *tenant.Limits) *int64 { return &l.MaxResultBytes }},
	{"retention_days", 7, 1, 3650, func(l *tenant.Limits) *int64 { return &l.RetentionDays }},
	{"max_data_bytes", 1 * gib, 1 * mib, 1024 * gib, func(l *tenant.Limits) *int64 { return &l.MaxDataBytes }},
	// The request body is capped at 2 MiB (spec 5.8) and base64 grows data by
	// a third: 1.5 MiB would be exactly 2 MiB encoded, with no room for the
	// JSON around it, so 1 MiB is the most that fits.
	{"max_upload_bytes", 1 * mib, 1, 1 * mib, func(l *tenant.Limits) *int64 { return &l.MaxUploadBytes }},
	{"kill_grace_seconds", 5, 1, 60, func(l *tenant.Limits) *int64 { return &l.KillGraceSeconds }},
	{"connect_timeout_seconds", 10, 1, 120, func(l *tenant.Limits) *int64 { return &l.ConnectTimeoutSeconds }},
	{"shutdown_grace_seconds", 20, 1, 300, func(l *tenant.Limits) *int64 { return &l.ShutdownGraceSeconds }},
}

// DefaultLimits returns the limits a tenant gets when its `limits` mapping
// is empty or absent.
func DefaultLimits() tenant.Limits {
	var l tenant.Limits
	for _, s := range limitTable {
		*s.field(&l) = s.def
	}
	return l
}

// limitPair is a cross-field rule: the limit named lo must not exceed the
// limit named hi.
type limitPair struct{ lo, hi string }

var limitPairs = []limitPair{
	{"default_wait_seconds", "max_wait_seconds"},
	{"default_timeout_seconds", "max_timeout_seconds"},
	{"max_concurrent_per_server", "max_concurrent_commands"},
	{"max_output_bytes", "max_data_bytes"},
}

func limitByKey(key string) limitSpec {
	for _, s := range limitTable {
		if s.key == key {
			return s
		}
	}
	panic("config: no limit named " + key)
}
