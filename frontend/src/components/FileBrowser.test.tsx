import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import type { CloudFile, MigrationConfig } from "../types";
import { apiFetch } from "../utils/apiClient";
import { FileBrowser } from "./FileBrowser";

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("../utils/apiClient", async () => {
  const actual = await vi.importActual<typeof import("../utils/apiClient")>("../utils/apiClient");
  return { ...actual, apiFetch: vi.fn() };
});

const credentials: MigrationConfig = {
  source_url: "https://source.example.test",
  source_username: "source-user",
  source_password: "source-password",
  source_refresh_token: "",
  source_token_expires_in: 0,
  target_url: "https://target.example.test",
  target_username: "target-user",
  target_password: "target-password",
  target_refresh_token: "",
  target_token_expires_in: 0,
  source_provider: "nextcloud",
  target_provider: "nextcloud",
};

const initialFiles: CloudFile[] = [{
  path: "/Documents",
  name: "Documents",
  size: 0,
  is_dir: true,
  hash: "",
  last_modified: "2026-01-01T00:00:00Z",
}];

const jsonResponse = (data: unknown, status = 200): Response => new Response(
  JSON.stringify(data),
  { status, headers: { "Content-Type": "application/json" } },
);

const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe("FileBrowser sync start retry", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage("en");
    vi.mocked(apiFetch).mockReset();
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  it("retries the first pass for an already-created sync without creating another job", async () => {
    let startRequests = 0;
    vi.mocked(apiFetch).mockImplementation((url) => {
      const path = String(url);
      if (path.endsWith("/api/sync")) {
        return Promise.resolve(jsonResponse({ id: "sync-1", success: true }));
      }
      if (path.endsWith("/api/sync/sync-1/start")) {
        startRequests += 1;
        return Promise.resolve(
          startRequests === 1 ? jsonResponse({}, 503) : jsonResponse({ success: true }),
        );
      }
      return Promise.resolve(jsonResponse({ success: true, items: [] }));
    });
    const onStartSuccess = vi.fn();

    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(
        <FileBrowser
          initialFiles={initialFiles}
          credentials={credentials}
          apiUrl="https://api.example.test"
          onBack={vi.fn()}
          onStartSuccess={onStartSuccess}
          token="token"
        />,
      );
      await flush();
    });

    const buttonWithText = (text: string) => Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.trim() === text) as HTMLButtonElement;

    await act(async () => {
      buttonWithText("Continuous Sync").click();
    });

    await act(async () => {
      buttonWithText("Start transfer").click();
      await flush();
    });

    expect(vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith("/api/sync"))).toHaveLength(1);
    expect(vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith("/api/sync/sync-1/start"))).toHaveLength(1);
    expect(onStartSuccess).not.toHaveBeenCalled();

    await act(async () => {
      buttonWithText("Start transfer").click();
      await flush();
    });

    expect(vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith("/api/sync"))).toHaveLength(1);
    expect(vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith("/api/sync/sync-1/start"))).toHaveLength(2);
    expect(onStartSuccess).toHaveBeenCalledWith("sync-1", true);
  });

  it("creates a new sync after its configuration changes following a failed start", async () => {
    let createRequests = 0;
    vi.mocked(apiFetch).mockImplementation((url) => {
      const path = String(url);
      if (path.endsWith("/api/sync")) {
        createRequests += 1;
        return Promise.resolve(jsonResponse({ id: `sync-${createRequests}`, success: true }));
      }
      if (path.endsWith("/api/sync/sync-1/start")) {
        return Promise.resolve(jsonResponse({}, 503));
      }
      if (path.endsWith("/api/sync/sync-2/start")) {
        return Promise.resolve(jsonResponse({ success: true }));
      }
      return Promise.resolve(jsonResponse({ success: true, items: [] }));
    });
    const onStartSuccess = vi.fn();

    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(
        <FileBrowser
          initialFiles={initialFiles}
          credentials={credentials}
          apiUrl="https://api.example.test"
          onBack={vi.fn()}
          onStartSuccess={onStartSuccess}
          token="token"
        />,
      );
      await flush();
    });

    const buttonWithText = (text: string) => Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.trim() === text) as HTMLButtonElement;

    await act(async () => {
      buttonWithText("Continuous Sync").click();
    });
    await act(async () => {
      buttonWithText("Start transfer").click();
      await flush();
    });

    const interval = container.querySelector("select") as HTMLSelectElement;
    await act(async () => {
      interval.value = "30";
      interval.dispatchEvent(new Event("change", { bubbles: true }));
      await flush();
    });
    await act(async () => {
      buttonWithText("Start transfer").click();
      await flush();
    });

    expect(vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith("/api/sync"))).toHaveLength(2);
    expect(vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith("/api/sync/sync-1/start"))).toHaveLength(1);
    expect(vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith("/api/sync/sync-2/start"))).toHaveLength(1);
    expect(onStartSuccess).toHaveBeenCalledWith("sync-2", true);
  });

  it("does not offer calendar or contact selections for continuous sync", async () => {
    vi.mocked(apiFetch).mockResolvedValue(jsonResponse({ success: true, items: [] }));

    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(
        <FileBrowser
          initialFiles={initialFiles}
          credentials={credentials}
          apiUrl="https://api.example.test"
          onBack={vi.fn()}
          onStartSuccess={vi.fn()}
          token="token"
        />,
      );
      await flush();
    });

    const syncButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.trim() === "Continuous Sync") as HTMLButtonElement;
    await act(async () => {
      syncButton.click();
    });

    expect(container.querySelector("#calendars-tab")).toBeNull();
    expect(container.querySelector("#contacts-tab")).toBeNull();
  });
});
