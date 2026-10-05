import { QueryClient, queryOptions } from "@tanstack/react-query";
import { api, ApiError, type SystemStatus, type User } from "./api";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
});

export const statusQuery = queryOptions({
  queryKey: ["system", "status"],
  queryFn: () => api<SystemStatus>("GET", "/system/status"),
});

/** Resolves to the signed-in user, or null when not signed in. */
export const meQuery = queryOptions({
  queryKey: ["auth", "me"],
  queryFn: async () => {
    try {
      return await api<User>("GET", "/auth/me");
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return null;
      throw e;
    }
  },
});
