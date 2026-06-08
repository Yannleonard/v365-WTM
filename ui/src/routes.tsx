// ui/src/routes.tsx
// Central route table. Public auth routes sit outside the AppShell; everything
// else is wrapped in RequireAuth + AppShell, and admin sections additionally in
// RequirePerm. Route → view → required perm mirrors the locked view list.

import { createBrowserRouter, Navigate } from "react-router-dom";
import { AppShell } from "./components/AppShell";
import { RequireAuth, RequirePerm } from "./components/RouteGuards";

import { Login } from "./views/Login";
import { TotpChallenge } from "./views/TotpChallenge";
import { Bootstrap } from "./views/Bootstrap";
import { Dashboard } from "./views/Dashboard";
import { Hosts } from "./views/Hosts";
import { Workloads } from "./views/Workloads";
import { WorkloadDetail } from "./views/WorkloadDetail";
import { VirtualMachines } from "./views/VirtualMachines";
import { VirtualMachineDetail } from "./views/VirtualMachineDetail";
import { VMCreateWizard } from "./views/VMCreateWizard";
import { VMNetworks } from "./views/VMNetworks";
import { VMStorage } from "./views/VMStorage";
import { VMClusters } from "./views/VMClusters";
import { HypervisorConnections } from "./views/HypervisorConnections";
import { StorageBackends } from "./views/StorageBackends";
import { Migration } from "./views/Migration";
import { Stacks } from "./views/Stacks";
import { StackEditor } from "./views/StackEditor";
import { Marketplace } from "./views/Marketplace";
import { Images } from "./views/Images";
import { Networks } from "./views/Networks";
import { Volumes } from "./views/Volumes";
import { Backups } from "./views/Backups";
import { SwarmServices } from "./views/SwarmServices";
import { K8sWorkloads } from "./views/K8sWorkloads";
import { K8sStorage } from "./views/K8sStorage";
import { K8sCluster } from "./views/K8sCluster";
import { Helm } from "./views/Helm";
import { Audit } from "./views/Audit";
import { Users } from "./views/Users";
import { Roles } from "./views/Roles";
import { Registries } from "./views/Registries";
import { Catalogs } from "./views/Catalogs";
import { Authentication } from "./views/Authentication";
import { Settings } from "./views/Settings";
import { Profile } from "./views/Profile";
import { NotFound } from "./views/NotFound";

