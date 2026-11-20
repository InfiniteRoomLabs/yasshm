package mcpserver

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/policy"
)

// downloadArgs is download_file's input.
type downloadArgs struct {
	Server   string `json:"server" jsonschema:"the server id"`
	Path     string `json:"path" jsonschema:"the absolute path of a regular file"`
	Offset   *int64 `json:"offset,omitempty" jsonschema:"where to start reading (default 0)"`
	MaxBytes *int64 `json:"max_bytes,omitempty" jsonschema:"the most bytes to read; clamped so the result fits max_result_bytes"`
}

// uploadArgs is upload_file's input.
type uploadArgs struct {
	Server        string  `json:"server" jsonschema:"the server id"`
	Path          string  `json:"path" jsonschema:"the absolute path to write"`
	Content       *string `json:"content,omitempty" jsonschema:"the content as text (give this or content_base64)"`
	ContentBase64 *string `json:"content_base64,omitempty" jsonschema:"the content in standard base64 (give this or content)"`
	Mode          string  `json:"mode,omitempty" jsonschema:"octal permission bits, at most 0777 (default 0644)"`
	Overwrite     *bool   `json:"overwrite,omitempty" jsonschema:"replace an existing file (default false)"`
}

// DownloadResult is download_file's structured content.
type DownloadResult struct {
	Server string `json:"server"`
	// Path is the cleaned path that was read.
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	// Size is the file's size when it was opened.
	Size int64 `json:"size"`
	// Encoding is "text" (Data) or "base64" (DataBase64).
	Encoding   string `json:"encoding"`
	Data       string `json:"data,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
	NextOffset int64  `json:"next_offset"`
	EOF        bool   `json:"eof"`
}

// UploadResult is upload_file's structured content.
type UploadResult struct {
	Server       string `json:"server"`
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytes_written"`
}

// defaultMode is upload_file's mode when none is given.
const defaultMode = "0644"

func prepareDownload(_ context.Context, g *Gateway, c *call, raw json.RawMessage) plan {
	var a downloadArgs
	if err := decodeArgs(raw, []string{"server", "path", "offset", "max_bytes"}, maxArgBytes, &a); err != nil {
		return plan{err: err}
	}
	p := plan{
		server: auditServer(a.Server), policy: &policy.Args{Path: a.Path},
		args: audit.ToolArgs{Path: auditText(a.Path, auditPathBytes), Offset: a.Offset, MaxBytes: a.MaxBytes},
		err:  cmp.Or(checkServerID(a.Server, true), checkNonNegative(a.Offset, "offset"), checkNonNegative(a.MaxBytes, "max_bytes")),
	}
	if p.err != nil {
		return p
	}
	c.resolveServer(a.Server)
	p.act = func(ctx context.Context) (result, error) {
		var off int64
		if a.Offset != nil {
			off = *a.Offset
		}
		return g.download(ctx, c, policy.CleanPath(a.Path), off, c.clampBytes(a.MaxBytes))
	}
	return p
}

// transferContext bounds an SFTP call: the SSH layer bounds it only by the
// caller's context.
func (g *Gateway) transferContext(ctx context.Context, c *call) (context.Context, context.CancelFunc) {
	return g.bounded(ctx, time.Duration(c.t.Limits.ConnectTimeoutSeconds)*time.Second+transferExtra)
}

// bounded is ctx with a timeout of d that also ends when the gateway's
// shutdown grace has passed (Shutdown cancels the base context).
func (g *Gateway) bounded(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, d)
	stop := context.AfterFunc(g.base, cancel)
	return ctx, func() { stop(); cancel() }
}

