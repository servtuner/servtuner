import "@mantine/core/styles.css";
import { Alert, MantineProvider } from "@mantine/core";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./app/App";
import { loadRuntimeConfig } from "./app/config";
import { theme } from "./app/theme";

const rootElement = document.getElementById("root");
if (rootElement === null) throw new Error("index.html has no #root element");
const root = createRoot(rootElement);

loadRuntimeConfig().then(
  (config) =>
    root.render(
      <StrictMode>
        <App config={config} />
      </StrictMode>,
    ),
  (error: unknown) =>
    root.render(
      <MantineProvider theme={theme}>
        <Alert color="red" title="servtuner could not start" m="md">
          {error instanceof Error ? error.message : String(error)}
        </Alert>
      </MantineProvider>,
    ),
);
