package sshclient

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// ProbeCommand is the fixed, read-only command Probe runs (spec 5.5). It is
// a constant: no caller-supplied text reaches the remote shell. It is
// wrapped in sh -c with one single-quoted script, which reads the same in
// sh, bash, zsh and fish, so the target's login shell does not matter.
const ProbeCommand = `sh -c 'echo "kernel=$(uname -sr)"; echo "arch=$(uname -m)"; echo "os=$(sed -n "s/^PRETTY_NAME=//p" /etc/os-release 2>/dev/null)"; echo "uptime=$(cut -d" " -f1 /proc/uptime 2>/dev/null)"; echo "user=$(id -un)"'`

// maxProbeOutput bounds what Probe keeps of the output; a hostile target
// cannot make it hold more.
const maxProbeOutput = 16 << 10

// maxFactBytes bounds one fact's value.
const maxFactBytes = 256

// Facts are what Probe learned about a server. Every value comes from the
// target and is untrusted text.
type Facts struct {
	// Kernel is "uname -sr", e.g. "Linux 6.8.0".
	Kernel string
	// Arch is "uname -m", e.g. "x86_64".
	Arch string
	// OS is PRETTY_NAME from /etc/os-release, without quotes; "" if absent.
	OS string
	// Uptime is from /proc/uptime; zero if absent.
	Uptime time.Duration
	// User is the login name the identity runs as.
	User string
}

// limitedBuffer keeps the first max bytes written to it and discards the
// rest, so a writer never blocks or grows without bound.
type limitedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// Probe runs ProbeCommand on srv and parses its output. ctx bounds the
// whole probe, connect included; when it ends the probe is stopped like any
// command. A probe that does not exit 0 is CodeInternal.
func (c *Client) Probe(ctx context.Context, tid tenant.TenantID, srv *tenant.Server) (Facts, error) {
	out := &limitedBuffer{max: maxProbeOutput}
	st, err := c.Exec(ctx, tid, srv, ProbeCommand, out, nil)
	if err != nil {
		return Facts{}, err
	}
	if st.Outcome != OutcomeExited || st.Code != 0 {
		return Facts{}, newErr(CodeInternal, srv.ID, "probe", "the probe command did not exit 0 ("+st.Outcome.String()+")", nil, nil)
	}
	return parseFacts(out.String()), nil
}

// parseFacts reads key=value lines; unknown keys are ignored, values are
// cut to maxFactBytes and made valid UTF-8.
func parseFacts(out string) Facts {
	var f Facts
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok {
			continue
		}
		v = clean(v)
		switch k {
		case "kernel":
			f.Kernel = v
		case "arch":
			f.Arch = v
		case "os":
			f.OS = strings.Trim(v, `"'`)
		case "uptime":
			if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 && s < 9e9 {
				f.Uptime = time.Duration(s * float64(time.Second))
			}
		case "user":
			f.User = v
		}
	}
	return f
}

// clean trims space, cuts to maxFactBytes on a rune boundary and replaces
// invalid UTF-8 and control characters.
func clean(v string) string {
	v = strings.ToValidUTF8(strings.TrimSpace(v), "�")
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	for len(v) > maxFactBytes {
		_, size := utf8.DecodeLastRuneInString(v)
		v = v[:len(v)-size]
	}
	return v
}
