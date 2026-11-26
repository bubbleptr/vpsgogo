/// <reference types="vitest/config" />
import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";

const generated = fileURLToPath(new URL("./src/mygo.ts", import.meta.url));
const mock = fileURLToPath(new URL("./src/shared/api/mygo.mock.ts", import.meta.url));

// Swaps the generated Go client for a mock, so the UI runs in a browser
// (`vite --mode mock`) and in tests without a MyGo window.
function mockGo(): Plugin {
  return {
    name: "vpsgogo:mock-go",
    enforce: "pre",
    async resolveId(source, importer, options) {
      const resolved = await this.resolve(source, importer, { ...options, skipSelf: true });
      return resolved?.id === generated ? mock : null;
    },
  };
}

export default defineConfig(({ mode }) => ({
  plugins: [react(), ...(mode === "mock" || mode === "test" ? [mockGo()] : [])],
  server: {
    // The app loads this address during `mygo dev` (devUrl in mygo.config.ts).
    port: 5173,
    strictPort: true,
    watch: { ignored: ["**/.mygo/**", "**/build/**"] },
  },
  test: { environment: "jsdom" },
}));
