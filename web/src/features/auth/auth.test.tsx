import { act, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { createQueryClient } from "@/app/query-client";
import { passwordStrength } from "@/features/auth/strength";
import { DEV_PASSWORD, MIN_PASSWORD_LENGTH } from "@/lib/mock/auth";
import { renderApp } from "@/test/render";

const rail = () => screen.findByRole("navigation", { name: "Main" });

describe("first-run setup", () => {
  it("validates, then sets the password and enters the app", async () => {
    const { user, backend } = renderApp("/", { auth: "setup" });
    expect(await screen.findByRole("heading", { name: "Set a password for vigo" })).toBeInTheDocument();
    expect(screen.getByText(/no username/)).toBeInTheDocument();

    const password = screen.getByLabelText("Password", { selector: "input" });
    const confirm = screen.getByLabelText("Confirm password", { selector: "input" });
    const submit = screen.getByRole("button", { name: "Set password" });

    await user.type(password, "short");
    await user.click(submit);
    expect(screen.getByText(`Use at least ${MIN_PASSWORD_LENGTH} characters.`)).toBeInTheDocument();

    await user.clear(password);
    await user.type(password, "lantern-river-quiet");
    await user.type(confirm, "lantern-river-quite");
    await user.click(submit);
    expect(screen.getByText("The passwords don't match.")).toBeInTheDocument();
    expect(backend.auth.password).toBeNull();

    await user.click(screen.getByRole("button", { name: "Show password" }));
    expect(password).toHaveAttribute("type", "text");

    await user.clear(confirm);
    await user.type(confirm, "lantern-river-quiet");
    await user.click(submit);
    expect(await rail()).toBeInTheDocument();
    expect(backend.auth.password).toBe("lantern-river-quiet");
    expect(backend.auth.session).toBe(true);
  });

  it("gives a local strength hint", () => {
    expect(passwordStrength("abc", 10).label).toBe("Too short");
    expect(passwordStrength("aaaaaaaaaa", 10).score).toBe(1);
    expect(passwordStrength("Lantern-River-Quiet-7", 10).label).toBe("Strong");
  });
});

describe("login", () => {
  it("rejects a wrong password and accepts the right one", async () => {
    const { user, backend } = renderApp("/", { auth: "login" });
    const password = await screen.findByLabelText("Password", { selector: "input" });

    await user.type(password, "not-the-password");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("That password isn't right.")).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Main" })).not.toBeInTheDocument();

    await user.clear(password);
    await user.type(password, `${DEV_PASSWORD}{Enter}`);
    expect(await rail()).toBeInTheDocument();
    expect(backend.auth.session).toBe(true);
  });

  it("says when sign-in is rate limited", async () => {
    const { user, backend } = renderApp("/", { auth: "login" });
    backend.auth.rateLimited = true;
    await user.type(await screen.findByLabelText("Password", { selector: "input" }), `${DEV_PASSWORD}{Enter}`);
    expect(await screen.findByText(/Too many attempts\. Wait a moment/)).toBeInTheDocument();
  });

  it("makes no data requests before signing in", async () => {
    const { backend } = renderApp("/gallery", { auth: "login" });
    await screen.findByRole("button", { name: "Sign in" });
    await new Promise((r) => setTimeout(r, 50));
    expect(backend.calls).toEqual(["AuthService/GetAuthState"]);
    expect(backend.tv.refreshCount).toBe(0);
    expect(backend.library.requests).toHaveLength(0);
  });
});

describe("Security page", () => {
  it("has its own rail entry and isn't on the TV page", async () => {
    const { user } = renderApp("/tv");
    const nav = await rail();
    await screen.findByRole("heading", { name: "Art settings" });
    expect(screen.queryByRole("form", { name: "Change password" })).not.toBeInTheDocument();
    await user.click(within(nav).getByRole("link", { name: "Security" }));
    expect(await screen.findByRole("form", { name: "Change password" })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Security" })).toHaveAttribute("aria-current", "page");
  });
});

describe("session", () => {
  it("returns to login and clears data when the session ends mid-session", async () => {
    const qc = createQueryClient();
    const { user, backend } = renderApp("/gallery", {}, undefined, qc);
    await screen.findByRole("list", { name: "Artworks" });
    expect(qc.getQueryCache().getAll().length).toBeGreaterThan(0);

    act(() => backend.mock.expireSession());
    expect(await screen.findByText(/Your session ended/)).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Artworks" })).not.toBeInTheDocument();
    expect(qc.getQueryCache().getAll()).toHaveLength(0);

    const before = backend.library.requests.length;
    await user.type(screen.getByLabelText("Password", { selector: "input" }), `${DEV_PASSWORD}{Enter}`);
    expect(await screen.findByRole("list", { name: "Artworks" })).toBeInTheDocument();
    expect(backend.library.requests.length).toBeGreaterThan(before);
  });

  it("signs out from the rail", async () => {
    const qc = createQueryClient();
    const { user, backend } = renderApp("/tv", {}, undefined, qc);
    const nav = await rail();
    await user.click(within(nav).getByRole("button", { name: "Sign out" }));
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByText("Enter vigo's password to continue.")).toBeInTheDocument();
    expect(backend.auth.session).toBe(false);
    expect(qc.getQueryCache().getAll()).toHaveLength(0);
  });

  it("changes the password, explaining other devices are signed out", async () => {
    const { user, backend } = renderApp("/security");
    expect(await screen.findByRole("heading", { name: "Security", level: 1 })).toBeInTheDocument();
    const form = await screen.findByRole("form", { name: "Change password" });
    expect(form).toHaveTextContent("signs out every other device");
    const field = (name: string) => within(form).getByLabelText(name, { selector: "input" });

    await user.type(field("Current password"), "wrong-password");
    await user.type(field("New password"), "lantern-river-quiet");
    await user.type(field("Confirm new password"), "lantern-river-quiet");
    await user.click(within(form).getByRole("button", { name: "Change password" }));
    expect(await within(form).findByText("The current password isn't right.")).toBeInTheDocument();
    expect(backend.auth.password).toBe(DEV_PASSWORD);
    expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();

    await user.clear(field("Current password"));
    await user.type(field("Current password"), DEV_PASSWORD);
    await user.click(within(form).getByRole("button", { name: "Change password" }));
    expect(await screen.findByText("Password changed")).toBeInTheDocument();
    expect(backend.auth.password).toBe("lantern-river-quiet");
    expect(backend.auth.session).toBe(true);
    await waitFor(() => expect(field("Current password")).toHaveValue(""));
  });
});
