import { Badge, createTheme } from "@mantine/core";

// The one theme definition. Mantine derives light and dark from it.
export const theme = createTheme({
  primaryColor: "indigo",
  defaultRadius: "sm",
  fontFamily: "system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif",
  components: {
    // Badges are statuses read next to numbers: sentence case, not shouted.
    Badge: Badge.extend({ styles: { label: { textTransform: "none" } } }),
  },
});
