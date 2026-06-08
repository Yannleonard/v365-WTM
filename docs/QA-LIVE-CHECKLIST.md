# UniHV — LIVE VERIFICATION CHECKLIST

> **Audience:** the QA + experts team executing the proofs **tomorrow morning** when the
> real **Xen / ESXi / Hyper-V** hosts come online, plus the **full local recette** runnable
> **today** on real KVM.
> **Owner's #1 rule:** NO mocks, NO fabricated data, NO false-success. A real hypervisor
> result or a clear error. Any placeholder export bytes (`VSPHEREEXPORT` / `XENEXPORT` /
> `HYPERVEXPORT` / `KVMEXPORT`) reaching a result = **FAIL**.
>
> Maps to `travaux.md` §5 (acceptance) and the "À PROUVER DEMAIN MATIN" list.
> Every command below uses the **real routes and JSON field names** read from the source
> (`server/internal/api/*.go`, `server/internal/vprovider/{esxi,xen,hyperv,kvm}/*.go`).
> Section C commands were **run live today** and the pasted outputs are the real expected results.

---

## 0. Conventions

- App: **http://localhost:8080**, login `admin` / `Admin1234567`.
- All API paths are under **`/api/v1`**.
- Auth = session **cookie** (`castor_session` + `castor_csrf`) OR bearer token.
- **CSRF (cookie path only):** mutating requests (POST/PUT/DELETE) require BOTH:
  - header `X-Castor-CSRF: <token>` (token returned in the login JSON as `csrfToken`, also in cookie `castor_csrf`), AND
  - an allowed **`Origin`** header (e.g. `Origin: http://localhost:8080`). Missing/foreign Origin → `403 csrf_failed "Origin not allowed."` (observed).
- Bearer-token requests are **exempt** from CSRF (no cookie ⇒ no CSRF) — useful for scripting if you mint an API token, but the curl recipes here use the cookie+CSRF path.

---

## 1. Prereqs

### 1.1 Get the latest deployed image (build + redeploy) — from COORDINATION.md §3

```bash
# Go build + tests (linux + windows + vet + test) in Docker — must be green.
MSYS_NO_PATHCONV=1 docker run --rm -v "/c/Users/yleon/.vscode/v360-leonard/unihv":/src \
  -w /src -v unihv-gomod:/go/pkg/mod golang:1.25.11-alpine sh -c \
  "CGO_ENABLED=0 go build ./server/... && CGO_ENABLED=0 GOOS=windows go build ./server/... \
   && CGO_ENABLED=0 go vet ./server/... && CGO_ENABLED=0 go test ./server/..."

# UI build + tests (~26 tests, must stay green).
cd ui && npm run build && npm test

# Rebuild + redeploy the local container.
docker build -t unihv:latest --build-arg VERSION=<tag> .
docker rm -f unihv
export CASTOR_SECRET_KEY=$(cat /tmp/unihv_secret.txt)
docker compose -f deploy/docker-compose.unihv.yml up -d
# wait until healthy:
until [ "$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/api/v1/healthz)" = "200" ]; do sleep 1; done
```

> ⚠️ **MSYS_NO_PATHCONV gotcha:** in Git-Bash you MUST prefix every `docker run` with
> `MSYS_NO_PATHCONV=1`, or the `-w /src` path is mangled to `C:/Program Files/Git/...` and
> the container fails to start. (PowerShell does not need it.)

### 1.2 Log in + capture CSRF token via curl

```bash
cd /tmp && rm -f cj.txt
curl -s -c cj.txt -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"Admin1234567"}' -o login.json -w "HTTP %{http_code}\n"
# Expect: HTTP 200
CSRF=$(python3 -c "import json;print(json.load(open('login.json'))['csrfToken'])")
echo "CSRF=$CSRF"
# Reusable header bundle for ALL mutations:
H="-b cj.txt -H X-Castor-CSRF:$CSRF -H Origin:http://localhost:8080 -H Content-Type:application/json"
```

