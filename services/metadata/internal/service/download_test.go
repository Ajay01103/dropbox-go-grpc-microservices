package service

import (
	"strings"
	"testing"
	"time"
)

func TestMintAndVerifyTicketRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ticket, err := MintDownloadTicket("file-1", "user-1", "attachment", "report.pdf", now)
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	if ticket.ExpiresUnix != now.Add(TicketTTL).Unix() {
		t.Fatalf("ExpiresUnix = %d, want %d", ticket.ExpiresUnix, now.Add(TicketTTL).Unix())
	}

	encoded := EncodeTicket(ticket, "test-key")
	decoded, err := VerifyTicket(encoded, "test-key", now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("VerifyTicket() error = %v", err)
	}
	if decoded.FileID != "file-1" || decoded.UserID != "user-1" {
		t.Fatalf("decoded scope = (%q, %q), want (file-1, user-1)", decoded.FileID, decoded.UserID)
	}
	if decoded.Disposition != "attachment" || decoded.Filename != "report.pdf" {
		t.Fatalf("decoded mode = (%q, %q), want (attachment, report.pdf)", decoded.Disposition, decoded.Filename)
	}
}

func TestVerifyTicketRejectsTamperingAndWrongKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ticket, err := MintDownloadTicket("file-1", "user-1", "inline", "a.txt", now)
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	encoded := EncodeTicket(ticket, "test-key")

	if _, err := VerifyTicket(encoded, "other-key", now); err == nil {
		t.Fatal("VerifyTicket() with a different key succeeded, want error")
	}

	parts := strings.SplitN(encoded, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("encoded ticket %q has no signature separator", encoded)
	}
	tampered := parts[0] + "x." + parts[1]
	if _, err := VerifyTicket(tampered, "test-key", now); err == nil {
		t.Fatal("VerifyTicket() accepted a tampered payload, want error")
	}
	if _, err := VerifyTicket("not-a-ticket", "test-key", now); err == nil {
		t.Fatal("VerifyTicket() accepted a malformed ticket, want error")
	}
}

// ContentDispositionHeader is the single renderer behind both download tiers:
// Tier A sets its output as a response header, Tier B puts it in the presigned
// URL's signed response-content-disposition parameter. Pinning the exact value
// here is what keeps the two tiers from drifting apart.
func TestContentDispositionHeader(t *testing.T) {
	tests := []struct {
		name        string
		disposition string
		filename    string
		want        string
	}{
		{
			name:        "attachment keeps the bare disposition and names the file",
			disposition: "attachment",
			filename:    "report.pdf",
			want:        "attachment; filename*=UTF-8''report.pdf",
		},
		{
			name:        "inline survives with a space in the name",
			disposition: "inline",
			filename:    "Q3 report.pdf",
			want:        "inline; filename*=UTF-8''Q3%20report.pdf",
		},
		{
			name:        "non-ASCII names are percent-encoded, not dropped",
			disposition: "attachment",
			filename:    "résumé.pdf",
			want:        "attachment; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf",
		},
		{
			name:        "an empty filename leaves just the disposition",
			disposition: "attachment",
			filename:    "",
			want:        "attachment",
		},
		{
			name:        "an unexpected disposition falls back to attachment",
			disposition: "preview",
			filename:    "a.txt",
			want:        "attachment; filename*=UTF-8''a.txt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContentDispositionHeader(tt.disposition, tt.filename); got != tt.want {
				t.Fatalf("ContentDispositionHeader(%q, %q) = %q, want %q", tt.disposition, tt.filename, got, tt.want)
			}
		})
	}
}

// The filename is attacker-controlled (it arrives with the upload) and ends up
// inside a header value, so it must never be able to break out of it.
func TestContentDispositionHeaderCannotInjectHeaders(t *testing.T) {
	got := ContentDispositionHeader("attachment", "a\"\r\nX-Evil: 1\r\n\r\nbody.txt")
	if strings.ContainsAny(got, "\r\n\"") {
		t.Fatalf("ContentDispositionHeader() = %q, want no raw CR, LF or quote", got)
	}
	if !strings.HasPrefix(got, "attachment; filename*=UTF-8''") {
		t.Fatalf("ContentDispositionHeader() = %q, want an attachment disposition with a filename", got)
	}
}

func TestVerifyTicketExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ticket, err := MintDownloadTicket("file-1", "user-1", "attachment", "a.bin", now)
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	encoded := EncodeTicket(ticket, "test-key")

	// Still valid within the TTL plus the transmission window.
	if _, err := VerifyTicket(encoded, "test-key", now.Add(TicketTTL+TransmissionWindow)); err != nil {
		t.Fatalf("VerifyTicket() just past TTL error = %v, want nil", err)
	}
	// Expired well past TTL + window.
	if _, err := VerifyTicket(encoded, "test-key", now.Add(TicketTTL+TransmissionWindow+time.Minute)); err == nil {
		t.Fatal("VerifyTicket() accepted an expired ticket, want error")
	}
	// Issued far in the future.
	future, err := MintDownloadTicket("file-1", "user-1", "attachment", "a.bin", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	if _, err := VerifyTicket(EncodeTicket(future, "test-key"), "test-key", now); err == nil {
		t.Fatal("VerifyTicket() accepted a far-future ticket, want error")
	}
}

func TestMintDownloadTicketNormalizesDisposition(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ticket, err := MintDownloadTicket("file-1", "user-1", "nonsense", "", now)
	if err != nil {
		t.Fatalf("MintDownloadTicket() error = %v", err)
	}
	if ticket.Disposition != "attachment" {
		t.Fatalf("Disposition = %q, want attachment (default)", ticket.Disposition)
	}
	if ticket.Filename != "download" {
		t.Fatalf("Filename = %q, want download fallback", ticket.Filename)
	}
}

// A preview ticket is the same signed envelope as a download ticket, scoped to
// one stored preview. It outlives the download TTL because clients cache the
// image URL alongside the list that showed it.
func TestMintThumbnailTicketRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const key = "thumbnails/file-1/preview.jpg"
	ticket, err := MintThumbnailTicket("file-1", "user-1", key, now)
	if err != nil {
		t.Fatalf("MintThumbnailTicket() error = %v", err)
	}
	if ticket.ExpiresUnix != now.Add(ThumbnailTicketTTL).Unix() {
		t.Fatalf("ExpiresUnix = %d, want %d", ticket.ExpiresUnix, now.Add(ThumbnailTicketTTL).Unix())
	}

	decoded, err := VerifyTicket(EncodeTicket(ticket, "test-key"), "test-key", now.Add(20*time.Minute))
	if err != nil {
		t.Fatalf("VerifyTicket() error = %v", err)
	}
	if decoded.ThumbnailKey != key {
		t.Fatalf("ThumbnailKey = %q, want %q", decoded.ThumbnailKey, key)
	}
	if decoded.FileID != "file-1" || decoded.Disposition != "inline" {
		t.Fatalf("decoded = (%q, %q), want (file-1, inline)", decoded.FileID, decoded.Disposition)
	}
}

func TestMintThumbnailTicketRequiresKey(t *testing.T) {
	if _, err := MintThumbnailTicket("file-1", "user-1", "", time.Now()); err == nil {
		t.Fatal("MintThumbnailTicket() accepted an empty key")
	}
}
