package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// DownloadTicket is the single-file, single-mode, short-lived token handed to
// the plain-HTTP content server. The content server verifies it statelessly
// (HMAC signature + expiry + scope match) — no DB hit, no ACL re-check —
// which keeps the byte path fast and horizontally scalable. Blast radius of a
// leak is one file for TTL seconds.
type DownloadTicket struct {
	FileID      string `json:"fid"`
	UserID      string `json:"uid"`
	Disposition string `json:"d"` // inline | attachment
	Filename    string `json:"fn"`
	// Version and ContentHash pin the ticket to one version's bytes; both are
	// empty for a plain download ticket (which means "whatever is current").
	// An edit session mints a pinned ticket so the editor loads exactly the
	// bytes its commit will be based on, and the content route refuses to
	// serve a different version under a pinned ticket instead of silently
	// handing the editor someone else's newer bytes to overwrite.
	Version     int32  `json:"v,omitempty"`
	ContentHash string `json:"h,omitempty"`
	// ThumbnailKey, when set, scopes the ticket to one stored preview object
	// instead of the file's blocks: clients read previews through this service
	// (`/thumbnails/{file_id}`) so the object store never has to be reachable
	// from a phone. A ticket without it cannot read a preview, and one with it
	// cannot read content.
	ThumbnailKey string `json:"tk,omitempty"`
	// IssuedAtUnix trades a slightly larger payload for a verification that
	// needs no key-version tracking: a check against now-TransmissionWindow
	// bounds clock skew on both sides.
	IssuedAtUnix int64 `json:"iat"`
	ExpiresUnix  int64 `json:"exp"`
}

const (
	// TicketTTL is short by design: the SPA fetches the ticket and opens the
	// content URL immediately after, and long downloads refresh the ticket
	// rather than carrying an account-scoped bearer token in the URL.
	TicketTTL = 2 * time.Minute
	// TransmissionWindow bounds accepted clock skew between ticket mint
	// (metadata) and verification (content server).
	TransmissionWindow = 90 * time.Second
	// ThumbnailTicketTTL is deliberately longer than TicketTTL: a preview URL
	// is cached with the image list (minutes) while a download URL is opened
	// the moment it is minted.
	ThumbnailTicketTTL = 30 * time.Minute
	// maxFilenameInTicket keeps the signed payload tiny; real filenames are
	// capped at 255 bytes by validateName anyway.
	maxFilenameInTicket = 255
)

// ContentDispositionHeader renders the Content-Disposition value for a
// download or preview: <disposition>; filename*=UTF-8”<escaped name>.
//
// Both tiers share it deliberately. Tier A sets it as a response header; Tier
// B carries it in the presigned URL's signed response-content-disposition
// parameter, which the object store then echoes as the same header. RFC 6266
// encoding keeps non-ASCII names intact, and PathEscape is what makes a
// filename safe in both places — it cannot inject header bytes or a query
// parameter.
func ContentDispositionHeader(disposition, filename string) string {
	if disposition != "inline" {
		disposition = "attachment"
	}
	if filename == "" {
		return disposition
	}
	return fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(filename))
}

// MintDownloadTicket signs a ticket for fileID after the CALLER has verified
// ownership (GetDownloadTicket does the ACL check upstream of this call).
func MintDownloadTicket(fileID, userID, disposition, filename string, now time.Time) (DownloadTicket, error) {
	if fileID == "" || userID == "" {
		return DownloadTicket{}, errors.New("file id and user id are required")
	}
	if disposition != "inline" && disposition != "attachment" {
		disposition = "attachment"
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "download"
	}
	if len(filename) > maxFilenameInTicket {
		filename = filename[:maxFilenameInTicket]
	}
	return DownloadTicket{
		FileID:       fileID,
		UserID:       userID,
		Disposition:  disposition,
		Filename:     filename,
		IssuedAtUnix: now.Unix(),
		ExpiresUnix:  now.Add(TicketTTL).Unix(),
	}, nil
}