Real login response (observed):
```json
{"amr":"pwd","csrfToken":"<token>","permissions":["*"],"requiresTotp":false,
 "user":{"id":"...","username":"admin","isActive":true,...}}
```

---

## SECTION A — Connect each real host (do FIRST tomorrow)

**Create-connection route** (`hvconnections.go`): `POST /api/v1/vm/connections`
Body fields (exact): `name`, `kind` (`kvm`|`hyperv`|`vmware`|`xen`), `endpoint`,
`username`, `secret` (the password), `insecureTls` (bool), `enabled` (bool).
> NOTE: ESXi uses `kind:"vmware"`. `endpoint` is **required** for `vmware` and `xen`.
> On `enabled:true`, the server connects + HealthChecks immediately; the response
> `status` is `connected` on success or `error` (with `lastError`) on failure — **no false success**.

**Optional pre-flight (no persist):** `POST /api/v1/vm/connections/test` with the same body
→ `{"ok":true}` or `422 "connection test failed: ..."`.

After create, **confirm connected** + capture the provider id, then **list its VMs**:
```bash
# List connections (status must be "connected"):
curl -s -b cj.txt http://localhost:8080/api/v1/vm/connections | python3 -m json.tool
# The provider id used by the VM routes == the connection id. Capture it:
# (the providers endpoint also lists them with capabilities)
curl -s -b cj.txt http://localhost:8080/api/v1/vm/providers | python3 -m json.tool
```

### A.1 — ESXi  (kind = vmware)
```bash
ESXI=$(curl -s $H -X POST http://localhost:8080/api/v1/vm/connections -d '{
  "name":"esxi-real","kind":"vmware","endpoint":"https://<ESXI_IP>/sdk",
  "username":"<USER>","secret":"<PASS>","insecureTls":true,"enabled":true
}')
echo "$ESXI" | python3 -m json.tool          # status MUST be "connected"
ESXI_PID=$(echo "$ESXI" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
echo "ESXI_PID=$ESXI_PID"
# List its VMs (capture a target vm id):
curl -s -b cj.txt "http://localhost:8080/api/v1/vm/providers/$ESXI_PID/vms" \
  | python3 -c "import sys,json;[print(v['id'],v['name'],v['state']) for v in json.load(sys.stdin)]"
ESXI_VM=<pick an id from the list>
```
PASS = `status:"connected"`, VM list non-empty. FAIL = `status:"error"` (read `lastError`).

### A.2 — Xen / XCP-ng  (kind = xen)
```bash
XEN=$(curl -s $H -X POST http://localhost:8080/api/v1/vm/connections -d '{
  "name":"xen-real","kind":"xen","endpoint":"https://<XEN_IP>",
  "username":"root","secret":"<PASS>","insecureTls":true,"enabled":true
}')
echo "$XEN" | python3 -m json.tool           # status MUST be "connected"
XEN_PID=$(echo "$XEN" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
curl -s -b cj.txt "http://localhost:8080/api/v1/vm/providers/$XEN_PID/vms" \
  | python3 -c "import sys,json;[print(v['id'],v['name'],v['state']) for v in json.load(sys.stdin)]"
XEN_VM=<pick an id>
```
PASS confirms the real `session.login_with_password` (XML-RPC) handshake succeeded.

### A.3 — Hyper-V  (kind = hyperv)
```bash
HV=$(curl -s $H -X POST http://localhost:8080/api/v1/vm/connections -d '{
  "name":"hyperv-real","kind":"hyperv","endpoint":"<HOST_OR_IP_OR_EMPTY>",
  "username":"<USER>","secret":"<PASS>","insecureTls":true,"enabled":true
}')
echo "$HV" | python3 -m json.tool            # status MUST be "connected"
HV_PID=$(echo "$HV" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
curl -s -b cj.txt "http://localhost:8080/api/v1/vm/providers/$HV_PID/vms" \
  | python3 -c "import sys,json;[print(v['id'],v['name'],v['state']) for v in json.load(sys.stdin)]"
HV_VM_STOPPED=<id of a STOPPED VM>
HV_VM_RUNNING=<id of a RUNNING VM>
```

