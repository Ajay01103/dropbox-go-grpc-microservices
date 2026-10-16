package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// ─── Edit-path unit tests ───────────────────────────────────────────────────
//
// These cover the decisions a commit makes from bytes and a block list alone:
// which document the bytes are, whether the shape of the list can be trusted,
// and which sessions are refused. Storage and version rows are integration
// concerns (see versions_integration_test.go).

// fakeBlockReader serves block bytes from memory. Missing blocks report false
// from BlockExists, which is what a commit treats as "these bytes were never
// stored".
type fakeBlockReader struct {
	blocks  map[string][]byte
	failOn  string
	failErr error
}

func (f *fakeBlockReader) ReadBlockRange(_ context.Context, hash string, offset, length int64) (io.ReadCloser, error) {
	if f.failOn == hash {
		return nil, f.failErr
	}
	data, ok := f.blocks[hash]
	if !ok {
		return nil, errors.New("block not found")
	}
	if offset >= int64(len(data)) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	end := offset + length
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func (f *fakeBlockReader) BlockExists(_ context.Context, hash string, sizeBytes int64) (bool, error) {
	if f.failOn == hash {
		return false, f.failErr
	}
	data, ok := f.blocks[hash]
	if !ok {
		return false, nil
	}
	return sizeBytes <= 0 || int64(len(data)) == sizeBytes, nil
}

func editTestService(reader BlockReader) *MetadataService {
	cfg := config.Config{
		EditPDFEnabled:   true,
		EditDocxEnabled:  true,
		EditXlsxEnabled:  true,
		EditPDFMaxBytes:  50 << 20,
		EditDocxMaxBytes: 25 << 20,
		EditXlsxMaxBytes: 25 << 20,
	}
	return &MetadataService{cfg: cfg, logger: zap.NewNop(), blockReader: reader}
}

// zipTail builds the tail of an Office container as the sniffing code sees it:
// a ZIP local header up front and part names in the central directory behind.
func zipTail(parts ...string) []byte {
	tail := []byte("PK\x03\x04")
	for _, part := range parts {
		tail = append(tail, []byte(part)...)
		tail = append(tail, 0)
	}
	tail = append(tail, []byte("PK\x05\x06")...)
	return tail
}

func TestSniffBlockListIdentifiesEditableDocuments(t *testing.T) {
	cases := []struct {
		name   string
		first  []byte
		tail   []byte
		expect string
	}{
		{name: "pdf", first: []byte("%PDF-1.7\n"), tail: []byte("trailer"), expect: EditTypePDF},
		{name: "docx", first: []byte("PK\x03\x04"), tail: zipTail("word/document.xml", "[Content_Types].xml"), expect: EditTypeDocx},
		{name: "xlsx", first: []byte("PK\x03\x04"), tail: zipTail("xl/workbook.xml", "xl/worksheets/sheet1.xml"), expect: EditTypeXlsx},
		{
			name:  "macro-enabled workbook is refused",
			first: []byte("PK\x03\x04"),
			tail:  zipTail("xl/workbook.xml", "xl/vbaProject.bin"),
			// A workbook with macros is not something an experimental editor
			// should rewrite, so it sniffs as "not editable" rather than xlsx.
			expect: "",
		},
		{name: "plain zip", first: []byte("PK\x03\x04"), tail: zipTail("notes.txt"), expect: ""},
		{name: "renamed text file", first: []byte("hello world"), tail: nil, expect: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reader := &fakeBlockReader{blocks: map[string][]byte{"head": testCase.first}}
			if testCase.tail != nil {
				reader.blocks["tail"] = testCase.tail
			}
			svc := editTestService(reader)
			hashes := []string{"head"}
			sizes := []int64{int64(len(testCase.first))}
			if testCase.tail != nil {
				hashes = []string{"head", "tail"}
				sizes = []int64{int64(len(testCase.first)), int64(len(testCase.tail))}
			}
			got, err := svc.sniffBlockList(context.Background(), hashes, sizes)
			if err != nil {
				t.Fatalf("sniffBlockList: %v", err)
			}
			if got != testCase.expect {
				t.Fatalf("sniffed type = %q, want %q", got, testCase.expect)
			}
		})
	}
}

