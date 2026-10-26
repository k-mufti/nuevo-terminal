import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig({
  plugins: [solid()],
  server: {
    port: 5173,
    proxy: {
      // The Go server runs on :8080 during development.
      "/api": { target: "http://localhost:8080", changeOrigin: true },
    },
  },
  build: { target: "es2022" },
});
