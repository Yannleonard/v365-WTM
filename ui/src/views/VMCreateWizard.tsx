// ui/src/views/VMCreateWizard.tsx
//
// Multi-step "Create VM" wizard (route /vms/new). Steps:
//   1. Basics    — name, provider, host/cluster, guest OS, firmware
//   2. Compute   — vCPUs, memory (MB)
//   3. Storage   — disk size + storage pool + format; optional boot ISO (picked
//                  from the ISO volumes of a pool)
//   4. Network   — pick a virtual network
// then POST a VMSpec to /vm/providers/{pid}/vms and show the returned Task.
//
// The provider list comes from useVMCapabilityLookup; only providers advertising
// "create_vm" are offered. Host/cluster/storage/network choices are loaded live
// from the selected provider. Every step validates before "Next"/"Create".

import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import {
  useVMCapabilityLookup,
  useVMStorage,
  useVMNetworks,
  useVMVolumes,
} from "../lib/hooks";
import { hasVMCap, can } from "../lib/rbac";
import { PageHeader } from "../components/PageHeader";
import { EmptyState } from "../components/EmptyState";
import { ActionButton } from "../components/ActionButton";
import { TextField, SelectField } from "../components/Field";
import { IconVM, IconCheck } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { formatBytes } from "../lib/format";
import type { VMHost, VMSpec, VMTask } from "../lib/types";

const FORMAT_OPTIONS = ["qcow2", "raw", "vmdk", "vhdx", "vdi"] as const;
const STEPS = ["Basics", "Compute", "Storage", "Network"] as const;
type StepIndex = 0 | 1 | 2 | 3;