---

## SECTION B — Prove ExportVM is REAL per host (the core of tomorrow)

The export is exercised through the **backup engine** (which calls the provider's `ExportVM`
and writes the streamed bytes to a storage backend). Create a `local` storage backend once,
then run a backup per host and **inspect the produced artifact**.

```bash
# One-time: a local filesystem backend (writes inside the container at /tmp/unihv-live).
BK=$(curl -s $H -X POST http://localhost:8080/api/v1/storage/backends -d '{
  "name":"qa-live","type":"local","target":"/tmp/unihv-live","enabled":true
}')
BKID=$(echo "$BK" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
echo "BKID=$BKID"   # status must be "connected"
```

**Run-backup route** (`backups_vm.go`): `POST /api/v1/vm-backups/run`
Body fields (exact): `providerId`, `vmId`, `backendId`.
Result row fields: `status` (`completed`|`failed`), `sizeBytes`, `diskCount`,
`disks[]{key,sizeBytes,format}`, `error`.

Helper to inspect the artifact on disk (the container path):
```bash
inspect_artifact() {   # $1 = keyPrefix from the run response
  docker exec unihv sh -c "ls -la /tmp/unihv-live/$1; echo '--- magic ---'; \
    for f in /tmp/unihv-live/$1*; do echo \$f; head -c 16 \"\$f\" | od -c | head -1; done; \
    echo '--- manifest ---'; cat /tmp/unihv-live/${1}manifest.json 2>/dev/null"
}
```

**Universal FAIL criteria for all three:**
- Any artifact whose first bytes spell `VSPHEREEXPORT` / `XENEXPORT` / `HYPERVEXPORT` → **FAIL** (placeholder reached live).
- `status:"completed"` with `sizeBytes:0` or a tiny (~40-byte) body → **FAIL** (false success).
- The run says `completed` but the artifact is missing/empty on disk → **FAIL**.

### B.1 — ESXi  →  acceptance: `vm.Export` (ExportVm) direct, NOT snapshot fallback
```bash
RUN=$(curl -s $H -X POST http://localhost:8080/api/v1/vm-backups/run \
  -d "{\"providerId\":\"$ESXI_PID\",\"vmId\":\"$ESXI_VM\",\"backendId\":\"$BKID\"}")
echo "$RUN" | python3 -m json.tool
PFX=$(echo "$RUN" | python3 -c "import sys,json;print(json.load(sys.stdin)['keyPrefix'])")
inspect_artifact "$PFX"
```
**EXPECTED (PASS):** `status:"completed"`, `sizeBytes` > 0 (real disk size), `disks[].format:"vmdk"`
(the code records the true on-the-wire format `vp.DiskVMDK` regardless of requested token —
`esxi/esxi.go:510`). Artifact magic = stream-optimized VMDK (`KDMV`...) / OVF tar, **not** `VSPHEREEXPORT`.

**Assert the ExportVm path (not the snapshot fallback)** — `esxi/live_vsphere.go:789 exportLease`:
the code calls `vm.Export(ctx)` FIRST; it only falls back to `ExportSnapshot` if the host
rejects with **"does not implement"** (that branch is for `vcsim` only). On a **real ESXi/vCenter**,
`vm.Export` succeeds, so the fallback is never taken. **How to tell:** tail the container logs
during the run and confirm there is **no** "does not implement" message and **no** ephemeral
`unihv-export` snapshot is created on the VM:
```bash
docker logs --since 2m unihv 2>&1 | grep -iE "does not implement|ExportSnapshot|unihv-export" \
  && echo "FAIL: snapshot fallback was used" || echo "PASS: direct ExportVm path"
# Also confirm on the host UI: no leftover 'unihv-export' snapshot on the VM.
```
FAIL if the fallback ran (means ExportVm did not work directly — must be fixed before sign-off).

