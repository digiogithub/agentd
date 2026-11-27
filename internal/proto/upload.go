package proto

// Op upload: a client stores an image on the daemon's host for a session (the
// mobile app over agentd-relay has no shell to copy it with) and then puts the
// returned path in its act prompt. The daemon never types anything itself.

const OpUpload = "upload" // UploadArgs → UploadResult

// FeatureUpload is listed in InfoResult.Features by daemons that serve op upload.
const FeatureUpload = "upload"

// MaxUploadBytes bounds the decoded file of one upload: its base64 text (4/3
// of it, 8,000,000 bytes) plus the JSON around it has to fit in one
// MaxLineSize (8 MiB) request line, which 6 MiB would not.
const MaxUploadBytes = 6_000_000

// UploadExts are the accepted file extensions (lowercase).
var UploadExts = []string{"png", "jpg", "jpeg", "webp", "gif"}

// UploadArgs: Name is the target session, Ext one of UploadExts, Data the file
// in standard base64 (padded).
type UploadArgs struct {
	Name string `json:"name"`
	Ext  string `json:"ext"`
	Data string `json:"data"`
}

// UploadResult: Path is the absolute path of the stored file on the daemon's
// host, Size its length in bytes.
type UploadResult struct {
	Path string `json:"path"`
	Size int    `json:"size"`
}