export const router = createBrowserRouter([
  { path: "/login", element: <Login /> },
  { path: "/totp", element: <TotpChallenge /> },
  { path: "/bootstrap", element: <Bootstrap /> },
  {
    path: "/",
    element: (
      <RequireAuth>
        <AppShell />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <Dashboard /> },
      { path: "hosts", element: <Hosts /> },
      { path: "workloads", element: <Workloads /> },
      { path: "workloads/:hostId/:id", element: <WorkloadDetail /> },
      {
        path: "vms",
        element: (
          <RequirePerm anyOf={["vm.read", "*"]}>
            <VirtualMachines />
          </RequirePerm>
        ),
      },
      {
        path: "vms/new",
        element: (
          <RequirePerm anyOf={["vm.create", "*"]}>
            <VMCreateWizard />
          </RequirePerm>
        ),
      },
      {
        path: "vms/:pid/:id",
        element: (
          <RequirePerm anyOf={["vm.read", "*"]}>
            <VirtualMachineDetail />
          </RequirePerm>
        ),
      },
      {
        path: "vm-networks",
        element: (
          <RequirePerm anyOf={["vm.network.read", "vm.network.write", "vm.read", "*"]}>
            <VMNetworks />
          </RequirePerm>
        ),
      },
      {
        path: "vm-storage",
        element: (
          <RequirePerm anyOf={["vm.storage.read", "vm.storage.write", "vm.read", "*"]}>
            <VMStorage />
          </RequirePerm>
        ),
      },
      {
        path: "vm/connections",
        element: (
          <RequirePerm anyOf={["vm.create", "*"]}>
            <HypervisorConnections />
          </RequirePerm>
        ),
      },
      {
        path: "storage-backends",
        element: (
          <RequirePerm anyOf={["storage.backend.read", "storage.backend.write", "*"]}>
            <StorageBackends />
          </RequirePerm>
        ),
      },
      {
        path: "vm-clusters",
        element: (
          <RequirePerm anyOf={["vm.cluster.read", "vm.read", "*"]}>
            <VMClusters />
          </RequirePerm>
        ),
      },
      {
        path: "migration",
        element: (
          <RequirePerm anyOf={["v2v.read", "*"]}>
            <Migration />
          </RequirePerm>
        ),
      },
      {
        path: "stacks",
        element: (
          <RequirePerm anyOf={["docker.container.read", "*"]}>
            <Stacks />
          </RequirePerm>
        ),
      },
      {
        path: "stacks/new",
        element: (
          <RequirePerm anyOf={["docker.container.create", "*"]}>
            <StackEditor />
          </RequirePerm>
        ),
      },
      {
        path: "stacks/:hostId/:id",
        element: (
          <RequirePerm anyOf={["docker.container.read", "*"]}>
            <StackEditor />
          </RequirePerm>
        ),
      },
      { path: "marketplace", element: <Marketplace /> },
      {
        path: "images",
        element: (
          <RequirePerm anyOf={["docker.image.read", "*"]}>
            <Images />
          </RequirePerm>
        ),
      },
      {
        path: "networks",
        element: (
          <RequirePerm anyOf={["docker.network.read", "*"]}>
            <Networks />
          </RequirePerm>
        ),
      },
      {
        path: "volumes",
        element: (
          <RequirePerm anyOf={["docker.volume.read", "*"]}>
            <Volumes />
          </RequirePerm>
        ),
      },
      {
        path: "backups",
        element: (
          <RequirePerm anyOf={["docker.volume.read", "*"]}>
            <Backups />
          </RequirePerm>
        ),
      },
      {
        path: "swarm",
        element: (
          <RequirePerm anyOf={["swarm.service.read", "swarm.task.read", "swarm.node.read", "*"]}>
            <SwarmServices />
          </RequirePerm>
        ),
      },
      {
        path: "k8s",
        element: (
          <RequirePerm anyOf={["k8s.pod.read", "k8s.deployment.read", "k8s.node.read", "*"]}>
            <K8sWorkloads />
          </RequirePerm>
        ),
      },
      {
        path: "k8s-storage",
        element: (
          <RequirePerm anyOf={["k8s.storage.read", "*"]}>
            <K8sStorage />
          </RequirePerm>
        ),
      },
      {
        path: "k8s-cluster",
        element: (
          <RequirePerm anyOf={["k8s.namespace.read", "k8s.service.read", "k8s.hpa.read", "k8s.config.read", "k8s.ingress.read", "k8s.metrics.read", "*"]}>
            <K8sCluster />
          </RequirePerm>
        ),
      },
      {
        path: "helm",
        element: (
          <RequirePerm anyOf={["helm.release.read", "helm.repo.read", "*"]}>
            <Helm />
          </RequirePerm>
        ),
      },
      {
        path: "audit",
        element: (
          <RequirePerm anyOf={["audit.read", "*"]}>
            <Audit />
          </RequirePerm>
        ),
      },
      {
        path: "users",
        element: (
          <RequirePerm anyOf={["rbac.user.read", "rbac.user.create", "rbac.user.update", "rbac.user.delete", "*"]}>
            <Users />
          </RequirePerm>
        ),
      },
      {
        path: "roles",
        element: (
          <RequirePerm anyOf={["rbac.role.read", "rbac.role.create", "rbac.role.update", "rbac.role.delete", "*"]}>
            <Roles />
          </RequirePerm>
        ),
      },
      {
        path: "registries",
        element: (
          <RequirePerm anyOf={["marketplace.registry.read", "marketplace.registry.write", "*"]}>
            <Registries />
          </RequirePerm>
        ),
      },
      {
        path: "catalogs",
        element: (
          <RequirePerm anyOf={["marketplace.catalog.read", "marketplace.catalog.write", "*"]}>
            <Catalogs />
          </RequirePerm>
        ),
      },
      {
        path: "authentication",
        element: (
          <RequirePerm anyOf={["auth.provider.read", "auth.provider.write", "*"]}>
            <Authentication />
          </RequirePerm>
        ),
      },
      {
        path: "settings",
        element: (
          <RequirePerm anyOf={["settings.read", "settings.update", "*"]}>
            <Settings />
          </RequirePerm>
        ),
      },
      { path: "profile", element: <Profile /> },
      { path: "404", element: <NotFound /> },
    ],
  },
  { path: "*", element: <Navigate to="/404" replace /> },
]);
