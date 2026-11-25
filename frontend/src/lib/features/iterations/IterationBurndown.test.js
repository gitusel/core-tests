import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import IterationBurndown from "./IterationBurndown.svelte";

beforeEach(() => {
	vi.stubGlobal(
		"ResizeObserver",
		class {
			observe() {}
			unobserve() {}
			disconnect() {}
		},
	);
});
afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
});

it("switches remaining and ideal chart values between items and fractional story points", async () => {
	render(IterationBurndown, {
		data: {
			total_items: 4,
			data_points: [
				{
					date: "2026-07-01",
					remaining: 4,
					completed: 0,
					ideal: 4,
					remaining_points: 10.5,
					completed_points: 0,
					ideal_points: 10.5,
				},
				{
					date: "2026-07-02",
					remaining: 3,
					completed: 1,
					ideal: 2,
					remaining_points: 8,
					completed_points: 2.5,
					ideal_points: 5.25,
				},
			],
		},
	});
	// The second data point is category index 1 in both series. Asserting on the
	// stable point testids keeps the value claim independent of the chart's
	// locale/timezone-rendered category label.
	const pointValues = (key) => screen.getByTestId(`chart-point-${key}-1`).getAttribute("aria-label");

	const selector = screen.getByTestId("iteration-burndown-metric");
	expect(selector).toHaveValue("items");
	expect(pointValues("remaining")).toMatch(/: 3$/);
	expect(pointValues("ideal")).toMatch(/: 2$/);

	await fireEvent.change(selector, { target: { value: "points" } });
	expect(pointValues("remaining")).toMatch(/: 8$/);
	expect(pointValues("ideal")).toMatch(/: 5\.25$/);

	await fireEvent.change(selector, { target: { value: "items" } });
	expect(pointValues("remaining")).toMatch(/: 3$/);
	expect(pointValues("ideal")).toMatch(/: 2$/);
});

it("hides the chart when fewer than two days are available", () => {
	render(IterationBurndown, { data: { data_points: [] } });
	expect(
		screen.queryByTestId("iteration-burndown-metric"),
	).not.toBeInTheDocument();
});