// MintVersionedTicket is MintDownloadTicket plus the version pin the edit path
// needs: the returned ticket names one (file, version, content hash), and a
// reader serving it can prove it handed over exactly those bytes.
func MintVersionedTicket(fileID, userID, disposition, filename string, version int32, contentHash string, now time.Time) (DownloadTicket, error) {
	ticket, err := MintDownloadTicket(fileID, userID, disposition, filename, now)
	if err != nil {
		return DownloadTicket{}, err
	}
	ticket.Version = version
	ticket.ContentHash = contentHash
	return ticket, nil
}

// MintThumbnailTicket signs a ticket for one stored preview object. The
// caller has already checked that the file is the user's and that its preview
// is ready (GetThumbnailURL does that upstream).
//
// Previews are served through this service rather than as a presigned object
// URL: the store is bound to loopback in dev and private in production, so a
// presigned link is only reachable from the machine that signed it.
func MintThumbnailTicket(fileID, userID, thumbnailKey string, now time.Time) (DownloadTicket, error) {
	if fileID == "" || userID == "" {
		return DownloadTicket{}, errors.New("file id and user id are required")
	}
	if thumbnailKey == "" {
		return DownloadTicket{}, errors.New("thumbnail key is required")
	}
	return DownloadTicket{
		FileID:       fileID,
		UserID:       userID,
		Disposition:  "inline",
		Filename:     "preview",
		ThumbnailKey: thumbnailKey,
		IssuedAtUnix: now.Unix(),
		ExpiresUnix:  now.Add(ThumbnailTicketTTL).Unix(),
	}, nil
}

// sign returns base64url(HMAC-SHA256(key, payload)).
func sign(payload []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// EncodeTicket serializes the ticket as "<base64url(json)>.<signature>".
func EncodeTicket(t DownloadTicket, key string) string {
	payload, err := json.Marshal(t)
	if err != nil {
		// Fields are plain strings; marshal cannot fail. Guard anyway.
		payload = []byte("{}")
	}
	b64 := base64.RawURLEncoding.EncodeToString(payload)
	return b64 + "." + sign(payload, key)
}

// VerifyTicket validates signature, expiry, scope, and freshness. now is
// injected for tests. A valid ticket is returned as-is.
func VerifyTicket(encoded, key string, now time.Time) (DownloadTicket, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return DownloadTicket{}, errors.New("malformed ticket")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return DownloadTicket{}, errors.New("malformed ticket payload")
	}
	if !hmac.Equal([]byte(sign(payload, key)), []byte(parts[1])) {
		return DownloadTicket{}, errors.New("invalid ticket signature")
	}
	var t DownloadTicket
	if err := json.Unmarshal(payload, &t); err != nil {
		return DownloadTicket{}, errors.New("malformed ticket claims")
	}
	nowUnix := now.Unix()
	if nowUnix+int64(TransmissionWindow.Seconds()) < t.IssuedAtUnix {
		return DownloadTicket{}, errors.New("ticket issued in the future")
	}
	if nowUnix > t.ExpiresUnix+int64(TicketTTL/time.Second) {
		return DownloadTicket{}, errors.New("ticket expired")
	}
	if t.FileID == "" || t.UserID == "" {
		return DownloadTicket{}, errors.New("ticket missing scope")
	}
	// A preview key is a path inside the bucket; refuse anything that is not
	// the per-file prefix the minting side always uses, so a forgotten check
	// upstream cannot turn a ticket into a read of somebody else's object.
	if t.ThumbnailKey != "" && !strings.HasPrefix(t.ThumbnailKey, "thumbnails/"+t.FileID+"/") {
		return DownloadTicket{}, errors.New("thumbnail key does not belong to this file")
	}
	return t, nil
}

