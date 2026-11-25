package wscli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedFile writes contents into a temp file and returns its path.
func seedFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return path
}

// UploadItemAttachment must hit the v2 item route, send a bearer token, put
// the bytes in a `file` part under the base filename, and decode a data document.
func TestClient_UploadItemAttachment_HappyPath(t *testing.T) {
	var gotPath, gotAuth, gotFilename string
	var gotBytes []byte

	c, _ := newTestPageClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if !strings.HasPrefix(mediaType, "multipart/") {
			t.Errorf("content-type: want multipart/*, got %q", mediaType)
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			if part.FormName() != "file" {
				continue
			}
			gotFilename = part.FileName()
			gotBytes, _ = io.ReadAll(part)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DataDocument[Attachment]{
			Data: Attachment{
				ID:               77,
				Filename:         "stored-abc.png",
				OriginalFilename: gotFilename,
				MimeType:         "image/png",
				FileSize:         int64(len(gotBytes)),
			},
		})
	})

	att, err := c.UploadItemAttachment(1181, "mockup.png", strings.NewReader("\x89PNG-ish"))
	if err != nil {
		t.Fatalf("UploadItemAttachment: %v", err)
	}

	if gotPath != "/rest/api/v2/items/1181/attachments" {
		t.Errorf("upload path: %q", gotPath)
	}
	if gotAuth != "Bearer ws_test_token" {
		t.Errorf("auth header: %q", gotAuth)
	}
	if gotFilename != "mockup.png" {
		t.Errorf("multipart filename: want mockup.png, got %q", gotFilename)
	}
	if string(gotBytes) != "\x89PNG-ish" {
		t.Errorf("multipart bytes: %q", string(gotBytes))
	}
	if att.ID != 77 || att.OriginalFilename != "mockup.png" {
		t.Errorf("decoded attachment: %+v", att)
	}
}

// A v2 error document must come back as an *APIError carrying the status,
// so translateItemAttachmentError can recognise a 404.
func TestClient_UploadItemAttachment_APIErrorEnvelope(t *testing.T) {
	c, _ := newTestPageClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Item not found"},"request_id":"req_test"}`))
	})

	_, err := c.UploadItemAttachment(9999, "x.png", strings.NewReader("data"))
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("expected APIError with Status=404, got %T %v", err, err)
	}
}

// A response that parses but carries no attachment id is a protocol error, not
// a silent success — callers print the record, so an empty one would mislead.
func TestClient_UploadItemAttachment_MissingAttachmentID(t *testing.T) {
	c, _ := newTestPageClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	})

	if _, err := c.UploadItemAttachment(1, "x.png", strings.NewReader("data")); err == nil {
		t.Fatal("expected an error when the envelope has no attachment id")
	}
}

// Every failure mode of checkUploadableFiles must be caught before any
// request goes out — that is the whole point of the pre-flight.
func TestCheckUploadableFiles(t *testing.T) {
	dir := t.TempDir()
	good := seedFile(t, dir, "good.png", "some bytes")
	empty := seedFile(t, dir, "empty.png", "")
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}

	t.Run("all readable files pass", func(t *testing.T) {
		if err := checkUploadableFiles([]string{good, good}); err != nil {
			t.Errorf("expected pass, got %v", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		err := checkUploadableFiles([]string{good, filepath.Join(dir, "nope.png")})
		if err == nil || !strings.Contains(err.Error(), "nope.png") {
			t.Errorf("want error naming the missing file, got %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		err := checkUploadableFiles([]string{subdir})
		if err == nil || !strings.Contains(err.Error(), "is a directory") {
			t.Errorf("want is-a-directory error, got %v", err)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		err := checkUploadableFiles([]string{empty})
		if err == nil || !strings.Contains(err.Error(), "file is empty") {
			t.Errorf("want empty-file error, got %v", err)
		}
	})

	t.Run("unreadable file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: mode bits do not deny reads")
		}
		locked := seedFile(t, dir, "locked.png", "secret")
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

		err := checkUploadableFiles([]string{locked})
		if err == nil || !strings.Contains(err.Error(), "locked.png") {
			t.Errorf("want permission error naming the file, got %v", err)
		}
	})
}

// The route's body cap is the effective attachment setting, which an
// administrator can raise or lower, so the CLI keeps no local copy of it. A
// large file must pass the pre-flight check and reach the server, whose
// rejection is what names the configured limit.
func TestCheckUploadableFiles_LeavesSizeToServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	f, err := os.Create(path) //nolint:gosec // G304: path built from t.TempDir()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Sparse file: no bytes are actually written, but Stat reports the size.
	// 1 GiB is above every configured limit the server accepts, so a stale
	// client-side ceiling would fail here.
	if err := f.Truncate(1 << 30); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := checkUploadableFiles([]string{path}); err != nil {
		t.Fatalf("large file rejected locally; server owns the limit: %v", err)
	}
}

