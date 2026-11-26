import { useQuery } from "@tanstack/react-query";
import { System } from "../../../shared/api";

export function DashboardPage() {
  const info = useQuery({ queryKey: ["system", "info"], queryFn: () => System.info() });

  return (
    <main style={{ padding: 32 }}>
      <h1>VPSGoGo</h1>
      {info.data && (
        <p>
          v{info.data.version} · {info.data.os}/{info.data.arch}
        </p>
      )}
    </main>
  );
}
