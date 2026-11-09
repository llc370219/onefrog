import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Backend address for `npm run dev`; scripts/dev.sh sets OF_DEV_API_PORT when 8080 is taken.
const api = `http://127.0.0.1:${process.env.OF_DEV_API_PORT || 8080}`;

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    target: "es2022",
    // React rarely changes between releases; keeping it in its own chunk lets
    // browsers reuse the cached copy after an upgrade.
    rollupOptions: {
      input: { panel: "index.html", frogBot: "frog-bot.html" },
      output: {
        manualChunks(id) {
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return "react";
          if (/[\\/]node_modules[\\/]lucide-react[\\/]/.test(id)) return "icons";
        },
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": api,
      "/ws": { target: api, ws: true },
    },
  },
});