// GetFileManifest returns the dense block manifest after the same
// ownership check every other file read goes through.
func (s *MetadataService) GetFileManifest(ctx context.Context, ownerID, fileID string) (repository.FileManifestRow, error) {
	if _, err := s.GetFileOwned(ctx, ownerID, fileID); err != nil {
		return repository.FileManifestRow{}, err
	}
	return s.metadataRepo.GetFileManifest(ctx, fileID)
}

// DownloadTicketResult carries everything the SPA needs to hand the URL to
// the browser (an <a download>, <video>, <img>): the full content-server URL
// with ticket appended, plus display metadata for the filename.
type DownloadTicketResult struct {
	Ticket           string
	URL              string
	ExpiresInSeconds int64
	Filename         string
	ContentType      string
	SizeBytes        int64
	Stitched         bool
}

// GetDownloadTicket verifies ownership once, mints the short-lived ticket,
// and builds the absolute content-server URL. Tier B hit: when the file has
// a consolidated object, the URL is a presigned S3 GET instead of the
// content endpoint — zero proxying through our infra.
func (s *MetadataService) GetDownloadTicket(ctx context.Context, ownerID, fileID, disposition string) (DownloadTicketResult, error) {
	file, err := s.GetFileOwned(ctx, ownerID, fileID)
	if err != nil {
		return DownloadTicketResult{}, err
	}

	ticket, err := MintDownloadTicket(fileID, ownerID, disposition, file.Filename, time.Now())
	if err != nil {
		return DownloadTicketResult{}, err
	}
	encoded := EncodeTicket(ticket, s.cfg.DownloadTicketKey)

	result := DownloadTicketResult{
		Ticket:           encoded,
		ExpiresInSeconds: int64(TicketTTL.Seconds()),
		Filename:         file.Filename,
		ContentType:      file.ContentType,
		SizeBytes:        file.SizeBytes,
	}

	// Tier B fast path: a consolidated object exists → presigned URL straight
	// to object storage. Content-addressed + immutable, so the presigned GET
	// is also stable across Range resume requests the ticket would not cover.
	var consolidatedPath string
	if s.stitchRepo != nil {
		consolidatedPath, err = s.stitchRepo.GetStitchedObjectKey(ctx, fileID)
		if err != nil {
			return DownloadTicketResult{}, fmt.Errorf("look up stitched object: %w", err)
		}
		result.Stitched = consolidatedPath != ""
	}
	// Presigned URLs are handed out for downloads only. An inline preview is
	// embedded by <img>/<video>/a PDF engine, and those need the byte-range
	// handshake on the origin that serves the document: the PDF engine probes
	// with a HEAD first, and a presigned GET URL answers HEAD with 403 because
	// its signature covers GET alone. It also hides Accept-Ranges from
	// JavaScript, so the engine would stop seeking and download everything.
	// Consolidated previews therefore stay on the content origin, which streams
	// the consolidated object itself.
	if consolidatedPath != "" && s.presigner != nil && ticket.Disposition != "inline" {
		presigned, presignErr := s.presigner.PresignFileGET(ctx, consolidatedPath, PresignRequest{
			Disposition: ticket.Disposition,
			Filename:    ticket.Filename,
			ContentType: file.ContentType,
			Expiry:      TicketTTL,
		})
		if presignErr == nil {
			result.URL = presigned
			return result, nil
		}
		s.logger.Warn("presign consolidated object failed; falling back to content path",
			zap.String("fileID", fileID), zap.Error(presignErr))
	}

	// Tier A path: the content endpoint streams block ranges itself. The URL is
	// for the client, so it uses the public base when one is configured.
	base := strings.TrimRight(s.cfg.ClientContentBaseURL(), "/")
	result.URL = fmt.Sprintf("%s/content/%s?ticket=%s&disposition=%s",
		base, url.PathEscape(fileID), url.QueryEscape(encoded), url.QueryEscape(ticket.Disposition))
	return result, nil
}
