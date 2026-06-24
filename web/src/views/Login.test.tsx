import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Login } from "./Login";
import { ApiError } from "@/api/client";

const login = vi.fn();
const navigate = vi.fn();

vi.mock("@/auth/SessionContext", () => ({
  useSession: () => ({ login }),
}));

vi.mock("react-router-dom", async (orig) => {
  const actual = await orig<typeof import("react-router-dom")>();
  return {
    ...actual,
    useNavigate: () => navigate,
    useLocation: () => ({ state: null }),
  };
});

function renderLogin() {
  render(
    <MemoryRouter>
      <Login />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("Login", () => {
  it("submit is disabled until a password is typed", () => {
    renderLogin();
    const submit = screen.getByRole("button", { name: /sign in/i });
    expect(submit).toBeDisabled();

    fireEvent.change(screen.getByLabelText(/admin password/i), {
      target: { value: "hunter2" },
    });
    expect(submit).toBeEnabled();
  });

  it("logs in and navigates to the requested destination", async () => {
    login.mockResolvedValue(undefined);
    renderLogin();

    fireEvent.change(screen.getByLabelText(/admin password/i), {
      target: { value: "hunter2" },
    });
    fireEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await waitFor(() => expect(login).toHaveBeenCalledWith("hunter2"));
    expect(navigate).toHaveBeenCalledWith("/", { replace: true });
  });

  it("shows a wrong-password message and clears the field", async () => {
    login.mockRejectedValue(new ApiError(401, { code: "auth", message: "nope" }));
    renderLogin();

    const input = screen.getByLabelText(/admin password/i) as HTMLInputElement;
    fireEvent.change(input, { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: /sign in/i }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/password is not correct/i);
    expect(input.value).toBe("");
    expect(navigate).not.toHaveBeenCalled();
    // The invalid field points at the error so AT reads it in context.
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAttribute("aria-describedby", alert.id);
  });

  it("surfaces a lockout but lets the user retype and retry (no dead-end)", async () => {
    login.mockRejectedValue(
      new ApiError(429, { code: "locked", message: "Locked out for a while." }),
    );
    renderLogin();

    fireEvent.change(screen.getByLabelText(/admin password/i), {
      target: { value: "wrong" },
    });
    fireEvent.click(screen.getByRole("button", { name: /sign in/i }));

    // The lockout is surfaced as a (warn-tone) alert and the field is cleared…
    expect(await screen.findByRole("alert")).toHaveTextContent(/locked out/i);
    const input = screen.getByLabelText(/admin password/i) as HTMLInputElement;
    expect(input.value).toBe("");

    // …but the form is NOT a dead-end: the input stays editable so the user can
    // retype once the server-side lock expires (the server remains the gate).
    expect(input).toBeEnabled();
    expect(login).toHaveBeenCalledTimes(1);

    fireEvent.change(input, { target: { value: "correct-horse" } });
    expect(screen.getByRole("button", { name: /sign in/i })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: /sign in/i }));

    // Retyping + resubmitting fires another auth attempt — recovery without reload.
    await waitFor(() => expect(login).toHaveBeenCalledTimes(2));
    expect(login).toHaveBeenLastCalledWith("correct-horse");
  });
});
