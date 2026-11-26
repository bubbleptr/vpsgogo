import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { DashboardPage } from "./DashboardPage";

test("shows app info from the Go side", async () => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <DashboardPage />
    </QueryClientProvider>,
  );
  expect(await screen.findByText(/v0\.0\.0-mock · darwin\/arm64/)).toBeTruthy();
});