// The upload route returns the same 404 for "no such item" and "you lack
// item.edit", so the CLI message must not confirm the item exists.
func TestTranslateItemAttachmentError(t *testing.T) {
	t.Run("404 names both possibilities without confirming existence", func(t *testing.T) {
		got := translateItemAttachmentError(&APIError{Status: http.StatusNotFound, Message: "Item not found"}, "shot.png")
		msg := got.Error()
		if !strings.Contains(msg, "shot.png") {
			t.Errorf("message should name the file: %q", msg)
		}
		if !strings.Contains(msg, "item not found, or you lack item.edit") {
			t.Errorf("message should offer both readings: %q", msg)
		}
	})

	t.Run("non-404 keeps the server's message", func(t *testing.T) {
		got := translateItemAttachmentError(&APIError{Status: http.StatusBadRequest, Message: "File extension .exe is not allowed"}, "tool.exe")
		if !strings.Contains(got.Error(), ".exe is not allowed") {
			t.Errorf("server message lost: %q", got.Error())
		}
		if !strings.Contains(got.Error(), "tool.exe") {
			t.Errorf("file context lost: %q", got.Error())
		}
	})

	// The restrictions an admin can configure — a smaller max size, an
	// allowed-MIME list, the global off switch — are enforced server-side
	// only. The CLI must relay the server's explanation intact rather than
	// flattening it into a status code, because that text is the only thing
	// telling the user which knob rejected them.
	t.Run("server-side restrictions relay verbatim", func(t *testing.T) {
		cases := map[string]struct {
			apiErr *APIError
			want   string
		}{
			"uploads disabled": {
				&APIError{Status: http.StatusServiceUnavailable, Message: "Attachments are not enabled on this server"},
				"Attachments are not enabled on this server",
			},
			"configured size limit": {
				&APIError{Status: http.StatusBadRequest, Message: "File too large. Maximum size: 5242880 bytes"},
				"Maximum size: 5242880 bytes",
			},
			"allowed mime types": {
				&APIError{Status: http.StatusBadRequest, Message: "File type application/pdf not allowed by server configuration"},
				"not allowed by server configuration",
			},
			"extension blocklist": {
				&APIError{Status: http.StatusBadRequest, Message: "file extension .svg is not allowed for security reasons"},
				".svg is not allowed",
			},
			"content/extension mismatch": {
				&APIError{Status: http.StatusBadRequest, Message: "File content validation failed: file content type (text/plain) doesn't match extension .png"},
				"doesn't match extension .png",
			},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				got := translateItemAttachmentError(tc.apiErr, "shot.png").Error()
				if !strings.Contains(got, tc.want) {
					t.Errorf("server explanation lost:\n got %q\n want substring %q", got, tc.want)
				}
				if !strings.Contains(got, "shot.png") {
					t.Errorf("file context lost: %q", got)
				}
			})
		}
	})

	t.Run("404 detected through a wrap", func(t *testing.T) {
		base := &APIError{Status: http.StatusNotFound, Message: "Item not found"}
		got := translateItemAttachmentError(fmt.Errorf("upload request: %w", base), "shot.png")
		if !strings.Contains(got.Error(), "you lack item.edit") {
			t.Errorf("wrapped 404 not translated: %q", got.Error())
		}
	})
}
