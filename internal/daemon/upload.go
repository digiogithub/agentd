package daemon

// Op upload: store an image for a session under <state>/uploads/<session>/ and
// return its path. The client then puts the path in its prompt; the daemon
// types nothing. File contents are never logged.

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/proto"
	"github.com/digiogithub/agentd/internal/session"
)

// UploadRetention is how long uploaded files are kept.
const UploadRetention = 7 * 24 * time.Hour

// uploadSweepEvery throttles the retention sweep run by upload calls.
const uploadSweepEvery = time.Hour

// uploads serializes file creation and remembers the last sweep.
type uploads struct {
	mu        sync.Mutex
	lastSweep time.Time
}

// uploadsDir returns the root directory of uploads.
func (s *Server) uploadsDir() (string, error) {
	if s.UploadsDir != "" {
		return s.UploadsDir, nil
	}
	return config.UploadsDir()
}

// uploadOp serves op upload: humans for any session, a session only for itself
// unless [security] agent_type (same rule as type and act). Relay devices are
// filtered by relayGate (operate scope, session globs).
func (s *Server) uploadOp(who caller, a proto.UploadArgs) (proto.UploadResult, error) {
	if a.Name == "" {
		return proto.UploadResult{}, proto.Errorf(proto.CodeInvalid, "upload: missing session name")
	}
	if who.session && who.id != a.Name && !s.Cfg.Security.AgentType {
		return proto.UploadResult{}, proto.Errorf(proto.CodeForbidden, "upload: session %q may not upload for session %q "+
			"(set [security] agent_type = true to allow)", who.id, a.Name)
	}
	ext := a.Ext
	if !slices.Contains(proto.UploadExts, ext) {
		return proto.UploadResult{}, proto.Errorf(proto.CodeInvalid, "upload: ext must be one of %s", strings.Join(proto.UploadExts, ", "))
	}
	if base64.StdEncoding.DecodedLen(len(a.Data)) > proto.MaxUploadBytes+3 {
		return proto.UploadResult{}, proto.Errorf(proto.CodeInvalid, "upload: file larger than %d bytes", proto.MaxUploadBytes)
	}
	data, err := base64.StdEncoding.DecodeString(a.Data)
	if err != nil {
		return proto.UploadResult{}, proto.Errorf(proto.CodeInvalid, "upload: data is not valid base64")
	}
	if len(data) == 0 || len(data) > proto.MaxUploadBytes {
		return proto.UploadResult{}, proto.Errorf(proto.CodeInvalid, "upload: file must be 1..%d bytes", proto.MaxUploadBytes)
	}
	if !imageMatches(ext, data) {
		return proto.UploadResult{}, proto.Errorf(proto.CodeInvalid, "upload: content is not a %s image", ext)
	}
	e, err := s.lookup(a.Name)
	if err != nil {
		return proto.UploadResult{}, err
	}
	if e.sess.State() == session.StateExited {
		return proto.UploadResult{}, proto.Errorf(proto.CodeExited, "session %q has exited", a.Name)
	}
	if a.Name == "." || a.Name == ".." || strings.ContainsAny(a.Name, `/\`) {
		return proto.UploadResult{}, proto.Errorf(proto.CodeInvalid, "upload: session name %q cannot be a directory name", a.Name)
	}
	root, err := s.uploadsDir()
	if err != nil {
		return proto.UploadResult{}, err
	}
	now := s.clock().Now()
	s.uploads.mu.Lock()
	defer s.uploads.mu.Unlock()
	if now.Sub(s.uploads.lastSweep) >= uploadSweepEvery {
		s.uploads.lastSweep = now
		s.sweepUploadsLocked(root, now)
	}
	path, err := writeUpload(root, a.Name, ext, data, now)
	if err != nil {
		s.Log.Error("upload failed", "session", a.Name, "ext", ext, "size", len(data), "err", err)
		return proto.UploadResult{}, proto.Errorf(proto.CodeInternal, "upload: could not store the file")
	}
	s.Log.Info("upload stored", "session", a.Name, "ext", ext, "size", len(data), "caller", who.id)
	return proto.UploadResult{Path: path, Size: len(data)}, nil
}

// imageMatches reports whether data starts with the signature of ext.
func imageMatches(ext string, data []byte) bool {
	switch ext {
	case "png":
		return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	case "jpg", "jpeg":
		return bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF})
	case "gif":
		return bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))
	case "webp":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	}
	return false
}

// ensurePrivateDir creates dir (0700) or checks that an existing one is a real
// directory (not a symlink) and makes it 0700.
func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return os.Chmod(dir, 0o700)
}

// writeUpload stores data as <root>/<name>/YYYYMMDD-HHMMSS-<n>.<ext> (0600,
// never overwriting) and returns the absolute path.
func writeUpload(root, name, ext string, data []byte, now time.Time) (string, error) {
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return "", err
	}
	if err := ensurePrivateDir(root); err != nil {
		return "", err
	}
	dir := filepath.Join(root, name)
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	stamp := now.Format("20060102-150405")
	for n := 1; n <= 1000; n++ {
		p := filepath.Join(dir, fmt.Sprintf("%s-%d.%s", stamp, n, ext))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(p)
			return "", errors.Join(werr, cerr)
		}
		return filepath.Abs(p)
	}
	return "", fmt.Errorf("no free file name in %s", dir)
}

// sweepUploads removes uploads older than UploadRetention (daemon start).
func (s *Server) sweepUploads() {
	root, err := s.uploadsDir()
	if err != nil {
		return
	}
	now := s.clock().Now()
	s.uploads.mu.Lock()
	defer s.uploads.mu.Unlock()
	s.uploads.lastSweep = now
	s.sweepUploadsLocked(root, now)
}

// sweepUploadsLocked removes regular files older than UploadRetention from the
// session directories under root, then the directories left empty.
func (s *Server) sweepUploadsLocked(root string, now time.Time) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	removed := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		left := len(files)
		for _, f := range files {
			fi, err := f.Info()
			if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < UploadRetention {
				continue
			}
			if os.Remove(filepath.Join(dir, f.Name())) == nil {
				removed++
				left--
			}
		}
		if left == 0 {
			_ = os.Remove(dir)
		}
	}
	if removed > 0 {
		s.Log.Info("old uploads removed", "count", removed)
	}
}

// removeUploads deletes the uploads of a removed session.
func (s *Server) removeUploads(name string) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return
	}
	root, err := s.uploadsDir()
	if err != nil {
		return
	}
	s.uploads.mu.Lock()
	defer s.uploads.mu.Unlock()
	dir := filepath.Join(root, name)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		s.Log.Warn("remove uploads", "session", name, "err", err)
	}
}
