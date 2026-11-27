import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { createBrowserRouter, RouterProvider } from "react-router";
import { createApiClient } from "../api/client";
import { ApiClientContext } from "../api/hooks";
import { routerBasename } from "./basePath";
import type { RuntimeConfig } from "./config";
import { routes } from "./routes";
import { theme } from "./theme";

export function App({ config }: { config: RuntimeConfig }) {
  const [apiClient] = useState(() => createApiClient(config.apiBaseUrl));
  const [queryClient] = useState(() => new QueryClient());
  const [router] = useState(() => createBrowserRouter(routes, { basename: routerBasename() }));
  return (
    <MantineProvider theme={theme} defaultColorScheme="auto">
      <ApiClientContext.Provider value={apiClient}>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </ApiClientContext.Provider>
    </MantineProvider>
  );
}