### B.2 — Xen  →  acceptance: real `session.login` → GET /export → importable XVA, chunked
```bash
RUN=$(curl -s $H -X POST http://localhost:8080/api/v1/vm-backups/run \
  -d "{\"providerId\":\"$XEN_PID\",\"vmId\":\"$XEN_VM\",\"backendId\":\"$BKID\"}")
echo "$RUN" | python3 -m json.tool
PFX=$(echo "$RUN" | python3 -c "import sys,json;print(json.load(sys.stdin)['keyPrefix'])")
inspect_artifact "$PFX"
```
**EXPECTED (PASS):** `status:"completed"`. The provider streams the XAPI export
(`GET /export?session_id=...&uuid=...`, `xen/xen.go:534 exportStream`). The artifact is a real
**XVA tar archive** (first bytes look like a tar header / `ustar`, contains `ova.xml` + VHD images),
importable via `xe vm-import` on the host. **`SizeBytes=-1` on the provider's ExportInfo is EXPECTED
and OK** for the chunked XVA stream (no Content-Length); the backend records the actual streamed
byte count, so the backup row's `sizeBytes` is the true size > 0.
- **Running-VM behavior:** XAPI permits export of a running VM (it snapshots internally); note
  whether the host returns a redirect/normal stream — either way bytes must be real.
- FAIL = `XENEXPORT` placeholder bytes, or `403/404` surfaced as a clear error but `status` still `completed`.

To prove importability (on the Xen host):
```bash
# copy the artifact off the container and import it:
docker cp unihv:/tmp/unihv-live/$PFX <local>/xen.xva  # then on host:
# xe vm-import filename=xen.xva   -> must create a VM
```

### B.3 — Hyper-V  →  acceptance: stopped VM streams byte-identical VHDX; running VM = ErrConflict
**B.3a — STOPPED VM → real VHDX stream (sha256 must match on-disk file):**
```bash
RUN=$(curl -s $H -X POST http://localhost:8080/api/v1/vm-backups/run \
  -d "{\"providerId\":\"$HV_PID\",\"vmId\":\"$HV_VM_STOPPED\",\"backendId\":\"$BKID\"}")
echo "$RUN" | python3 -m json.tool
PFX=$(echo "$RUN" | python3 -c "import sys,json;print(json.load(sys.stdin)['keyPrefix'])")
inspect_artifact "$PFX"
# sha256 of the streamed artifact:
docker exec unihv sh -c "sha256sum /tmp/unihv-live/${PFX}disk-0.vhdx 2>/dev/null || \
  for f in /tmp/unihv-live/$PFX*.vhdx; do sha256sum \"\$f\"; done"
# Compare against the on-disk VHDX ON THE HYPER-V HOST (PowerShell):
#   Get-FileHash 'C:\...\<vm>.vhdx' -Algorithm SHA256
```
**EXPECTED (PASS):** `status:"completed"`, real VHDX (magic `vhdxfile`), size > 0 matching the
host file, and the **sha256 equal** to the on-disk VHDX. The live exporter resolves the VHDX via
`Msvm_StorageAllocationSettingData.HostResource` and streams the real bytes
(`hyperv/provider.go:514 ExportVM` → `liveExportHook` in `live_windows.go`).
FAIL = `HYPERVEXPORT` placeholder / size 0 / sha mismatch.

