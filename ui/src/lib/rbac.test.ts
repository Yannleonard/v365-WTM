// ui/src/lib/rbac.test.ts
import { describe, it, expect } from "vitest";
import {
  can,
  canAny,
  hasCap,
  isReadOnly,
  gateWorkloadAction,
  gateStats,
  gateExec,
  gateLogs,
  gateVMNetworkWrite,
  gateVMStorageWrite,
  gateVMConsole,
  gateVMBackup,
} from "./rbac";
import type { Capability, VMCapability } from "./types";

const DOCKER_CAPS: Capability[] = [
  "list",
  "inspect",
  "logs",
  "stats",
  "start",
  "stop",
  "restart",
  "remove",
  "exec",
  "events",
  "images",
  "networks",
  "volumes",
];
const SWARM_CAPS: Capability[] = ["list", "inspect", "logs", "stats", "readonly"];
const KUBE_CAPS: Capability[] = ["list", "inspect", "logs", "readonly"];

describe("can()", () => {
  it("grants everything to superuser", () => {
    expect(can(["*"], "docker.container.remove")).toBe(true);
    expect(can(["*"], "anything.at.all")).toBe(true);
  });
  it("matches exact permission", () => {
    expect(can(["docker.container.start"], "docker.container.start")).toBe(true);
    expect(can(["docker.container.start"], "docker.container.stop")).toBe(false);
  });
  it("returns false for empty/undefined", () => {
    expect(can([], "audit.read")).toBe(false);
    expect(can(undefined, "audit.read")).toBe(false);
  });
  it("supports domain wildcards defensively", () => {
    expect(can(["docker.container.*"], "docker.container.exec")).toBe(true);
  });
});

describe("canAny()", () => {
  it("is true when any perm matches", () => {
    expect(canAny(["audit.read"], ["rbac.user.read", "audit.read"])).toBe(true);
    expect(canAny(["x"], ["a", "b"])).toBe(false);
  });
});

describe("capability helpers", () => {
  it("hasCap / isReadOnly", () => {
    expect(hasCap(DOCKER_CAPS, "exec")).toBe(true);
    expect(hasCap(SWARM_CAPS, "exec")).toBe(false);
    expect(isReadOnly(SWARM_CAPS)).toBe(true);
    expect(isReadOnly(DOCKER_CAPS)).toBe(false);
  });
});

describe("gateWorkloadAction()", () => {
  it("allows docker stop with cap + perm", () => {
    const r = gateWorkloadAction("stop", "docker", DOCKER_CAPS, ["docker.container.stop"]);
    expect(r.allowed).toBe(true);
  });
  it("blocks docker stop without perm, with reason", () => {
    const r = gateWorkloadAction("stop", "docker", DOCKER_CAPS, []);
    expect(r.allowed).toBe(false);
    expect(r.reason).toContain("docker.container.stop");
  });
  it("blocks all lifecycle on swarm (read-only)", () => {
    const r = gateWorkloadAction("start", "swarm", SWARM_CAPS, ["*"]);
    expect(r.allowed).toBe(false);
    expect(r.reason.toLowerCase()).toContain("read-only");
  });
  it("blocks all lifecycle on kubernetes (read-only)", () => {
    const r = gateWorkloadAction("remove", "kubernetes", KUBE_CAPS, ["*"]);
    expect(r.allowed).toBe(false);
  });
});

describe("gateStats / gateExec / gateLogs", () => {
  it("hides stats on kubernetes (no CapStats)", () => {
    expect(gateStats(KUBE_CAPS, ["*"]).allowed).toBe(false);
  });
  it("allows stats on docker with perm", () => {
    expect(gateStats(DOCKER_CAPS, ["docker.container.stats"]).allowed).toBe(true);
  });
  it("blocks exec on read-only providers", () => {
    expect(gateExec(SWARM_CAPS, ["*"]).allowed).toBe(false);
    expect(gateExec(KUBE_CAPS, ["*"]).allowed).toBe(false);
  });
  it("allows logs on docker/swarm/k8s with perm + cap", () => {
    expect(gateLogs(DOCKER_CAPS, ["docker.container.logs"]).allowed).toBe(true);
    expect(gateLogs(SWARM_CAPS, ["docker.container.logs"]).allowed).toBe(true);
    expect(gateLogs(KUBE_CAPS, ["docker.container.logs"]).allowed).toBe(true);
  });
});

describe("VM infrastructure write gates", () => {
  const FULL: VMCapability[] = ["create_vm", "delete_vm", "network_write", "storage_write", "console", "list_storage"];
  const RO: VMCapability[] = ["readonly", "console"];

  it("gateVMNetworkWrite needs cap + perm", () => {
    expect(gateVMNetworkWrite(FULL, ["vm.network.write"]).allowed).toBe(true);
    expect(gateVMNetworkWrite(FULL, []).allowed).toBe(false);
    expect(gateVMNetworkWrite(["console"], ["vm.network.write"]).allowed).toBe(false);
    expect(gateVMNetworkWrite(RO, ["*"]).allowed).toBe(false);
  });

  it("gateVMStorageWrite needs cap + perm", () => {
    expect(gateVMStorageWrite(FULL, ["vm.storage.write"]).allowed).toBe(true);
    expect(gateVMStorageWrite(FULL, []).allowed).toBe(false);
    expect(gateVMStorageWrite(["console"], ["vm.storage.write"]).allowed).toBe(false);
    expect(gateVMStorageWrite(RO, ["*"]).allowed).toBe(false);
  });

  it("gateVMConsole needs cap + perm (independent of read-only)", () => {
    expect(gateVMConsole(FULL, ["vm.console"]).allowed).toBe(true);
    expect(gateVMConsole(FULL, []).allowed).toBe(false);
    expect(gateVMConsole(["network_write"], ["vm.console"]).allowed).toBe(false);
    // console is allowed on an otherwise read-only provider when the cap is present
    expect(gateVMConsole(RO, ["vm.console"]).allowed).toBe(true);
  });

  it("gateVMBackup needs export cap + vm.backup, blocked when read-only", () => {
    const EXPORT: VMCapability[] = ["export", "snapshot"];
    expect(gateVMBackup(EXPORT, ["vm.backup"]).allowed).toBe(true);
    expect(gateVMBackup(EXPORT, ["*"]).allowed).toBe(true);
    expect(gateVMBackup(EXPORT, []).allowed).toBe(false); // missing perm
    expect(gateVMBackup(["snapshot"], ["vm.backup"]).allowed).toBe(false); // no export cap
    expect(gateVMBackup(["readonly", "export"], ["*"]).allowed).toBe(false); // read-only
  });
});
