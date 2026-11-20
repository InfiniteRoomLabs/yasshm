package jobs

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/store"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// ReadRequest is one page request for ReadOutput.
type ReadRequest struct {
	// Stream is "stdout" or "stderr".
	Stream string
	// Offset is where to read: >= 0 from the start, < 0 from the end
	// (clamped at the start).
	Offset int64
	// MaxBytes is the most bytes to return; 0 or more than the tenant's
	// max_result_bytes means max_result_bytes.
	MaxBytes int64
	// Wait, when positive, blocks until there are bytes past the offset,
	// the command finishes, Wait passes (at most the tenant's
	// max_wait_seconds) or the context ends.
	Wait time.Duration
}

// Output is one page of a command's output.
type Output struct {
	// Data is the bytes read (target output: untrusted, maybe not UTF-8).
	Data []byte
	// Start is the offset Data begins at; Next is the offset after it.
	Start, Next int64
	// Size is the stream's stored size when it was read.
	Size int64
	// EOF is true when the command has finished and Next equals Size.
	EOF bool
	// Record is the command's record after the read.
	Record Record
}

// ReadOutput reads one page of the tenant's command id's stored output. With
// merge_stderr the stderr stream is empty (its bytes are in stdout) and its
// EOF follows the command. A stream other than stdout or stderr is
// invalid_argument; a command of another tenant is not_found.
func (e *Engine) ReadOutput(ctx context.Context, tid tenant.TenantID, id tenant.CommandID, req ReadRequest) (Output, error) {
	ts, err := e.tenant(tid)
	if err != nil {
		return Output{}, err
	}
	idx, ok := streamIndex(req.Stream)
	if !ok {
		return Output{}, invalid("stream must be stdout or stderr")
	}
	// The live job is looked up once, before the record: its done channel
	// says when it has finished, after its record is final and its files
	// are closed; a record still running without a live job belongs to no
	// engine here.
	j := e.live(tid, id)
	r, err := e.Get(ctx, tid, id)
	if err != nil {
		return Output{}, err
	}
	req.Wait = min(req.Wait, time.Duration(ts.t.Limits.MaxWaitSeconds)*time.Second)
	limit := ts.t.Limits.MaxResultBytes
	if req.MaxBytes > 0 && req.MaxBytes < limit {
		limit = req.MaxBytes
	}
	dir, err := commandDir(e.cfg.DataDir, tid, id)
	if err != nil {
		return Output{}, err
	}
	path := filepath.Join(dir, req.Stream)
	empty := r.MergeStderr && idx == errIdx

	var expired <-chan time.Time
	if req.Wait > 0 {
		t := time.NewTimer(req.Wait)
		defer t.Stop()
		expired = t.C
	}
	stop := req.Wait <= 0
	for {
		finished := j == nil && r.Status != StatusRunning
		if j != nil {
			select {
			case <-j.done:
				finished = true
			default:
			}
		}
		var size int64
		var changed <-chan struct{}
		var done <-chan struct{}
		switch {
		case empty:
		case j != nil && !finished:
			snap := j.cap.snapshot()
			size, changed = snap.sizes[idx], snap.changed
		default:
			if size, err = fileSize(path); err != nil {
				return Output{}, internal("could not read the output", err)
			}
		}
		if j != nil {
			done = j.done
		}
		start, next := page(req.Offset, size, limit)
		if next > start || finished || stop || (j == nil && !finished) {
			data, err := readFileRange(path, start, next)
			if err != nil {
				return Output{}, internal("could not read the output", err)
			}
			next = start + int64(len(data))
			rec, err := e.Get(context.WithoutCancel(ctx), tid, id)
			if err != nil {
				return Output{}, err
			}
			return Output{Data: data, Start: start, Next: next, Size: size, EOF: finished && next == size, Record: rec}, nil
		}
		select {
		case <-changed:
		case <-done:
		case <-expired:
			stop = true
		case <-ctx.Done():
			stop = true
		}
	}
}

// ListFilter selects records for List.
type ListFilter struct {
	// Server and Status keep only matching records when set.
	Server tenant.ServerID
	Status Status
	// Limit is the page size: 0 means 20, at most 100.
	Limit int
	// Cursor continues a previous listing (its NextCursor).
	Cursor string
}

// DefaultListLimit is List's page size when the filter gives none.
const DefaultListLimit = 20

// ListPage is one page of List.
type ListPage struct {
	// Records are newest first.
	Records []Record
	// NextCursor continues the listing; empty at the end.
	NextCursor string
}

// cursorPrefix versions the cursor format.
const cursorPrefix = "c1."

// encodeCursor makes the opaque cursor for the position after r: its
// creation time and id, base64url without padding.
func encodeCursor(r *Record) string {
	raw := strconv.FormatInt(r.CreatedAt.UnixMicro(), 10) + "." + string(r.ID)
	return cursorPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor validates and decodes a cursor. A cursor carries only a
// position (a time and an id); List applies it inside the caller's tenant,
// so a cursor from another tenant reveals nothing of it.
func decodeCursor(c string) (*store.Position, error) {
	bad := invalid("malformed cursor")
	rest, ok := strings.CutPrefix(c, cursorPrefix)
	if !ok || len(rest) > 128 {
		return nil, bad
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(rest)
	if err != nil {
		return nil, bad
	}
	ts, id, ok := strings.Cut(string(raw), ".")
	if !ok || !store.ValidCommandID(tenant.CommandID(id)) {
		return nil, bad
	}
	us, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || us <= 0 || strconv.FormatInt(us, 10) != ts {
		return nil, bad
	}
	return &store.Position{CreatedAt: time.UnixMicro(us).UTC(), ID: tenant.CommandID(id)}, nil
}

// List returns the tenant's records newest first, filtered by server and
// status, one page at a time.
func (e *Engine) List(ctx context.Context, tid tenant.TenantID, f ListFilter) (ListPage, error) {
	if _, err := e.tenant(tid); err != nil {
		return ListPage{}, err
	}
	limit := f.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 0 || limit > store.MaxListLimit {
		return ListPage{}, invalid("limit must be between 1 and 100")
	}
	if f.Server != "" && !store.ValidServerID(f.Server) {
		return ListPage{}, invalid("malformed server id")
	}
	if f.Status != "" && !f.Status.Valid() {
		return ListPage{}, invalid("unknown status")
	}
	sf := store.Filter{Server: f.Server, Status: f.Status, Limit: limit}
	if f.Cursor != "" {
		pos, err := decodeCursor(f.Cursor)
		if err != nil {
			return ListPage{}, err
		}
		sf.After = pos
	}
	if err := e.usable(); err != nil {
		return ListPage{}, err
	}
	recs, more, err := e.st.List(ctx, tid, sf)
	if errors.Is(err, store.ErrInvalid) {
		return ListPage{}, invalid("invalid filter")
	}
	if err != nil {
		return ListPage{}, internal("could not list the commands", err)
	}
	for i := range recs {
		e.overlay(&recs[i])
	}
	p := ListPage{Records: recs}
	if more && len(recs) > 0 {
		p.NextCursor = encodeCursor(&recs[len(recs)-1])
	}
	return p, nil
}
