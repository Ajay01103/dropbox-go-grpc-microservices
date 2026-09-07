import { QueryClient, defaultShouldDehydrateQuery, environmentManager } from "@tanstack/react-query"
import { cache } from "react"

function makeQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 60 * 1000,
        gcTime: 5 * 60 * 1000,
        retry: 1,
        refetchOnWindowFocus: false,
      },
      mutations: { throwOnError: false, retry: 0 },
      dehydrate: {
        shouldDehydrateQuery: (query) =>
          defaultShouldDehydrateQuery(query) || query.state.status === "pending",
      },
    },
  })
}

let browserQueryClient: QueryClient | undefined

// cache() is request-scoped in RSC — this gives every server component
// in the SAME request the same client, without leaking across requests.
const getServerQueryClient = cache(makeQueryClient)

export function getQueryClient() {
  if (environmentManager.isServer()) return getServerQueryClient()
  if (!browserQueryClient) browserQueryClient = makeQueryClient()
  return browserQueryClient
}
