package server

import (
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

// chunkEnvelopeSlack is headroom above one block for the per-request protobuf
// envelope: upload id, hex sha256, chunk index, plus framing varints.
const chunkEnvelopeSlack = 1 << 20

// connectDefaultReadMaxBytes is connect's own inbound message cap, applied when
// Mount is not given a larger one.
const connectDefaultReadMaxBytes = 4 << 20

// ReadMaxBytes is the inbound message-size cap for the upload RPCs.
//
// UploadChunk carries one whole block per message, so the cap has to track the
// block size the client is handed. connect's 4 MiB default is below the
// configured block size, which made every full-size chunk fail with
// resource_exhausted ("message size … is larger than configured max 4194304")
// before the service ever saw it. The floor keeps a missing or garbage chunk
// size from producing a cap below connect's own default.
func ReadMaxBytes(chunkSizeBytes int64) int {
	if limit := int(chunkSizeBytes) + chunkEnvelopeSlack; limit > connectDefaultReadMaxBytes {
		return limit
	}
	return connectDefaultReadMaxBytes
}

// Mount mounts the upload Connect server on mux with the message-size cap
// derived from chunkSizeBytes. The service and its tests both mount through
// here, so a cap that breaks real uploads cannot pass the tests unnoticed.
func Mount(mux *http.ServeMux, srv *connect.Server, chunkSizeBytes int64) {
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(ReadMaxBytes(chunkSizeBytes)))
}
