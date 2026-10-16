"use client"

import { Code, ConnectError } from "@connectrpc/connect"

import { metadataCoreBrowserRpcClient, uploadBrowserRpcClient } from "@/lib/rpc"

/**
 * The save path for an in-browser edit: split the edited bytes, hash every
 * block, ask the file which blocks it already holds, upload only the rest, and
 * commit the new version.
 *
 * The three services involved keep their own responsibilities: the browser
 * never sees a block it does not need to send, the upload service owns block
 * writes and ref-counting (and verifies every block's hash), and the metadata
 * service owns the version number — a commit that loses the race is reported
 * as a conflict, never as an overwrite.
 */

/** How many blocks the save hashes or uploads at once. */
const PARALLEL_WORKERS = 4
/** Per-block upload attempts before the save gives up. */
const UPLOAD_ATTEMPTS = 3
/** Shrinks with the request size: a whole save has one shared budget. */
const RETRY_BASE_DELAY_MS = 250

export type EditSavePhase = "hashing" | "uploading" | "committing"

export type EditSaveProgress = {
  phase: EditSavePhase
  /** Blocks processed so far in the current phase. */
  completed: number
  total: number
  bytesUploaded: number
  /** Bytes of the new version that had to be sent (the rest were reused). */
  bytesToUpload: number
}

export type SaveEditedFileInput = {
  fileId: string
  /** The version the editor loaded; the commit is refused if the head moved. */
  baseVersion: number
  /** The size the file was written with — a save must re-split at exactly it. */
  blockSize: number
  filename: string
  /** Recorded on the version row, e.g. "extend-pdf-editor". */
  editor: string
  bytes: Uint8Array
  onProgress?: (progress: EditSaveProgress) => void
  signal?: AbortSignal
}

export type SaveEditedFileResult =
  | {
      status: "committed"
      version: number
      contentHash: string
      /** Blocks the file already held — the part of the save that was free. */
      blocksReused: number
      blocksTotal: number
      bytesUploaded: number
    }
  | {
      status: "conflict"
      headVersion: number
      headContentHash: string
    }

export class EditSaveAbortedError extends Error {
  constructor() {
    super("Save cancelled")
    this.name = "EditSaveAbortedError"
  }
}

/**
 * saveEditedFile splits, hashes, uploads and commits in one call so a caller
 * cannot accidentally commit a block list it did not send.
 */
export async function saveEditedFile(input: SaveEditedFileInput): Promise<SaveEditedFileResult> {
  const { fileId, baseVersion, blockSize, filename, editor, bytes, onProgress, signal } = input
  if (blockSize <= 0) throw new Error("The file has no block size to split at.")
  if (bytes.byteLength === 0) throw new Error("There is nothing to save.")

  const blocks = splitBlocks(bytes, blockSize)
  throwIfAborted(signal)
  onProgress?.({ phase: "hashing", completed: 0, total: blocks.length, bytesUploaded: 0, bytesToUpload: 0 })
  const hashes = await hashBlocks(blocks, signal, (completed) =>
    onProgress?.({
      phase: "hashing",
      completed,
      total: blocks.length,
      bytesUploaded: 0,
      bytesToUpload: 0,
    }),
  )
  throwIfAborted(signal)

  // The file's own blocks only: a hash nobody in this file references is never
  // reported as present, so the pre-flight cannot be used to probe whether some
  // other user's file shares a block.
  const prepared = await metadataCoreBrowserRpcClient.prepareCommit(
    {
      fileId,
      baseVersion,
      sizeBytes: BigInt(bytes.byteLength),
      blockSize: BigInt(blockSize),
      blockHashes: hashes,
    },
    { signal },
  )
  const known = new Set(prepared.alreadyReferencedHashes)
  const missing = blocks.map((block, index) => ({ block, index })).filter(({ index }) => !known.has(hashes[index]))
  const bytesToUpload = missing.reduce((total, { block }) => total + block.byteLength, 0)

  const commitId = crypto.randomUUID()
  let bytesUploaded = 0
  let uploaded = 0
  onProgress?.({
    phase: "uploading",
    completed: 0,
    total: missing.length,
    bytesUploaded,
    bytesToUpload,
  })
  await runWithWorkers(missing, PARALLEL_WORKERS, async ({ block, index }) => {
    await uploadBlockWithRetry({ fileId, index, hash: hashes[index], block, signal })
    bytesUploaded += block.byteLength
    uploaded += 1
    onProgress?.({
      phase: "uploading",
      completed: uploaded,
      total: missing.length,
      bytesUploaded,
      bytesToUpload,
    })
  })

  throwIfAborted(signal)
  onProgress?.({
    phase: "committing",
    completed: missing.length,
    total: missing.length,
    bytesUploaded,
    bytesToUpload,
  })
  const verdict = await uploadBrowserRpcClient.commitEdit(
    {
      fileId,
      baseVersion,
      commitId,
      sizeBytes: BigInt(bytes.byteLength),
      blockSize: BigInt(blockSize),
      blockHashes: hashes,
      blockSizes: blocks.map((block) => BigInt(block.byteLength)),
      editor,
      filename,
    },
    { signal },
  )

  if (verdict.conflict || !verdict.committed) {
    return {
      status: "conflict",
      headVersion: verdict.headVersion,
      headContentHash: verdict.headContentHash,
    }
  }
  return {
    status: "committed",
    version: verdict.version,
    contentHash: verdict.contentHash,
    blocksReused: Number(verdict.blocksReused),
    blocksTotal: blocks.length,
    bytesUploaded,
  }
}

