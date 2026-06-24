import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// Proves the shadcn scaffold is wired: the `@/` alias resolves to src/, the
// components render, and cn() composes classes. Guards against the components
// drifting back into an unresolvable path.
describe("shadcn scaffold", () => {
  it("renders a Button via the @/ alias", () => {
    render(<Button>Provision</Button>);
    expect(screen.getByRole("button", { name: "Provision" })).toBeInTheDocument();
  });

  it("renders a Badge", () => {
    render(<Badge>ok</Badge>);
    expect(screen.getByText("ok")).toBeInTheDocument();
  });

  it("cn() merges and dedupes classes", () => {
    expect(cn("px-2", "px-4")).toBe("px-4");
  });
});
