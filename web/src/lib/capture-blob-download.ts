"use client"

/**
 * captureBlobDownload runs an export that ends in a browser download and
 * returns the bytes instead.
 *
 * The Extend DOCX and XLSX editors expose no byte-serializing API: their save
 * path builds a Blob (or a Blob URL) and clicks an `<a download>`. Intercepting
 * that single step is how an edited document reaches our own commit flow
 * without forking the editor's serializer — the bytes are exactly the ones the
 * editor would have handed the user.
 *
 * The patch is installed for the duration of the call and removed in a
 * `finally`, so no global is left modified. Only clicks on the link the export
 * itself just created are swallowed: every other click is passed through, and
 * the browser never saves a stray second copy next to the committed version.
 */
export async function captureBlobDownload(
  trigger: () => void,
  timeoutMs = 60_000,
): Promise<Uint8Array> {
  const originalCreateObjectURL = URL.createObjectURL
  const originalClick = HTMLAnchorElement.prototype.click
  const exportUrls = new Set<string>()
  let settle: ((bytes: Uint8Array) => void) | null = null
  let fail: ((error: Error) => void) | null = null
  const bytes = new Promise<Uint8Array>((resolve, reject) => {
    settle = resolve
    fail = reject
  })

  URL.createObjectURL = function patchedCreateObjectURL(this: void, target: Blob | MediaSource): string {
    const url = originalCreateObjectURL.call(URL, target)
    if (target instanceof Blob && target.size > 0) {
      exportUrls.add(url)
      void target.arrayBuffer().then(
        (buffer) => settle?.(new Uint8Array(buffer)),
        (error: unknown) => fail?.(error instanceof Error ? error : new Error(String(error))),
      )
    }
    return url
  }

  HTMLAnchorElement.prototype.click = function patchedClick(this: HTMLAnchorElement): void {
    if (this.download && exportUrls.has(this.href)) return
    originalClick.call(this)
  }

  let timer: ReturnType<typeof setTimeout> | undefined
  const timedOut = new Promise<never>((_, reject) => {
    timer = setTimeout(
      () => reject(new Error("The editor did not produce a file to save.")),
      timeoutMs,
    )
  })

  try {
    trigger()
    return await Promise.race([bytes, timedOut])
  } finally {
    if (timer !== undefined) clearTimeout(timer)
    URL.createObjectURL = originalCreateObjectURL
    HTMLAnchorElement.prototype.click = originalClick
  }
}
