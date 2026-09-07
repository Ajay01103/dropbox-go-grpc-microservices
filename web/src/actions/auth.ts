"use server"

import { cookies } from "next/headers"

import { unauthenticatedAuthClient, getServerRpcClients } from "@/lib/rpc-server"
import { ACCESS_TOKEN_COOKIE_NAME, REFRESH_TOKEN_COOKIE_NAME } from "@/lib/auth-cookie"
import { assertSameOriginRequest } from "@/lib/csrf"

function setAuthCookies(
  cookieStore: Awaited<ReturnType<typeof cookies>>,
  accessToken: string,
  refreshToken: string,
) {
  const secure = process.env.NODE_ENV === "production"

  cookieStore.set(ACCESS_TOKEN_COOKIE_NAME, accessToken, {
    httpOnly: true,
    secure,
    sameSite: "strict",
    path: "/",
    maxAge: 15 * 60,
  })

  cookieStore.set(REFRESH_TOKEN_COOKIE_NAME, refreshToken, {
    httpOnly: true,
    secure,
    sameSite: "strict",
    path: "/",
    maxAge: 7 * 24 * 60 * 60,
  })
}

async function clearAuthCookies() {
  const cookieStore = await cookies()
  cookieStore.delete(ACCESS_TOKEN_COOKIE_NAME)
  cookieStore.delete(REFRESH_TOKEN_COOKIE_NAME)
}

export async function loginAction(value: { email: string; password: string }) {
  await assertSameOriginRequest()

  const response = await unauthenticatedAuthClient.login(value)
  const cookieStore = await cookies()
  setAuthCookies(cookieStore, response.accessToken, response.refreshToken)

  return {
    userId: response.userId,
    name: response.name,
    email: response.email,
  }
}

export async function registerAction(value: { name: string; email: string; password: string }) {
  await assertSameOriginRequest()

  const response = await unauthenticatedAuthClient.register(value)
  const cookieStore = await cookies()
  setAuthCookies(cookieStore, response.accessToken, response.refreshToken)

  return {
    userId: response.userId,
    name: response.name,
    email: response.email,
  }
}

export async function getCurrentUserAction() {
  const { authClient } = await getServerRpcClients()

  try {
    const response = await authClient.getCurrentUser({})
    return {
      userId: response.userId,
      email: response.email,
      name: response.name,
    }
  } catch {
    return null
  }
}

export async function logoutAction() {
  await assertSameOriginRequest()

  const cookieStore = await cookies()
  const refreshToken = cookieStore.get(REFRESH_TOKEN_COOKIE_NAME)?.value

  if (refreshToken) {
    try {
      await unauthenticatedAuthClient.logout({ refreshToken })
    } catch (error) {
      console.error("RPC logout failed", error)
    }
  }

  await clearAuthCookies()
}
