import "server-only"

import { headers } from "next/headers"

async function getRequestOrigin() {
  const headerStore = await headers()
  const origin = headerStore.get("origin")
  const host = headerStore.get("host")

  if (!origin || !host) {
    throw new Error("CSRF validation failed")
  }

  const originHost = new URL(origin).host
  if (originHost !== host) {
    throw new Error("CSRF validation failed")
  }

  const fetchSite = headerStore.get("sec-fetch-site")
  if (fetchSite && fetchSite !== "same-origin" && fetchSite !== "same-site") {
    throw new Error("CSRF validation failed")
  }
}

export async function assertSameOriginRequest() {
  await getRequestOrigin()
}
