package email

import "testing"

func TestParserFallsBackToRawHeadersWhenEnvelopeIsMissing(t *testing.T) {
	raw := []byte("Date: Fri, 17 Jul 2026 10:00:00 +0200\r\n" +
		"From: =?UTF-8?Q?Bj=C3=B6rn_Customer?= <customer@example.com>\r\n" +
		"To: Intake <ingest@example.com>\r\n" +
		"Subject: =?UTF-8?Q?Pr=C3=BCfung_request?=\r\n" +
		"Message-ID: <reply@example.com>\r\n" +
		"In-Reply-To: <previous@example.com>\r\n" +
		"References: <root@example.com> <previous@example.com>\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\nBody")

	parsed := NewParser().Parse(&FetchedMessage{UID: 9, Raw: raw})
	if parsed.MessageID != "<reply@example.com>" || parsed.InReplyTo != "<previous@example.com>" {
		t.Fatalf("thread headers = Message-ID %q, In-Reply-To %q", parsed.MessageID, parsed.InReplyTo)
	}
	if len(parsed.References) != 2 || parsed.References[0] != "<root@example.com>" || parsed.References[1] != "<previous@example.com>" {
		t.Fatalf("References = %v", parsed.References)
	}
	if parsed.Subject != "Prüfung request" || parsed.From.Name != "Björn Customer" || parsed.From.Address != "customer@example.com" {
		t.Fatalf("raw-header envelope = subject %q, from %#v", parsed.Subject, parsed.From)
	}
	if len(parsed.To) != 1 || parsed.To[0].Address != "ingest@example.com" {
		t.Fatalf("To = %#v", parsed.To)
	}
	if parsed.Date.IsZero() {
		t.Fatalf("Date = %v", parsed.Date)
	}
	if parsed.PlainBody != "Body" {
		t.Fatalf("body = %q", parsed.PlainBody)
	}
}

func TestCanonicalMessageIDRejectsHeaderSyntax(t *testing.T) {
	for _, invalid := range []string{"", "<>", "two words@example.com", "a@example.com\r\nBcc: victim@example.com"} {
		if got := canonicalMessageID(invalid); got != "" {
			t.Fatalf("canonicalMessageID(%q) = %q, want empty", invalid, got)
		}
	}
	for input, want := range map[string]string{
		"message@example.com":   "<message@example.com>",
		"<message@example.com>": "<message@example.com>",
	} {
		if got := canonicalMessageID(input); got != want {
			t.Fatalf("canonicalMessageID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDecodeHeaderWordSupportsLegacyCharsets(t *testing.T) {
	if got := decodeHeaderWord("=?windows-1252?Q?Pr=FCfung?="); got != "Prüfung" {
		t.Fatalf("decoded windows-1252 header = %q", got)
	}
}

// A screenshot pasted into a mail client arrives as a MIME image part with no
// filename. It must be kept as a generated attachment (image-N with the media
// type's extension), while a nameless non-image part is still dropped.
func TestParserUnnamedInlineImageKeptWithGeneratedName(t *testing.T) {
	raw := []byte("From: customer@example.com\r\n" +
		"To: intake@example.com\r\n" +
		"Subject: Screenshot\r\n" +
		"Message-ID: <inline-shot@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=MIMEBOUND\r\n\r\n" +
		"--MIMEBOUND\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		"See the attached screenshot.\r\n" +
		"--MIMEBOUND\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Disposition: inline\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"aGVsbG8=\r\n" +
		"--MIMEBOUND\r\n" +
		"Content-Type: application/octet-stream\r\n\r\n" +
		"nameless non-image\r\n" +
		"--MIMEBOUND--\r\n")

	parsed := NewParser().Parse(&FetchedMessage{UID: 1, Raw: raw})
	var names []string
	for _, attachment := range parsed.Attachments {
		names = append(names, attachment.Filename)
	}
	if len(parsed.Attachments) != 1 {
		t.Fatalf("attachments = %v, want exactly the named inline image", names)
	}
	kept := parsed.Attachments[0]
	if kept.Filename != "image-1.png" {
		t.Fatalf("generated filename = %q, want \"image-1.png\"", kept.Filename)
	}
	if kept.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png", kept.ContentType)
	}
	if string(kept.Data) != "hello" {
		t.Fatalf("data = %q, want decoded \"hello\"", kept.Data)
	}
}
