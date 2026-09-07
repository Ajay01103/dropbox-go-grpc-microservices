"use client"

import { create } from "@bufbuild/protobuf"
import type { Client } from "@connectrpc/connect"
import {
  BasePlugin,
  type Body,
  type Meta,
  type PluginOpts,
  type Uppy,
  type UppyFile,
} from "@uppy/core"
import type { UploadService } from "@/gen/pb/upload/upload_pb"
import { InitUploadRequestSchema, UploadChunkRequestSchema } from "@/gen/pb/upload/upload_pb"

type UploadClient = Client<typeof UploadService>

interface ConnectRPCUploaderOptions extends PluginOpts {
  client: UploadClient
  userId?: string
}

function toHex(bytes: Uint8Array) {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")
}

async function sha256(data: Uint8Array) {
  const digest = await crypto.subtle.digest("SHA-256", data.buffer as ArrayBuffer)
  return toHex(new Uint8Array(digest))
}

export class ConnectRPCUploader extends BasePlugin<ConnectRPCUploaderOptions, Meta, Body> {
  static VERSION = "1.0.0"
  type = "uploader"

  constructor(uppy: Uppy<Meta, Body>, opts: ConnectRPCUploaderOptions) {
    super(uppy, opts)
    this.id = opts.id ?? "ConnectRPCUploader"
  }

  install() {
    console.info("[files] ConnectRPCUploader installed")
    this.uppy.addUploader(this.upload)
  }

  uninstall() {
    this.uppy.removeUploader(this.upload)
  }

  private upload = async (fileIDs: string[]) => {
    console.info("[files] Uppy uploader invoked", { fileIDs })
    await Promise.all(fileIDs.map((fileID) => this.uploadFile(fileID)))
  }

  private async uploadFile(fileID: string) {
    const file = this.uppy.getFile(fileID)
    if (!file) return

    try {
      const data = file.data as Blob
      if (!(data instanceof Blob)) {
        throw new Error(`File data is unavailable for ${file.name}`)
      }
      console.info("[files] initializing upload", { fileID, filename: file.name, size: data.size })
      const initialized = await this.opts.client.initUpload(
        create(InitUploadRequestSchema, {
          filename: file.name,
          totalSizeBytes: BigInt(data.size),
          contentType: file.type ?? "application/octet-stream",
        }),
      )

      if (initialized.alreadyComplete) {
        this.markComplete(file, initialized.objectId)
        return
      }

      const chunkSize = Number(initialized.chunkSizeBytes)
      if (!Number.isSafeInteger(chunkSize) || chunkSize <= 0) {
        throw new Error("Upload service returned an invalid chunk size")
      }

      const receivedChunkIndices = new Set(initialized.alreadyReceivedChunkIndices.map(Number))
      let uploaded = 0
      for (let offset = 0; offset < data.size; offset += chunkSize) {
        const chunkIndex = Math.floor(offset / chunkSize)
        if (receivedChunkIndices.has(chunkIndex)) {
          uploaded = Math.max(uploaded, Math.min(offset + chunkSize, data.size))
          continue
        }
        const bytes = new Uint8Array(await data.slice(offset, offset + chunkSize).arrayBuffer())
        const ack = await this.opts.client.uploadChunk(
          create(UploadChunkRequestSchema, {
            uploadId: initialized.uploadId,
            offset: BigInt(offset),
            data: bytes,
            sha256OfChunk: await sha256(bytes),
            chunkIndex,
          }),
        )
        console.info("[files] chunk acknowledged", {
          fileID,
          offset,
          offsetPersisted: ack.offsetPersisted.toString(),
          isFinal: ack.isFinal,
        })
        uploaded = Number(ack.offsetPersisted)
        this.uppy.emit("upload-progress", file, {
          uploadStarted: file.progress.uploadStarted ?? Date.now(),
          bytesUploaded: uploaded,
          bytesTotal: data.size,
          percentage: Math.min(100, Math.round((uploaded / data.size) * 100)),
        })
      }

      if (uploaded < data.size) {
        throw new Error("Upload stream ended before the file was complete")
      }
      this.markComplete(file, initialized.uploadId)
    } catch (error) {
      const message = error instanceof Error ? error.message : "Upload failed"
      this.uppy.setFileState(file.id, { error: message })
      this.uppy.emit("upload-error", file, { name: "UploadError", message })
      throw error
    }
  }

  private markComplete(file: UppyFile<Meta, Body>, uploadId: string) {
    const data = file.data as Blob
    const size = data.size
    this.uppy.setFileState(file.id, {
      progress: {
        ...file.progress,
        uploadStarted: file.progress.uploadStarted ?? Date.now(),
        uploadComplete: true,
        complete: true,
        bytesUploaded: size,
        bytesTotal: size,
        percentage: 100,
      },
      response: { status: 200, body: { uploadId } },
    })
    this.uppy.emit("upload-success", file, { status: 200, body: { uploadId } })
  }
}
