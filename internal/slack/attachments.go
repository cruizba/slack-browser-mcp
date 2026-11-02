package slack

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/failure"
)

// Files that travel with a normal message. The tool runs on the machine the agent
// runs on, so a path is the whole interface: the caller says which file, this file
// says whether it may go and what Slack will call it.
//
// A send is a dry run until it is confirmed, so the two steps are separate on
// purpose: PlanAttachments only looks at metadata, and the bytes are read at the
// last moment. A preview that fails because a path is wrong costs a stat.
const (
	// MaxAttachments is how many files one message may carry.
	MaxAttachments = 10
	// MaxAttachmentBytes is the per-file cap this tool enforces; Slack's plan limit
	// can be lower, and when it is, Slack refuses the upload and says so.
	MaxAttachmentBytes = 100 * 1024 * 1024
)

var mimeByExtension = map[string]string{
	"md": "text/markdown", "markdown": "text/markdown", "txt": "text/plain",
	"log": "text/plain", "csv": "text/csv", "tsv": "text/tab-separated-values",
	"json": "application/json", "jsonl": "application/x-ndjson",
	"yaml": "application/yaml", "yml": "application/yaml", "toml": "application/toml",
	"xml": "application/xml", "html": "text/html", "pdf": "application/pdf",
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif",
	"webp": "image/webp", "svg": "image/svg+xml", "zip": "application/zip",
	"gz": "application/gzip", "tar": "application/x-tar",
}

// HumanSize is the size a person reads.
func HumanSize(bytes int64) string {
	switch {
	case bytes < 1024:
		return itoa(int(bytes)) + " B"
	case bytes < 1024*1024:
		return strconv.FormatFloat(float64(bytes)/1024, 'f', 1, 64) + " KB"
	default:
		return strconv.FormatFloat(float64(bytes)/(1024*1024), 'f', 1, 64) + " MB"
	}
}

// ContentTypeOf is only a label: Slack sniffs the type itself, so a wrong guess here
// misleads nobody.
func ContentTypeOf(name string) string {
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))

	if contentType, known := mimeByExtension[extension]; known {
		return contentType
	}

	return "application/octet-stream"
}

// Plan is one attachment as the dry run describes it: enough to show, not the bytes.
type Plan struct {
	// Path is as the caller wrote it, so an error names the path they know.
	Path string
	// Name is what Slack shows: the basename, because the directory it came from is
	// not the channel's business.
	Name        string
	Bytes       int64
	ContentType string
}

// PlanAttachments checks every path without reading a byte.
func PlanAttachments(paths []string) ([]Plan, error) {
	if len(paths) > MaxAttachments {
		return nil, errors.New("A message carries at most " + itoa(MaxAttachments) +
			" attachments; " + itoa(len(paths)) + " were given.")
	}

	plans := make([]Plan, 0, len(paths))

	for _, path := range paths {
		resolved, err := filepath.Abs(path)
		if err != nil {
			return nil, errors.New("No file at " + path + ". Attachments are local paths on the machine that runs this tool.")
		}

		info, err := os.Stat(resolved)
		if err != nil {
			return nil, errors.New("No file at " + path + ". Attachments are local paths on the machine that runs this tool.")
		}

		switch {
		case !info.Mode().IsRegular():
			return nil, errors.New(path + " is not a regular file.")
		case info.Size() == 0:
			return nil, errors.New(path + " is empty; Slack refuses a zero-byte upload.")
		case info.Size() > MaxAttachmentBytes:
			return nil, errors.New(path + " is " + HumanSize(info.Size()) + "; this tool uploads at most " +
				HumanSize(MaxAttachmentBytes) + " per file.")
		}

		plans = append(plans, Plan{
			Path:        path,
			Name:        filepath.Base(resolved),
			Bytes:       info.Size(),
			ContentType: ContentTypeOf(filepath.Base(resolved)),
		})
	}

	return plans, nil
}

// ReadAttachments loads the bytes, only for a confirmed send, so a preview never
// loads a 90 MB log.
func ReadAttachments(plans []Plan) ([]Upload, error) {
	uploads := make([]Upload, 0, len(plans))

	for _, plan := range plans {
		resolved, err := filepath.Abs(plan.Path)
		if err != nil {
			return nil, &failure.StoreError{Message: "No file at " + plan.Path + "."}
		}

		bytes, err := os.ReadFile(resolved) //nolint:gosec // the caller named this path on purpose
		if err != nil {
			return nil, &failure.StoreError{Message: "Reading " + plan.Path + ": " + err.Error()}
		}

		uploads = append(uploads, Upload{Name: plan.Name, Bytes: bytes})
	}

	return uploads, nil
}

// DescribeAttachment is one line of the dry run.
func DescribeAttachment(plan Plan) string {
	return plan.Name + " (" + plan.ContentType + ", " + HumanSize(plan.Bytes) + ")"
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