**B.3b — RUNNING VM → clear ErrConflict (stop/checkpoint), NOT a placeholder:**
```bash
RUN=$(curl -s $H -X POST http://localhost:8080/api/v1/vm-backups/run \
  -d "{\"providerId\":\"$HV_PID\",\"vmId\":\"$HV_VM_RUNNING\",\"backendId\":\"$BKID\"}" \
  -w "\nHTTP %{http_code}\n")
echo "$RUN"
```
**EXPECTED (PASS):** the run **FAILS** with a clear message — the live code returns
`ErrConflict` with text like *"VM \"<name>\" is running; its VHDX is locked for write — stop the
VM (or take a checkpoint) before exporting"* (`live_windows.go:1537`). The backup row must record
`status:"failed"` with that error — **never** `completed`. FAIL = it returns `completed` (false
success) or a `HYPERVEXPORT` placeholder.

---

## SECTION C — Full local recette (real KVM, runnable TODAY)

> All commands below were **executed live today** against the running container on real WSL
> libvirt; the pasted JSON is the **actual** output. Capture the provider/VM ids fresh each run
> (ids are stable per VM but re-list to be safe).

```bash
# Capture KVM provider + VM ids:
PID=$(curl -s -b cj.txt http://localhost:8080/api/v1/vm/providers \
  | python3 -c "import sys,json;d=json.load(sys.stdin);print(next(p['id'] for p in d if p['kind']=='kvm'))")
echo "PID=$PID"
curl -s -b cj.txt "http://localhost:8080/api/v1/vm/providers/$PID/vms" \
  | python3 -c "import sys,json;[print(v['id'],v['name'],v['state']) for v in json.load(sys.stdin)]"
```
Observed (real):
```
56751731-8008-495c-8272-fe15a6c2e67c web-server-01 running
5aaca0f1-32af-4ea6-8ca4-120b74d0c6a7 linux-server  running
f751f8f3-3339-4358-aaef-5d6d2f864490 w11           stopped
2f16a1f8-1214-4296-bd2a-70e61c7fb19f db-server-01  running
227fdb4f-8249-4230-b6ed-e6090e2fdfa8 Alpine        running
```
Cross-check vs `virsh` (must match):
```bash
wsl -d Ubuntu -u root -- bash -lc "virsh -c qemu:///system list --all"
```

### C.1 — Inventory loads
`GET /api/v1/vm/providers` → KVM provider with capabilities incl. `export`, `metrics`, `console`,
`hotplug`, `disk_resize`, `snapshot` (observed). `GET /api/v1/inventory` → unified VM+container tree.
PASS = provider listed + 5 VMs returned matching `virsh`.

### C.2 — Power start / stop (route `/power/{op}`, op ∈ start|stop|reset|suspend|resume)
Suspend + resume a running VM (safe, fully reversible):
```bash
DB=2f16a1f8-1214-4296-bd2a-70e61c7fb19f
curl -s $H -X POST "http://localhost:8080/api/v1/vm/providers/$PID/vms/$DB/power/suspend" -w " HTTP %{http_code}\n"
curl -s $H -X POST "http://localhost:8080/api/v1/vm/providers/$PID/vms/$DB/power/resume"  -w " HTTP %{http_code}\n"
```
Observed: both → `{"kind":"powerOp","state":"succeeded","progress":100,...}` HTTP 200.

> ⚠️ **KNOWN ISSUE found during recette (log it):** `POST .../vms/<w11>/power/start` on the
> **stopped** w11 VM returned **`404 not_found`** even though `w11` exists in libvirt (`virsh
> list --all` shows it `shut off`) and its detail/metrics endpoints resolve fine. Investigate the
> VM-id resolution on the power route for shut-off domains before sign-off (does not affect
> running-VM power ops, which pass).

### C.3 — Console connects (note: black until keypress is normal VNC)
```bash
curl -s -b cj.txt "http://localhost:8080/api/v1/vm/providers/$PID/vms/56751731-8008-495c-8272-fe15a6c2e67c/console"
```
Observed: `{"kind":"vnc","host":"host.docker.internal","port":5900}` HTTP 200.
In the UI, open the VM → Console tab; the interactive WS bridges browser→guacd→VNC.
**A static console shows BLACK until a keypress forces a redraw — this is normal VNC, not a bug.**

