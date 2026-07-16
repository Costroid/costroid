// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { ThemeSwitch } from "./ThemeSwitch";

function pressedStates() {
  return ["Device theme", "Dark theme", "Light theme"].map((label) =>
    screen.getByRole("button", { name: label }).getAttribute("aria-pressed"),
  );
}

describe("ThemeSwitch", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  afterEach(() => {
    cleanup();
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  it("renders the three options with device pressed by default", () => {
    render(<ThemeSwitch />);
    expect(screen.getByRole("group", { name: "Theme" })).toBeTruthy();
    expect(pressedStates()).toEqual(["true", "false", "false"]);
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });

  it("forcing dark sets html[data-theme], persists, and moves aria-pressed", () => {
    render(<ThemeSwitch />);
    fireEvent.click(screen.getByRole("button", { name: "Dark theme" }));
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(localStorage.getItem("costroid-theme")).toBe("dark");
    expect(pressedStates()).toEqual(["false", "true", "false"]);
  });

  it("returning to device removes the override and clears storage", () => {
    render(<ThemeSwitch />);
    fireEvent.click(screen.getByRole("button", { name: "Light theme" }));
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    fireEvent.click(screen.getByRole("button", { name: "Device theme" }));
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(localStorage.getItem("costroid-theme")).toBeNull();
    expect(pressedStates()).toEqual(["true", "false", "false"]);
  });

  it("initializes from a stored preference (the pre-paint script contract)", () => {
    localStorage.setItem("costroid-theme", "dark");
    render(<ThemeSwitch />);
    expect(pressedStates()).toEqual(["false", "true", "false"]);
    // The mount effect applies the attribute even when the inline script
    // didn't run (as in this jsdom render), so the two paths can't diverge.
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });

  it("ignores an invalid stored value", () => {
    localStorage.setItem("costroid-theme", "purple");
    render(<ThemeSwitch />);
    expect(pressedStates()).toEqual(["true", "false", "false"]);
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });

  it("keeps theme-color metas in sync with a forced theme", () => {
    const meta = document.createElement("meta");
    meta.name = "theme-color";
    meta.content = "#f6f7f9";
    document.head.appendChild(meta);
    try {
      render(<ThemeSwitch />);
      fireEvent.click(screen.getByRole("button", { name: "Dark theme" }));
      expect(meta.content).toBe("#0d1119");
      fireEvent.click(screen.getByRole("button", { name: "Device theme" }));
      expect(meta.content).toBe("#f6f7f9");
    } finally {
      meta.remove();
    }
  });
});
