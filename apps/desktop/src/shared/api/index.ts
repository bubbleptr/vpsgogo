// The only entry to the Go side. In `mock` and `test` modes vite.config.ts
// swaps src/mygo.ts for ./mygo.mock.ts, so pages run in a plain browser.
export * from "../../mygo";