// download writes file.downloaded (requested) before the read, refusing if
// it cannot, reads at most n bytes and writes the done form after.
func (g *Gateway) download(ctx context.Context, c *call, path string, off, n int64) (result, error) {
	if err := g.audit.Write(audit.FileDownloadRequested(c.actor, c.srv.ID, path, off, n)); err != nil {
		return result{}, errAudit
	}
	tctx, cancel := g.transferContext(ctx, c)
	defer cancel()
	data, size, eof, err := g.ssh[c.t.ID].Download(tctx, c.t.ID, c.srv, path, off, n)
	if err != nil {
		return result{}, g.fileError(err)
	}
	text := isText(data)
	used := 0
	res, err := c.fit(func(payload int) (result, error) {
		r := DownloadResult{Server: string(c.srv.ID), Path: path, Offset: off, Size: size, Encoding: "text"}
		if text {
			// Valid UTF-8 as it stands: fitText cuts on a character
			// boundary and replaces nothing.
			t := fitText(data, payload, true)
			r.Data, used = t.text, t.used
		} else {
			r.Encoding = "base64"
			r.DataBase64, used = fitBase64(data, payload-2)
		}
		r.NextOffset, r.EOF = off+int64(used), eof && used == len(data)
		head := fmt.Sprintf("%s:%s bytes %d..%d of %d (%s)", r.Server, r.Path, off, r.NextOffset, size, r.Encoding)
		if r.EOF {
			head += ", eof"
		}
		return result{structured: r, blocks: []string{head, r.Data + r.DataBase64}}, nil
	})
	if err != nil {
		return result{}, err
	}
	if werr := g.audit.Write(audit.FileDownloaded(c.actor, c.srv.ID, path, off, int64(used))); werr != nil {
		g.log.Error("audit write failed after a download", "request_id", c.actor.RequestID, "event", audit.EventFileDownloaded)
	}
	return res, nil
}

// isText reports whether data is shown as text: valid UTF-8 as it stands
// (a chunk that splits a character is base64) with no NUL byte. File data is
// never altered: no replacement character, ever.
func isText(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0 && utf8.Valid(data)
}

func prepareUpload(_ context.Context, g *Gateway, c *call, raw json.RawMessage) plan {
	var a uploadArgs
	if err := decodeArgs(raw, []string{"server", "path", "content", "content_base64", "mode", "overwrite"}, MaxBodyBytes, &a); err != nil {
		return plan{err: err}
	}
	if a.Mode == "" {
		a.Mode = defaultMode
	}
	p := plan{
		server: auditServer(a.Server), policy: &policy.Args{Path: a.Path},
		args: audit.ToolArgs{Path: auditText(a.Path, auditPathBytes), Mode: auditEnum(a.Mode, modePattern.MatchString(a.Mode)), Overwrite: a.Overwrite},
	}
	var data []byte
	switch {
	case checkServerID(a.Server, true) != nil:
		p.err = checkServerID(a.Server, true)
	case (a.Content == nil) == (a.ContentBase64 == nil):
		p.err = argError("give exactly one of content and content_base64")
	case !modePattern.MatchString(a.Mode):
		p.err = argError("mode must be octal permission bits, at most 0777")
	case a.Content != nil:
		data = []byte(*a.Content)
	default:
		b, err := base64.StdEncoding.Strict().DecodeString(*a.ContentBase64)
		if err != nil {
			p.err = argError("content_base64 is not standard base64")
		}
		data = b
	}
	size := int64(len(data))
	p.args.ContentBytes = &size
	if p.err == nil && size > c.t.Limits.MaxUploadBytes {
		p.err = argError("content is larger than the tenant's max_upload_bytes")
	}
	if p.err != nil {
		return p
	}
	c.resolveServer(a.Server)
	p.act = func(ctx context.Context) (result, error) {
		mode, err := strconv.ParseUint(a.Mode, 8, 32)
		if err != nil {
			return result{}, argError("mode must be octal permission bits, at most 0777")
		}
		overwrite := a.Overwrite != nil && *a.Overwrite
		return g.upload(ctx, c, policy.CleanPath(a.Path), data, a.Mode, fs.FileMode(mode), overwrite)
	}
	return p
}

// upload writes file.uploaded (requested) before the write, refusing if it
// cannot, writes the file and writes the done form after.
func (g *Gateway) upload(ctx context.Context, c *call, path string, data []byte, modeText string, mode fs.FileMode, overwrite bool) (result, error) {
	if err := g.audit.Write(audit.FileUploadRequested(c.actor, c.srv.ID, path, int64(len(data)), modeText, overwrite)); err != nil {
		return result{}, errAudit
	}
	tctx, cancel := g.transferContext(ctx, c)
	defer cancel()
	if err := g.ssh[c.t.ID].Upload(tctx, c.t.ID, c.srv, path, data, mode, overwrite); err != nil {
		return result{}, g.fileError(err)
	}
	if werr := g.audit.Write(audit.FileUploaded(c.actor, c.srv.ID, path, int64(len(data)))); werr != nil {
		g.log.Error("audit write failed after an upload", "request_id", c.actor.RequestID, "event", audit.EventFileUploaded)
	}
	r := UploadResult{Server: string(c.srv.ID), Path: path, BytesWritten: int64(len(data))}
	return oneBlock(r, fmt.Sprintf("%s:%s: %d bytes written\n", r.Server, r.Path, r.BytesWritten)), nil
}
