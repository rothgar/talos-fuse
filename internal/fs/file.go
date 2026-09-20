// Package fs - writeback file handle implementation.
//
// A resourceFile backed by a writable mount buffers writes in memory and
// flushes them back through ResourceRepository.UpdateResource when the
// file handle is released. The buffer can be resized via Setattr
// (FATTR_SIZE); the actual writeback happens on Flush (the close(2)
// flush cycle of the FUSE protocol).
package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/cosi-project/runtime/pkg/resource/protobuf"
	"github.com/cosi-project/runtime/pkg/state"

	"github.com/jgarr/talos-fuse/internal/resourceutil"
)

// fileHandle is the per-open-file state for a resourceFile. It buffers
// writes in memory until Flush is invoked.
type fileHandle struct {
	buf  bytes.Buffer
	mu   sync.Mutex
	file *resourceFile
}

// Write buffers the incoming write into the in-memory buffer. The
// returned size is the number of bytes written; on full-buffer success
// it equals len(data).
func (h *fileHandle) Write(_ context.Context, data []byte, _ int64) (uint32, syscall.Errno) {
	if h.file.opts.Maintenance {
		return 0, syscall.EROFS
	}
	if !h.file.opts.Writeable {
		return 0, syscall.EACCES
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, err := h.buf.Write(data); err != nil {
		return 0, syscall.EIO
	}

	return uint32(len(data)), 0
}

// Setattr implements FileSetattrer. Only FATTR_SIZE is honoured: it
// truncates (or extends) the buffered data. Mode/owner/time updates are
// ignored; the file's mode is determined by the mount writability.
func (h *fileHandle) Setattr(_ context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if h.file.opts.Maintenance {
		return syscall.EROFS
	}
	if !h.file.opts.Writeable {
		return syscall.EACCES
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if in.Valid&fuse.FATTR_SIZE != 0 {
		newSize := int(in.Size)
		if newSize < 0 {
			newSize = 0
		}

		current := h.buf.Bytes()
		switch {
		case newSize == len(current):
			// no-op
		case newSize < len(current):
			h.buf.Reset()
			h.buf.Write(current[:newSize]) //nolint:errcheck
		default:
			h.buf.Write(make([]byte, newSize-len(current))) //nolint:errcheck
		}
	}

	out.Size = uint64(h.buf.Len())
	return 0
}

// Flush implements FileFlusher. It validates writability, parses the
// buffered content into a *protobuf.Resource, validates the parsed
// metadata against the file path, and writes it back via
// Repository.UpdateResource. Errors are mapped to syscall.Errno values.
func (h *fileHandle) Flush(ctx context.Context) syscall.Errno {
	if h.file.opts.Maintenance {
		return syscall.EROFS
	}
	if !h.file.opts.Writeable {
		return syscall.EACCES
	}

	h.mu.Lock()
	data, err := io.ReadAll(bytes.NewReader(h.buf.Bytes()))
	h.mu.Unlock()
	if err != nil {
		return syscall.EIO
	}

	format := normalizeFormat(h.file.opts.Format)

	parsed, err := resourceutil.ParseResource(data, format)
	if err != nil {
		return mapFlushError(err)
	}

	if !metadataMatchesFile(parsed, h.file) {
		return syscall.EINVAL
	}

	if err := h.file.opts.Repository.UpdateResource(ctx, h.file.node, parsed); err != nil {
		return mapFlushError(err)
	}

	h.file.invalidateContent()

	return 0
}

// metadataMatchesFile reports whether the parsed resource's identifying
// metadata matches the path of the file it was written to.
func metadataMatchesFile(r *protobuf.Resource, f *resourceFile) bool {
	if r == nil {
		return false
	}

	md := r.Metadata()

	if string(md.Namespace()) != f.namespace {
		return false
	}

	if string(md.Type()) != f.rd.TypedSpec().Type {
		return false
	}

	if string(md.ID()) != f.id {
		return false
	}

	return true
}

// mapFlushError converts repository/parse errors to syscall.Errno values.
// Conflict and validation errors map to EINVAL; permission errors to EPERM;
// gRPC status errors are translated by grpcErrno.
func mapFlushError(err error) syscall.Errno {
	if err == nil {
		return 0
	}

	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EROFS), errors.Is(err, syscall.EPERM):
		return syscall.EPERM
	case errors.Is(err, syscall.EINVAL):
		return syscall.EINVAL
	}

	if state.IsConflictError(err) {
		return syscall.EINVAL
	}

	return grpcErrno(err)
}

// invalidateContent resets the cached read buffer for the file so that
// the next read fetches fresh content from the repository.
func (f *resourceFile) invalidateContent() {
	f.contentMu.Lock()
	f.content = nil
	f.contentErr = nil
	f.contentMu.Unlock()
}
