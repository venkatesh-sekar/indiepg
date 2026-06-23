// Vitest setup: jest-dom matchers + DOM cleanup between tests.
// Imported via `test.setupFiles` in vite.config.ts.
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => {
  cleanup();
});