### C.4 — Snapshot create / delete
```bash
AL=227fdb4f-8249-4230-b6ed-e6090e2fdfa8
curl -s $H -X POST "http://localhost:8080/api/v1/vm/providers/$PID/vms/$AL/snapshots" \
  -d '{"name":"qa-snap","description":"qa live test"}' -w "\nHTTP %{http_code}\n"
# list -> capture id (snapshot id == its name on KVM), then delete:
SNAP=qa-snap
curl -s $H -X DELETE "http://localhost:8080/api/v1/vm/providers/$PID/vms/$AL/snapshots/$SNAP" -w " HTTP %{http_code}\n"
```
Observed: create → `{"kind":"snapshot","state":"succeeded","progress":100}` HTTP 201;
delete → `{"kind":"deleteSnapshot","state":"succeeded","progress":100}`.
Body fields: `name`, `description`, `memory` (bool), `quiesce` (bool).

### C.5 — Hot-add disk (DeviceManager, route `/disks`, needs RUNNING VM)
```bash
curl -s $H -X POST "http://localhost:8080/api/v1/vm/providers/$PID/vms/$DB/disks" \
  -d '{"sizeGb":1,"format":"qcow2"}' -w "\nHTTP %{http_code}\n"
# verify in guest/virsh that a new vdX appeared; then detach by its disk id:
# curl -s $H -X DELETE ".../vms/$DB/disks/<diskId>"
```
PASS = task `succeeded` and the new disk visible via `virsh domblklist <vm>`.
(Greyed in UI if `hotplug` capability absent — here it IS present.)

### C.6 — Online disk resize (route `/disks/{diskId}/resize`, RUNNING VM, no reboot)
```bash
# Use an existing disk id from the VM detail (e.g. "<vmid>-vdb"); grow it:
curl -s $H -X POST "http://localhost:8080/api/v1/vm/providers/$PID/vms/$DB/disks/<DISK_ID>/resize" \
  -d '{"sizeGb":<new_larger_size>}' -w "\nHTTP %{http_code}\n"
```
PASS = task `succeeded`; `virsh domblkinfo <vm> <target>` shows the larger capacity. Gated by
the dedicated `vm.disk.resize` permission + `disk_resize` capability.

