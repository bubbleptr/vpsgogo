import type * as Real from "../../mygo";

type Fn = (...args: never[]) => unknown;
type MockOf<S> = { [K in keyof S]: S[K] extends Fn ? (...args: Parameters<S[K]>) => ReturnType<S[K]> : never };

export const System: MockOf<typeof Real.System> = {
  info: async () => ({ version: "0.0.0-mock", os: "darwin", arch: "arm64", goVersion: "go-mock" }),
};