export function VMCreateWizard() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { permissions } = useAuth();
  const { providers } = useVMCapabilityLookup();

  // Only providers that can create VMs are eligible targets.
  const eligible = useMemo(() => providers.filter((p) => hasVMCap(p.capabilities, "create_vm")), [providers]);

  const [step, setStep] = useState<StepIndex>(0);

  // ---- form state ----
  const [pid, setPid] = useState("");
  const [name, setName] = useState("");
  const [hostId, setHostId] = useState("");
  const [clusterId, setClusterId] = useState("");
  const [guestOs, setGuestOs] = useState("");
  const [firmware, setFirmware] = useState<"bios" | "uefi">("bios");

  const [vcpus, setVcpus] = useState("2");
  const [memoryMb, setMemoryMb] = useState("2048");

  const [diskGb, setDiskGb] = useState("20");
  const [diskFormat, setDiskFormat] = useState<string>("qcow2");
  const [storageId, setStorageId] = useState("");
  const [isoPoolId, setIsoPoolId] = useState("");
  const [bootIso, setBootIso] = useState("");

  const [networkId, setNetworkId] = useState("");

  const [submitting, setSubmitting] = useState(false);
  const [task, setTask] = useState<VMTask | null>(null);

  // Default the provider to the first eligible one.
  useEffect(() => {
    if (!pid && eligible.length > 0) setPid(eligible[0]!.id);
  }, [eligible, pid]);

  // ---- live provider resources ----
  const hostsQ = useVMHostsLite(pid);
  const hosts = hostsQ.data ?? [];
  const storageQ = useVMStorage(pid, !!pid);
  const pools = storageQ.data ?? [];
  const networksQ = useVMNetworks(pid, !!pid);
  const networks = networksQ.data ?? [];
  const isoVolsQ = useVMVolumes(pid, isoPoolId, !!pid && !!isoPoolId);
  const isos = useMemo(() => (isoVolsQ.data ?? []).filter((v) => v.isIso), [isoVolsQ.data]);

  // Default storage pool once pools load.
  useEffect(() => {
    if (pools.length > 0 && !pools.some((p) => p.id === storageId)) setStorageId(pools[0]!.id);
  }, [pools, storageId]);

  const canCreate = can(permissions, "vm.create");

  // ---- per-step validation ----
  const vcpusNum = Number(vcpus);
  const memNum = Number(memoryMb);
  const diskNum = Number(diskGb);

  const step0Ok = name.trim().length > 0 && !!pid;
  const step1Ok = Number.isInteger(vcpusNum) && vcpusNum > 0 && Number.isFinite(memNum) && memNum > 0;
  const step2Ok = Number.isFinite(diskNum) && diskNum > 0;
  const step3Ok = networks.length === 0 || !!networkId;

  const stepValid = [step0Ok, step1Ok, step2Ok, step3Ok][step];

  const submit = async () => {
    if (!step0Ok || !step1Ok || !step2Ok) return;
    const spec: VMSpec = {
      name: name.trim(),
      hostId: hostId.trim() || undefined,
      clusterId: clusterId.trim() || undefined,
      vcpus: vcpusNum,
      memoryMb: memNum,
      guestOs: guestOs.trim() || undefined,
      firmware,
      disks: [
        {
          capacityGb: diskNum,
          format: diskFormat || undefined,
          storageId: storageId || undefined,
        },
      ],
      nics: networkId ? [{ networkId }] : [],
      bootIso: bootIso || undefined,
    };
    setSubmitting(true);
    try {
      const t = await api.vmCreate(pid, spec);
      setTask(t ?? { id: "" });
      toast.success("VM creation requested", name.trim());
      queryClient.invalidateQueries({ queryKey: ["inventory"] });
      queryClient.invalidateQueries({ queryKey: ["vms", pid] });
    } catch (err) {
      toastError("Create failed", err);
    } finally {
      setSubmitting(false);
    }
  };

  // ---- access / availability gates ----
  if (eligible.length === 0) {
    return (
      <div className="page">
        <PageHeader title="Create virtual machine" actions={<ActionButton variant="ghost" onClick={() => navigate("/vms")}>Back to virtual machines</ActionButton>} />
        <div className="card card-pad">
          <EmptyState
            icon={<IconVM size={40} />}
            title="No hypervisor supports VM creation"
            message="None of the connected hypervisor providers advertise the create_vm capability. Connect a hypervisor that supports provisioning."
          />
        </div>
      </div>
    );
  }

  if (!canCreate) {
    return (
      <div className="page">
        <PageHeader title="Create virtual machine" actions={<ActionButton variant="ghost" onClick={() => navigate("/vms")}>Back to virtual machines</ActionButton>} />
        <div className="card card-pad">
          <EmptyState icon={<IconVM size={40} />} title="Access denied" message="You lack the vm.create permission required to provision virtual machines." />
        </div>
      </div>
    );
  }

  // ---- success panel after submit ----
  if (task) {
    return (
      <div className="page">
        <PageHeader title="Create virtual machine" />
        <div className="card card-pad col" style={{ gap: "var(--sp-4)" }}>
          <div className="row" style={{ gap: "var(--sp-2)", color: "var(--success)", fontWeight: 600 }}>
            <IconCheck size={18} />
            VM creation requested
          </div>
          <div className="text-sm secondary">
            <strong className="mono">{name.trim()}</strong> is being created on <span className="mono">{pid}</span>. The
            hypervisor is processing the task below; the VM will appear in the list once provisioning completes.
          </div>
          <dl className="dl">
            <dt>Task ID</dt>
            <dd className="mono">{task.id || "—"}</dd>
            <dt>State</dt>
            <dd>{task.state || "submitted"}</dd>
            {task.progress !== undefined ? (
              <>
                <dt>Progress</dt>
                <dd className="mono">{Math.round(task.progress)}%</dd>
              </>
            ) : null}
            {task.message ? (
              <>
                <dt>Message</dt>
                <dd>{task.message}</dd>
              </>
            ) : null}
            {task.error ? (
              <>
                <dt>Error</dt>
                <dd style={{ color: "var(--danger)" }}>{task.error}</dd>
              </>
            ) : null}
          </dl>
          <div className="row">
            <ActionButton variant="primary" onClick={() => navigate("/vms")}>
              Go to virtual machines
            </ActionButton>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="page">
      <PageHeader
        title={
          <span className="row" style={{ gap: "var(--sp-3)" }}>
            <IconVM size={20} />
            Create virtual machine
          </span>
        }
        subtitle="Provision a new guest on a hypervisor in four steps."
        actions={
          <ActionButton variant="ghost" onClick={() => navigate("/vms")}>
            Cancel
          </ActionButton>
        }
      />

      {/* Step indicator */}
      <div className="card card-pad">
        <div className="row" style={{ gap: "var(--sp-3)", flexWrap: "wrap" }}>
          {STEPS.map((label, i) => {
            const active = i === step;
            const done = i < step;
            return (
              <div key={label} className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
                <span
                  className="pill"
                  style={{
                    color: active ? "var(--accent)" : done ? "var(--success)" : "var(--text-secondary)",
                    borderColor: active ? "var(--accent)" : done ? "var(--success)" : "var(--border)",
                    background: "transparent",
                  }}
                >
                  {done ? <IconCheck size={13} /> : <span className="mono">{i + 1}</span>}
                  {label}
                </span>
                {i < STEPS.length - 1 ? <span className="muted">›</span> : null}
              </div>
            );
          })}
        </div>
      </div>

      <div className="card card-pad col" style={{ gap: "var(--sp-4)" }}>
        {step === 0 ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <TextField label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="my-vm" />
            <SelectField
              label="Hypervisor provider"
              value={pid}
              onChange={(e) => {
                setPid(e.target.value);
                setHostId("");
                setStorageId("");
                setNetworkId("");
                setIsoPoolId("");
                setBootIso("");
              }}
            >
              {eligible.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.id} ({p.kind})
                </option>
              ))}
            </SelectField>
            <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
              <SelectField label="Host (optional)" value={hostId} onChange={(e) => setHostId(e.target.value)}>
                <option value="">Auto / any host</option>
                {hosts.map((h) => (
                  <option key={h.id} value={h.id}>
                    {h.name || h.id}
                  </option>
                ))}
              </SelectField>
              <TextField
                label="Cluster ID (optional)"
                mono
                value={clusterId}
                onChange={(e) => setClusterId(e.target.value)}
                placeholder="leave blank for standalone"
              />
            </div>
            <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
              <TextField label="Guest OS (optional)" value={guestOs} onChange={(e) => setGuestOs(e.target.value)} placeholder="ubuntu64Guest" />
              <SelectField label="Firmware" value={firmware} onChange={(e) => setFirmware(e.target.value as "bios" | "uefi")}>
                <option value="bios">BIOS</option>
                <option value="uefi">UEFI</option>
              </SelectField>
            </div>
          </div>
        ) : null}

        {step === 1 ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <TextField
              label="vCPUs"
              type="number"
              min={1}
              value={vcpus}
              onChange={(e) => setVcpus(e.target.value)}
              error={vcpus !== "" && !step1Ok ? "Enter a positive whole number of vCPUs." : undefined}
              style={{ maxWidth: 200 }}
            />
            <TextField
              label="Memory (MB)"
              type="number"
              min={1}
              value={memoryMb}
              onChange={(e) => setMemoryMb(e.target.value)}
              hint={Number.isFinite(memNum) && memNum > 0 ? formatBytes(memNum * 1024 * 1024, 0) : undefined}
              style={{ maxWidth: 200 }}
            />
          </div>
        ) : null}

        {step === 2 ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
              <TextField
                label="Disk size (GB)"
                type="number"
                min={1}
                value={diskGb}
                onChange={(e) => setDiskGb(e.target.value)}
                style={{ maxWidth: 160 }}
              />
              <SelectField label="Format" value={diskFormat} onChange={(e) => setDiskFormat(e.target.value)}>
                {FORMAT_OPTIONS.map((f) => (
                  <option key={f} value={f}>
                    {f}
                  </option>
                ))}
              </SelectField>
              <SelectField label="Storage pool" value={storageId} onChange={(e) => setStorageId(e.target.value)}>
                {pools.length === 0 ? <option value="">No pools reported</option> : null}
                {pools.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                    {p.freeGb !== undefined ? ` — ${formatBytes(p.freeGb * 1024 ** 3, 0)} free` : ""}
                  </option>
                ))}
              </SelectField>
            </div>

            <div style={{ borderTop: "1px solid var(--border)", margin: "var(--sp-1) 0" }} />

            <span className="field-label" style={{ margin: 0 }}>
              Boot ISO (optional)
            </span>
            <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
              <SelectField
                label="ISO library (pool)"
                value={isoPoolId}
                onChange={(e) => {
                  setIsoPoolId(e.target.value);
                  setBootIso("");
                }}
              >
                <option value="">No boot ISO</option>
                {pools.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </SelectField>
              <SelectField
                label="ISO image"
                value={bootIso}
                onChange={(e) => setBootIso(e.target.value)}
                disabled={!isoPoolId}
              >
                <option value="">{!isoPoolId ? "Select a pool first" : isos.length === 0 ? "No ISOs in this pool" : "None"}</option>
                {isos.map((v) => (
                  <option key={v.id} value={v.path || v.id}>
                    {v.name}
                  </option>
                ))}
              </SelectField>
            </div>
            <span className="text-xs muted">
              Attach an ISO from the storage library to boot an installer. Upload ISOs on the VM Storage page.
            </span>
          </div>
        ) : null}

        {step === 3 ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            {networksQ.isLoading ? (
              <span className="text-sm muted">Loading networks…</span>
            ) : networks.length === 0 ? (
              <div className="banner" role="status">
                This provider reports no virtual networks. The VM will be created without a NIC; you can attach one later, or
                create a network on the VM Networks page first.
              </div>
            ) : (
              <SelectField label="Network" value={networkId} onChange={(e) => setNetworkId(e.target.value)}>
                <option value="">Select a network…</option>
                {networks.map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.name}
                    {n.type ? ` (${n.type})` : ""}
                  </option>
                ))}
              </SelectField>
            )}
          </div>
        ) : null}

        {/* Navigation */}
        <div className="row">
          <ActionButton variant="ghost" disabled={step === 0 || submitting} onClick={() => setStep((s) => (s - 1) as StepIndex)}>
            Back
          </ActionButton>
          <span className="spacer" />
          {step < 3 ? (
            <ActionButton
              variant="primary"
              disabled={!stepValid}
              tooltip={stepValid ? undefined : "Complete this step to continue"}
              onClick={() => setStep((s) => (s + 1) as StepIndex)}
            >
              Next
            </ActionButton>
          ) : (
            <ActionButton
              variant="primary"
              loading={submitting}
              disabled={!step0Ok || !step1Ok || !step2Ok || !step3Ok}
              onClick={submit}
            >
              Create VM
            </ActionButton>
          )}
        </div>
      </div>
    </div>
  );
}

// useVMHostsLite loads a provider's hosts for the wizard's host picker. Inlined
// here (rather than added to hooks.ts) since it is only used by the wizard.
function useVMHostsLite(pid: string) {
  return useQuery<VMHost[]>({
    queryKey: ["vm", "hosts", pid],
    queryFn: () => api.vmHosts(pid),
    enabled: !!pid,
    staleTime: 30_000,
  });
}