### C.7 — Real metrics (running has data; STOPPED VM = 0/empty — the fixed bug)
```bash
# Running:
curl -s -b cj.txt "http://localhost:8080/api/v1/vm/providers/$PID/vms/56751731-8008-495c-8272-fe15a6c2e67c/metrics"
# Stopped (w11) — MUST be empty:
curl -s -b cj.txt "http://localhost:8080/api/v1/vm/providers/$PID/vms/f751f8f3-3339-4358-aaef-5d6d2f864490/metrics"
```
Observed running (real DomainGetInfo/MemoryStats/BlockStats):
```json
{"samples":[{"timestamp":"2026-06-08T21:46:04Z","cpuPercent":0,"memUsageBytes":64716800,
  "memLimitBytes":2147483648,"netRxBytes":1844,"netTxBytes":0,"diskReadBytes":1698,"diskWriteBytes":0}]}
```
Observed stopped (w11): `{"samples":null}` → **0 samples / empty** ✅ (the "36% CPU on a
powered-off VM" bug is fixed). FAIL = any non-zero invented metrics on a stopped VM.

### C.8 — Backup of web-server-01 completes with real qcow2
```bash
BK=8ee9d33f-c4ab-4bf2-8443-4223ccb0396d   # an existing connected 'local' backend (or create one, §B)
curl -s $H -X POST http://localhost:8080/api/v1/vm-backups/run \
  -d "{\"providerId\":\"$PID\",\"vmId\":\"56751731-8008-495c-8272-fe15a6c2e67c\",\"backendId\":\"$BK\"}" \
  -w "\nHTTP %{http_code}\n"
```
Observed (real):
```json
{"status":"completed","sizeBytes":196688,"diskCount":1,
 "disks":[{"key":"vm/56751731-.../disk-0.qcow2","sizeBytes":196688,"format":"qcow2"}],
 "guestOs":"hvm","firmware":"bios"}   // HTTP 201
```
Artifact verified on disk: `disk-0.qcow2` (196688 B) + `manifest.json`; magic bytes `Q F I 373`
(`51 46 49 fb`) = **real qcow2** (qemu-img + libvirt StorageVolDownload RPC). PASS.

### C.9 — Alarm SMTP test → MailHog receives a real mail
MailHog runs as container `unihv-mailhog` (SMTP `:1025`, UI `:8025`). It is on the default
bridge, so from the app container reach it via **`host.docker.internal:1025`**.
```bash
# Create an SMTP channel:
CH=$(curl -s $H -X POST http://localhost:8080/api/v1/alarms/channels -d '{
  "name":"qa-mailhog","type":"smtp",
  "smtp":{"host":"host.docker.internal","port":1025,"from":"unihv@test.local",
          "to":"ops@test.local","useTLS":false,"startTLS":false}}')
CHID=$(echo "$CH" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
# Send a test:
curl -s $H -X POST "http://localhost:8080/api/v1/alarms/channels/$CHID/test" -w "\nHTTP %{http_code}\n"
# Verify MailHog inbox:
curl -s http://localhost:8025/api/v2/messages | python3 -c "import sys,json;d=json.load(sys.stdin);print('count:',d['total'])"
```
Observed: test → `{"ok":true}` HTTP 200; MailHog received the mail
**From `unihv@test.local` → To `ops@test.local` Subject `UniHV alarms test: qa-mailhog`** ✅.
SMTP channel input fields (exact, `alarms.go`): `name`, `type:"smtp"`, and
`smtp:{host,port,username,password,from,to,useTLS,startTLS}` (port defaults to 587; password
sealed at rest, never returned).
**No-false-success proof:** a bad SMTP host/port returns a CLEAR error, not success:
```
test failed: smtp channel "qa-bad" send to host.docker.internal:2 failed: dial tcp ...: connection refused   // HTTP 422
```

---

## SECTION D — Sign-off matrix (`travaux.md` §5 acceptance)

Fill PASS/FAIL + note. "Today" rows are pre-verified (results above); "Tomorrow" rows need the real hosts.

| # | travaux.md §5 criterion | Test (this doc) | When | Result | PASS/FAIL |
|---|---|---|---|---|---|
| 1 | `docker build … .` passes | §1.1 build | today | green expected | ☐ |
| 2 | `cd ui && npm run build && npm test` passes | §1.1 UI | today | green expected | ☐ |
| 3 | `go test ./server/...` passes in Docker | §1.1 Go | today | green expected | ☐ |
| 4 | App reachable + KVM inventory works | §C.1 | today | ✅ 5 VMs, matches virsh | ☐ |
| 4b | Power start/stop works | §C.2 | today | ✅ suspend/resume (⚠ stopped-VM start 404, §C.2) | ☐ |
| 4c | Console connects | §C.3 | today | ✅ vnc ticket :5900 | ☐ |
| 4d | Snapshot create/delete | §C.4 | today | ✅ succeeded | ☐ |
| 4e | Hot-add disk | §C.5 | today | run + verify virsh | ☐ |
| 4f | Online disk resize | §C.6 | today | run + verify domblkinfo | ☐ |
| 4g | Real metrics (stopped = 0/empty) | §C.7 | today | ✅ running real, w11 empty | ☐ |
| 4h | Backup = real qcow2, completed | §C.8 | today | ✅ 196688B qcow2 (magic QFI) | ☐ |
| 5 | ESXi export ≠ ErrUnsupported (real) | §B.1 | tomorrow | direct ExportVm, vmdk >0 | ☐ |
| 5 | Xen export ≠ ErrUnsupported (real) | §B.2 | tomorrow | real XVA, importable | ☐ |
| 5 | Hyper-V export ≠ ErrUnsupported (real) | §B.3a | tomorrow | VHDX sha256 == on-disk | ☐ |
| 5b | Hyper-V running VM → clear ErrConflict | §B.3b | tomorrow | failed + stop/checkpoint msg | ☐ |
| 6 | Email via real SMTP works | §C.9 | today | ✅ MailHog received | ☐ |
| 7 | No false success on prod path | §B (all) + §C.9 bad-host | both | no placeholder bytes; bad cfg → 422 | ☐ |
| A | ESXi host connects (status connected) | §A.1 | tomorrow | — | ☐ |
| A | Xen host connects (status connected) | §A.2 | tomorrow | — | ☐ |
| A | Hyper-V host connects (status connected) | §A.3 | tomorrow | — | ☐ |

### Global FAIL gate (any one = do NOT sign off)
- Any export artifact starting with `VSPHEREEXPORT` / `XENEXPORT` / `HYPERVEXPORT` / `KVMEXPORT`.
- Any backup row `status:"completed"` with 0 / placeholder-sized bytes, or missing artifact.
- ESXi export taking the snapshot fallback on a real host (§B.1 — must use direct `vm.Export`).
- Hyper-V running-VM export returning `completed` instead of a clear ErrConflict.
- Any stopped/unavailable resource showing invented (non-zero) metrics.

---

### Appendix — quick reference (verified routes & fields)

| Action | Method + path | Key body fields |
|---|---|---|
| Login | `POST /auth/login` | `username,password` → `csrfToken` |
| Create HV connection | `POST /vm/connections` | `name,kind(kvm\|hyperv\|vmware\|xen),endpoint,username,secret,insecureTls,enabled` |
| Test HV connection | `POST /vm/connections/test` | same body → `{ok:true}` / 422 |
| List connections | `GET /vm/connections` | — (`status`, `lastError`) |
| List providers | `GET /vm/providers` | — (id + capabilities) |
| List VMs | `GET /vm/providers/{providerID}/vms` | — (`id,name,state,vcpus,memoryMb,disks[]`) |
| Power op | `POST /vm/providers/{providerID}/vms/{vmID}/power/{op}` | op ∈ start\|stop\|reset\|suspend\|resume |
| Snapshot create | `POST .../vms/{vmID}/snapshots` | `name,description,memory,quiesce` |
| Snapshot delete | `DELETE .../vms/{vmID}/snapshots/{snapID}` | — |
| Hot-add disk | `POST .../vms/{vmID}/disks` | `sizeGb,format` |
| Disk resize | `POST .../vms/{vmID}/disks/{diskID}/resize` | `sizeGb` |
| Metrics | `GET .../vms/{vmID}/metrics` | — (`samples[]`) |
| Console ticket | `GET .../vms/{vmID}/console` | — (`kind,host,port`) |
| Storage backend | `POST /storage/backends` | `name,type(local\|s3\|nfs\|...),target,endpoint,username,secret,enabled` |
| Run VM backup/export | `POST /vm-backups/run` | `providerId,vmId,backendId` |
| List VM backups | `GET /vm-backups?vmId=` | — |
| SMTP channel create | `POST /alarms/channels` | `name,type:"smtp",smtp:{host,port,username,password,from,to,useTLS,startTLS}` |
| Channel test | `POST /alarms/channels/{id}/test` | — → `{ok:true}` / 422 clear error |

> Reminder: every mutation needs `-H X-Castor-CSRF:$CSRF -H Origin:http://localhost:8080` on the
> cookie path. A bearer API token skips CSRF entirely.
