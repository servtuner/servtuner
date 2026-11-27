import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { createApiClient } from "../api/client";
import { ApiClientContext } from "../api/hooks";
import { routes } from "../app/routes";
import { theme } from "../app/theme";
import { TEST_API_BASE_URL } from "./server";

export function renderApp(initialPath: string, basename = "/") {
  const router = createMemoryRouter(routes, { initialEntries: [initialPath], basename });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const result = render(
    <MantineProvider theme={theme}>
      <ApiClientContext.Provider value={createApiClient(TEST_API_BASE_URL)}>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </ApiClientContext.Provider>
    </MantineProvider>,
  );
  return { ...result, router };
}
