# Op `upload`: images for a session over the relay

Builds on [[relay-transport]] and [[agentd/plans/relay_plan]]; requested by agentd-app for image
attachments in the mobile chat (2026-11-27), where relay hosts have no shell to copy files with.

## What changed
- New op `upload {name, ext, data}` → `{path, size}`: png/jpg/jpeg/webp/gif, standard base64, decoded
  1..6,000,000 bytes (`proto.MaxUploadBytes`; 6 MiB would not fit in the 8 MiB wire line once base64
  encoded), content signature must match `ext`. Stored as
  `<state>/uploads/<session>/YYYYMMDD-HHMMSS-<n>.<ext>` (dirs 0700, never symlinks; file 0600, `O_EXCL`).
- Permissions: humans any session; a session only itself unless `[security] agent_type`; relay devices
  `operate`+ within their session globs, audited.
- Retention: files older than 7 days removed at daemon start and by an upload at most hourly; `rm`
  removes the session's directory. Logs: session, ext, size, caller; never content.
- `info.features` (new, omitempty) lists `upload`, so clients detect support.
- `client.Upload`.

## Files & symbols
- `internal/proto/upload.go` (`OpUpload`, `FeatureUpload`, `MaxUploadBytes`, `UploadExts`, `UploadArgs`, `UploadResult`),
  `internal/proto/proto.go` (`InfoResult.Features`)
- `internal/daemon/upload.go` (`uploadOp`, `imageMatches`, `writeUpload`, `sweepUploads`, `removeUploads`),
  `handlers.go` (dispatch, `rm`), `info.go`, `relaygate.go` (operate scope, audited), `server.go`
  (`Server.UploadsDir`, sweep at `Serve`)
- `internal/config/paths.go` (`UploadsDir`), `internal/client/client.go` (`Upload`)
- AGENTS.md ("Uploads", paths, ops, relay gate), README.md (Relay section)

## Verification
- `internal/daemon/upload_test.go`, `upload_internal_test.go`, `internal/proto` `TestMaxUploadFitsInALine`.
- gofmt, `go vet ./...`, `go test ./...`, `go test -race ./internal/daemon ./internal/session ./internal/delivery`
  all pass (run outside an agentd session: `TestHCListenerLifecycleAndIdentity` fails when the test process
  inherits `AGENTD_TOKEN`, unrelated). `go list -deps ./cmd/agentd | grep -c mygo` = 0.