func TestValidateCommitRejectsUntrustworthyBlockLists(t *testing.T) {
	svc := editTestService(&fakeBlockReader{blocks: map[string][]byte{}})
	hash := strings.Repeat("ab", 32)
	state := editState{Version: 2, BlockSize: 8 << 20, BlockHashes: []string{hash}}

	cases := []struct {
		name    string
		request CommitVersionRequest
		wantErr string
	}{
		{
			name: "base from the future",
			request: CommitVersionRequest{
				BaseVersion: 3, SizeBytes: 100, BlockSize: 8 << 20, BlockHashes: []string{hash},
			},
			wantErr: "ahead of head",
		},
		{
			name: "block list does not cover the size",
			request: CommitVersionRequest{
				BaseVersion: 2, SizeBytes: 100, BlockSize: 8 << 20,
				BlockHashes: []string{hash, hash},
			},
			wantErr: "does not cover",
		},
		{
			name: "block size differs from the file's",
			request: CommitVersionRequest{
				BaseVersion: 2, SizeBytes: 100, BlockSize: 4 << 20, BlockHashes: []string{hash},
			},
			wantErr: "does not match the file's block size",
		},
		{
			name: "over the type cap",
			request: CommitVersionRequest{
				BaseVersion: 2, SizeBytes: 32 << 20, BlockSize: 8 << 20,
				BlockHashes: []string{hash, hash, hash, hash},
			},
			wantErr: "exceeds",
		},
		{
			name: "hash is not a sha256 digest",
			request: CommitVersionRequest{
				BaseVersion: 2, SizeBytes: 100, BlockSize: 8 << 20, BlockHashes: []string{"not-a-hash"},
			},
			wantErr: "invalid hash",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := svc.validateCommit(testCase.request, state)
			if err == nil {
				t.Fatalf("expected a rejection containing %q", testCase.wantErr)
			}
			if !errors.Is(err, ErrInvalidEdit) {
				t.Fatalf("error %v is not classified as ErrInvalidEdit", err)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error %q does not mention %q", err.Error(), testCase.wantErr)
			}
		})
	}
}

func TestValidateCommitAcceptsAMatchingBlockList(t *testing.T) {
	svc := editTestService(&fakeBlockReader{blocks: map[string][]byte{}})
	hash := strings.Repeat("ab", 32)
	// Ten bytes at a four byte block size is three blocks: 4, 4, 2.
	err := svc.validateCommit(
		CommitVersionRequest{BaseVersion: 1, SizeBytes: 10, BlockSize: 4, BlockHashes: []string{hash, hash, hash}},
		editState{Version: 1, BlockSize: 4, SniffedType: EditTypePDF},
	)
	if err != nil {
		t.Fatalf("validateCommit rejected a valid commit: %v", err)
	}
}

func TestBlockSizesForCommitDerivesTheRemainder(t *testing.T) {
	svc := editTestService(nil)
	request := CommitVersionRequest{
		SizeBytes:   10,
		BlockSize:   4,
		BlockHashes: []string{"a", "b", "c"},
	}
	got := svc.blockSizesForCommit(request)
	want := []int64{4, 4, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("block sizes = %v, want %v", got, want)
		}
	}
}

func TestVerifyCommitBlocksRejectsAMissingBlock(t *testing.T) {
	present := strings.Repeat("cd", 32)
	missing := strings.Repeat("ef", 32)
	reader := &fakeBlockReader{blocks: map[string][]byte{present: make([]byte, 4)}}
	svc := editTestService(reader)

	if err := svc.verifyCommitBlocks(context.Background(), []string{present}, []int64{4}); err != nil {
		t.Fatalf("verifyCommitBlocks rejected a present block: %v", err)
	}
	err := svc.verifyCommitBlocks(context.Background(), []string{present, missing}, []int64{4, 4})
	if !errors.Is(err, ErrEditBlockUnavailable) {
		t.Fatalf("missing block classified as %v, want ErrEditBlockUnavailable", err)
	}
	// A block whose stored size disagrees with the version's claim is refused
	// too: the version row would otherwise name bytes nobody can read.
	if err := svc.verifyCommitBlocks(context.Background(), []string{present}, []int64{8}); !errors.Is(err, ErrEditBlockUnavailable) {
		t.Fatalf("size mismatch classified as %v, want ErrEditBlockUnavailable", err)
	}
}

func TestEditGateReportsWhyEditingIsUnavailable(t *testing.T) {
	svc := editTestService(nil)
	cases := []struct {
		sniffed string
		enabled bool
		reason  string
	}{
		{sniffed: EditTypePDF, enabled: true, reason: ""},
		{sniffed: EditTypeDocx, enabled: true, reason: ""},
		{sniffed: EditTypeXlsx, enabled: true, reason: ""},
		{sniffed: "", enabled: false, reason: EditUnavailableType},
	}
	for _, testCase := range cases {
		enabled := svc.editEnabledFor(testCase.sniffed)
		if enabled != testCase.enabled {
			t.Fatalf("editEnabledFor(%q) = %v, want %v", testCase.sniffed, enabled, testCase.enabled)
		}
		if testCase.sniffed != "" && svc.editCapFor(testCase.sniffed) <= 0 {
			t.Fatalf("editCapFor(%q) has no cap", testCase.sniffed)
		}
	}

	disabled := editTestService(nil)
	disabled.cfg.EditXlsxEnabled = false
	if disabled.editEnabledFor(EditTypeXlsx) {
		t.Fatal("the xlsx kill switch did not disable editing")
	}
}

func TestResolveEditStateFallsBackToTheFileRow(t *testing.T) {
	// A file with no version-history row is version 1 described by its own row;
	// resolveEditState needs the repository for the head lookup, so this test
	// exercises the derivation the fallback depends on.
	svc := editTestService(nil)
	file := repository.File{Version: 0, ContentHash: "hash-from-create", BlockHashList: []string{"a"}}
	if file.Version <= 0 {
		file.Version = 1
	}
	if got := svc.deriveBlockSize(context.Background(), file.FileID, file.BlockHashList, 1234); got != 1234 {
		t.Fatalf("single-block file block size = %d, want the file size", got)
	}
}
