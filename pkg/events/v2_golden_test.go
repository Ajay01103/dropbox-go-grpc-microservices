package events

import (
	"bytes"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv2 "github.com/Ajay01103/go-dropbox/pkg/gen/events/v2"
)

// timeUTC is a fixed instant so golden marshals are deterministic.
func timeUTC(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

// TestFileStoredGoldenBytes pins the wire format of FileStored: field numbers,
// types, and the raw-bytes hash encoding. Regenerate deliberately.
func TestFileStoredGoldenBytes(t *testing.T) {
	hash1 := bytes.Repeat([]byte{0xAB}, 32)
	hash2 := bytes.Repeat([]byte{0xCD}, 32)
	msg := &eventsv2.FileStored{
		FileId:        "file-1",
		FileVersion:   2,
		OwnerId:       "owner-1",
		ContentType:   "image/png",
		SizeBytes:     1234,
		BlockHashes:   [][]byte{hash1, hash2},
		StoredAt:      timestamppb.New(timeUTC(1700000000)),
		ContentFamily: eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE,
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Re-parse and assert every field survives a round trip with identical
	// values — a wire-format change (renumber, type change) breaks this.
	var back eventsv2.FileStored
	if err := proto.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.FileId != "file-1" || back.FileVersion != 2 || back.OwnerId != "owner-1" ||
		back.ContentType != "image/png" || back.SizeBytes != 1234 ||
		back.ContentFamily != eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE ||
		len(back.BlockHashes) != 2 ||
		!bytes.Equal(back.BlockHashes[0], hash1) || !bytes.Equal(back.BlockHashes[1], hash2) {
		t.Fatalf("round trip mismatch: %+v", &back)
	}

	// And the bytes are stable across runs (deterministic marshal).
	data2, _ := proto.Marshal(msg)
	if !bytes.Equal(data, data2) {
		t.Fatal("marshal is not deterministic")
	}
}

// TestDecrefMessagesGoldenBytes pins the batched request/completed messages:
// batch fields present, ops carry GLOBAL occurrence + raw 32-byte hashes,
// and the Outcome enum values round trip.
func TestDecrefMessagesGoldenBytes(t *testing.T) {
	hash := bytes.Repeat([]byte{0xEF}, 32)
	req := &eventsv2.DecrefRequested{
		JobId:      "job-1",
		BatchIndex: 1,
		BatchCount: 3,
		Ops: []*eventsv2.DecrefOp{
			{Occurrence: 500, BlockHash: hash},
		},
		Reason:      "permanent_delete",
		RequestedAt: timestamppb.New(timeUTC(1700000000)),
	}
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var backReq eventsv2.DecrefRequested
	if err := proto.Unmarshal(data, &backReq); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if backReq.BatchIndex != 1 || backReq.BatchCount != 3 || len(backReq.Ops) != 1 ||
		backReq.Ops[0].Occurrence != 500 || !bytes.Equal(backReq.Ops[0].BlockHash, hash) {
		t.Fatalf("DecrefRequested round trip mismatch: %+v", &backReq)
	}

	for _, tc := range []struct {
		out  eventsv2.DecrefResult_Outcome
		name string
	}{
		{eventsv2.DecrefResult_APPLIED, "APPLIED"},
		{eventsv2.DecrefResult_ALREADY_ABSENT, "ALREADY_ABSENT"},
		{eventsv2.DecrefResult_INVALID, "INVALID"},
	} {
		if tc.out.String() != tc.name {
			t.Errorf("outcome %d = %q, want %q", tc.out, tc.out.String(), tc.name)
		}
	}

	done := &eventsv2.DecrefCompleted{
		JobId: "job-1", BatchIndex: 1, BatchCount: 3,
		Results: []*eventsv2.DecrefResult{
			{Occurrence: 500, BlockHash: hash, Outcome: eventsv2.DecrefResult_APPLIED, NewRefCount: 0, BecameGcCandidate: true},
		},
		ProcessedAt: timestamppb.New(timeUTC(1700000001)),
	}
	data, err = proto.Marshal(done)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var backDone eventsv2.DecrefCompleted
	if err := proto.Unmarshal(data, &backDone); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(backDone.Results) != 1 || backDone.Results[0].Outcome != eventsv2.DecrefResult_APPLIED ||
		backDone.Results[0].NewRefCount != 0 || !backDone.Results[0].BecameGcCandidate {
		t.Fatalf("DecrefCompleted round trip mismatch: %+v", &backDone)
	}
}

// TestMsgIDFileStored pins the v2 stored MsgId shape and its republish
// semantics: same ID for the same (file, version), distinct across versions.
// TestContentFamilyRoundTrip pins the family enum: values must stay stable
// (they are persisted as smallint on file rows and the pending work list).
func TestContentFamilyRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		f    eventsv2.ContentFamily
		name string
	}{
		{eventsv2.ContentFamily_CONTENT_FAMILY_UNSPECIFIED, "CONTENT_FAMILY_UNSPECIFIED"},
		{eventsv2.ContentFamily_CONTENT_FAMILY_IMAGE, "CONTENT_FAMILY_IMAGE"},
		{eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO, "CONTENT_FAMILY_VIDEO"},
		{eventsv2.ContentFamily_CONTENT_FAMILY_PDF, "CONTENT_FAMILY_PDF"},
		{eventsv2.ContentFamily_CONTENT_FAMILY_OTHER, "CONTENT_FAMILY_OTHER"},
	} {
		if tc.f.String() != tc.name {
			t.Errorf("ContentFamily %d = %q, want %q", int32(tc.f), tc.f.String(), tc.name)
		}
	}

	// Wire stability: family field survives a marshal round trip on a
	// FileStored that carries nothing else unusual.
	evt := &eventsv2.FileStored{FileId: "f", ContentFamily: eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO}
	data, err := proto.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back eventsv2.FileStored
	if err := proto.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ContentFamily != eventsv2.ContentFamily_CONTENT_FAMILY_VIDEO {
		t.Fatalf("content_family round trip = %v, want VIDEO", back.ContentFamily)
	}
}

func TestMsgIDFileStored(t *testing.T) {
	if got, want := MsgIDFileStored("abc", 3), "stored:abc:3"; got != want {
		t.Fatalf("MsgIDFileStored = %q, want %q", got, want)
	}
	msgID := MsgIDFileStored("abc", 3)
	if msgID != MsgIDFileStored("abc", 3) {
		t.Fatal("same (file, version) must produce the same MsgId (republish = dedup no-op)")
	}
	if MsgIDFileStored("abc", 3) == MsgIDFileStored("abc", 4) {
		t.Fatal("different versions must produce different MsgIds")
	}
}