/** splitBlocks cuts the bytes at exactly the file's block size: any other
 * split would give every block a different hash and re-send the whole file. */
export function splitBlocks(bytes: Uint8Array, blockSize: number): Uint8Array[] {
  const blocks: Uint8Array[] = []
  for (let offset = 0; offset < bytes.byteLength; offset += blockSize) {
    blocks.push(bytes.subarray(offset, Math.min(offset + blockSize, bytes.byteLength)))
  }
  return blocks
}

/** hashBlocks digests every block in parallel and reports progress in order. */
async function hashBlocks(
  blocks: Uint8Array[],
  signal: AbortSignal | undefined,
  onProgress: (completed: number) => void,
): Promise<string[]> {
  const hashes = new Array<string>(blocks.length)
  let completed = 0
  await runWithWorkers(blocks, PARALLEL_WORKERS, async (block, index) => {
    throwIfAborted(signal)
    const digest = await crypto.subtle.digest("SHA-256", toArrayBuffer(block))
    hashes[index] = toHex(new Uint8Array(digest))
    completed += 1
    onProgress(completed)
  })
  return hashes
}

async function uploadBlockWithRetry({
  fileId,
  index,
  hash,
  block,
  signal,
}: {
  fileId: string
  index: number
  hash: string
  block: Uint8Array
  signal?: AbortSignal
}): Promise<void> {
  for (let attempt = 0; ; attempt += 1) {
    throwIfAborted(signal)
    try {
      await uploadBrowserRpcClient.writeEditBlock(
        { fileId, index, sha256: hash, data: block },
        { signal },
      )
      return
    } catch (error) {
      if (attempt + 1 >= UPLOAD_ATTEMPTS || !isRetryable(error)) throw error
      // Exponential backoff with jitter: parallel workers retrying in lockstep
      // would hit the same overloaded node together.
      const delay = RETRY_BASE_DELAY_MS * 2 ** attempt * (0.5 + Math.random())
      await sleep(delay, signal)
    }
  }
}

/** isRetryable distinguishes a transient transport failure from a request that
 * will never succeed: only the former is worth another attempt with the same
 * bytes. */
function isRetryable(error: unknown): boolean {
  if (error instanceof EditSaveAbortedError) return false
  const code = ConnectError.from(error).code
  return (
    code === Code.Unavailable ||
    code === Code.Internal ||
    code === Code.DeadlineExceeded ||
    code === Code.ResourceExhausted ||
    code === Code.Unknown
  )
}

/** runWithWorkers runs one task per item, at most `limit` at a time, and
 * rejects with the first failure (cancelling nothing already in flight). */
async function runWithWorkers<T>(
  items: T[],
  limit: number,
  task: (item: T, index: number) => Promise<void>,
): Promise<void> {
  let next = 0
  let failure: unknown = null
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (failure === null) {
      const index = next
      next += 1
      if (index >= items.length) return
      try {
        await task(items[index], index)
      } catch (error) {
        failure = error ?? new Error("worker failed")
        return
      }
    }
  })
  await Promise.all(workers)
  if (failure !== null) throw failure
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw new EditSaveAbortedError()
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort)
      resolve()
    }, ms)
    function onAbort() {
      clearTimeout(timer)
      reject(new EditSaveAbortedError())
    }
    signal?.addEventListener("abort", onAbort, { once: true })
  })
}

/** toArrayBuffer copies a block view into its own buffer: crypto.subtle wants
 * a BufferSource, and a subarray's offset would otherwise be ignored. */
function toArrayBuffer(block: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(block.byteLength)
  copy.set(block)
  return copy.buffer
}

function toHex(bytes: Uint8Array): string {
  let hex = ""
  for (const byte of bytes) hex += byte.toString(16).padStart(2, "0")
  return hex
}
