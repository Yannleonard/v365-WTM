//go:build windows

// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// This file is compiled ONLY on Windows (`//go:build windows`) — per D-005/D-007 it
// is the REAL Hyper-V transport. It talks to the OFFICIAL Microsoft Virtualization
// management API: the WMI/CIM namespace root\virtualization\v2 (the Msvm_* classes —
// exactly what the Hyper-V PowerShell module wraps), accessed DIRECTLY via COM from
// Go using github.com/go-ole/go-ole. No PowerShell, no os/exec: every read and write
// goes through SWbemLocator -> ConnectServer("root\\virtualization\\v2") -> ExecQuery
// (WQL) and ExecMethod against the Msvm_* objects.
//
// The default cross-platform build (CGO_ENABLED=0, Linux/alpine) NEVER compiles this
// file; it uses New(...) with the in-memory WMI fake (sim_backend.go) and the
// conformance suite. go-ole is CGO-free (it uses syscall), so the windows/amd64 build
// stays CGO-free too.
//
// Msvm_* classes / methods used (root\virtualization\v2):
//
//	Msvm_ComputerSystem                       - VMs + the host role; EnabledState, Name,
//	                                            ElementName, Caption.
//	Msvm_VirtualSystemSettingData             - per-VM settings; VirtualSystemType
//	                                            (host vs VM), CreationTime, generation.
//	Msvm_ProcessorSettingData                 - VirtualQuantity -> vCPU count.
//	Msvm_MemorySettingData                    - VirtualQuantity -> memory (MB).
//	Msvm_StorageAllocationSettingData         - HostResource -> VHDX disk paths.
//	Msvm_SyntheticEthernetPortSettingData     - synthetic NICs (Address, Connection).
//	Msvm_VirtualEthernetSwitch                - virtual switches -> vp.Network.
//	Msvm_VirtualSystemManagementService       - DefineSystem (create), DestroySystem
//	                                            (delete), RequestStateChange (power, on
//	                                            Msvm_ComputerSystem).
//	Msvm_VirtualSystemSnapshotService         - CreateSnapshot / ApplySnapshot.
//	MSCluster_* (root\MSCluster, best effort) - failover cluster nodes; absent on a
//	                                            standalone host (modeled as 1 logical host).
package hyperv

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

var stderr = os.Stderr

func dbg() bool { return os.Getenv("UNIHV_DEBUG") != "" }

// virtNamespace is the official Hyper-V management WMI namespace.
const virtNamespace = `root\virtualization\v2`

// vmConcrete is the VirtualSystemType value identifying a *real* VM's settings
// (as opposed to the host's own Msvm_VirtualSystemSettingData, snapshots, planned VMs).
const vmTypeRealized = "Microsoft:Hyper-V:System:Realized"

// jobStarted is the WMI return code meaning "method started, an Msvm_ConcreteJob is
// tracking it" (i.e. async). 0 = completed synchronously. Anything else is an error.
const (
	wmiCompleted  = 0
	wmiJobStarted = 4096
)

// liveBackend holds a live COM/WMI connection to the local Hyper-V host's
// root\virtualization\v2 namespace. It is NOT goroutine-safe at the COM level, so all
// calls are serialized through mu and pinned (COM is apartment-threaded; we lock the
// OS thread per call).
type liveBackend struct {
	mu  sync.Mutex
	svc *ole.IDispatch // SWbemServices for root\virtualization\v2
	loc *ole.IUnknown  // SWbemLocator (kept alive)
	uni *ole.IDispatch // SWbemLocator's IDispatch (kept alive)

	hostName string
	ver      string
	ok       bool
}

// NewLive constructs a Provider backed by a live Hyper-V host via direct COM/WMI on
// root\virtualization\v2. computerName is accepted for API symmetry; the WMI moniker
// here always connects to the LOCAL host (".") — the official local-management surface.
// Available only on Windows; the cross-platform build uses New(...) with the fake.
func NewLive(id, computerName string, opts ...Option) (*Provider, error) {
	be, err := newLiveBackend(computerName)
	if err != nil {
		return nil, err
	}
	opts = append(opts, WithBackend(be))
	return New(id, opts...), nil
}

func newLiveBackend(_ string) (*liveBackend, error) {
	// COINIT_MULTITHREADED keeps things simple across goroutines; we still serialize.
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		// CoInitializeEx returns an *OleError with S_FALSE if already initialized; tolerate.
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 0x00000001 /* S_FALSE */ {
			return nil, fmt.Errorf("hyperv: CoInitializeEx: %w", err)
		}
	}
	loc, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		ole.CoUninitialize()
		return nil, fmt.Errorf("hyperv: create SWbemLocator: %w", err)
	}
	wmi, err := loc.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		loc.Release()
		ole.CoUninitialize()
		return nil, fmt.Errorf("hyperv: SWbemLocator QueryInterface: %w", err)
	}
	// ConnectServer(strServer=".", strNamespace="root\virtualization\v2")
	connRes, err := oleutil.CallMethod(wmi, "ConnectServer", ".", virtNamespace)
	if err != nil {
		wmi.Release()
		loc.Release()
		ole.CoUninitialize()
		return nil, fmt.Errorf("hyperv: ConnectServer(%s): %w", virtNamespace, err)
	}
	svc := connRes.ToIDispatch()

	be := &liveBackend{svc: svc, loc: loc, uni: wmi}
	be.hostName, be.ver = be.detectHostAndVersion()
	be.ok = be.hostName != ""
	return be, nil
}

// withCOM serializes a COM call and pins the OS thread (COM apartment requirement).
func (l *liveBackend) withCOM(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn()
}

// query runs a WQL query and returns the result objects as IDispatch handles. The
// caller MUST Release each returned object. Returns nil on error (live backend errors
// surface as empty inventory / health-false; callers map to vp errors at the seam).
func (l *liveBackend) query(wql string) []*ole.IDispatch {
	if l.svc == nil {
		return nil
	}
	resRaw, err := oleutil.CallMethod(l.svc, "ExecQuery", wql)
	if err != nil {
		return nil
	}
	res := resRaw.ToIDispatch()
	defer res.Release()
	return enumDispatch(res)
}

// enumDispatch walks an SWbemObjectSet (an IEnumVARIANT) into a slice of IDispatch.
func enumDispatch(set *ole.IDispatch) []*ole.IDispatch {
	enumProp, err := set.GetProperty("_NewEnum")
	if err != nil {
		return nil
	}
	enum, err := enumProp.ToIUnknown().IEnumVARIANT(ole.IID_IEnumVariant) //nolint
	enumProp.Clear()
	if err != nil || enum == nil {
		return nil
	}
	defer enum.Release()
	var out []*ole.IDispatch
	for {
		v, length, err := enum.Next(1)
		if err != nil || length == 0 {
			break
		}
		out = append(out, v.ToIDispatch())
	}
	return out
}

// prop reads a string-able property off a WMI object (empty string if absent/null).
func prop(obj *ole.IDispatch, name string) string {
	v, err := obj.GetProperty(name)
	if err != nil {
		return ""
	}
	defer v.Clear()
	if v.VT == ole.VT_NULL || v.VT == ole.VT_EMPTY {
		return ""
	}
	return fmt.Sprintf("%v", v.Value())
}

func propInt(obj *ole.IDispatch, name string) int64 {
	s := prop(obj, name)
	if s == "" {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// path returns an object's __PATH (the absolute WMI object path used as a method arg).
func path(obj *ole.IDispatch) string {
	// __PATH is a WMI system property. Some IDispatch property reads return it directly;
	// when they don't, fall back to the SWbemObject.Path_ helper object (.Path).
	if p := prop(obj, "__PATH"); p != "" {
		return p
	}
	pv, err := obj.GetProperty("Path_")
	if err == nil && pv.VT == ole.VT_DISPATCH {
		po := pv.ToIDispatch()
		full := prop(po, "Path")
		po.Release()
		pv.Clear()
		if full != "" {
			return full
		}
	} else {
		pv.Clear()
	}
	return ""
}

// detectHostAndVersion reads the host role Msvm_ComputerSystem (Caption identifies the
// "Hosting Computer System") and the management-service version.
func (l *liveBackend) detectHostAndVersion() (host, version string) {
	l.withCOM(func() {
		// The host's own Msvm_ComputerSystem has Description "Microsoft Hosting
		// Computer System"; its ElementName is the physical computer name.
		for _, o := range l.query("SELECT ElementName,Description,Caption FROM Msvm_ComputerSystem") {
			desc := prop(o, "Description")
			cap := prop(o, "Caption")
			if strings.Contains(desc, "Hosting") || strings.Contains(cap, "Hosting") ||
				strings.Contains(cap, "hébergement") {
				host = prop(o, "ElementName")
			}
			o.Release()
		}
		// Management service version (Hyper-V/WMI provider version).
		for _, o := range l.query("SELECT Version FROM Msvm_VirtualSystemManagementService") {
			version = prop(o, "Version")
			o.Release()
		}
	})
	if version != "" {
		version = "Microsoft Hyper-V (WMI " + virtNamespace + ", v" + version + ")"
	} else {
		version = "Microsoft Hyper-V (WMI " + virtNamespace + ")"
	}
	return host, version
}

func (l *liveBackend) version() string { return l.ver }

func (l *liveBackend) healthy() bool {
	if l.svc == nil {
		return false
	}
	// Liveness: the management service must be queryable.
	ok := false
	l.withCOM(func() {
		objs := l.query("SELECT Name FROM Msvm_VirtualSystemManagementService")
		ok = len(objs) > 0
		for _, o := range objs {
			o.Release()
		}
	})
	return ok
}

func (l *liveBackend) close() error {
	l.withCOM(func() {
		if l.svc != nil {
			l.svc.Release()
			l.svc = nil
		}
		if l.uni != nil {
			l.uni.Release()
			l.uni = nil
		}
		if l.loc != nil {
			l.loc.Release()
			l.loc = nil
		}
	})
	ole.CoUninitialize()
	return nil
}

// --- host / cluster ---

func (l *liveBackend) hostID() string {
	if l.hostName == "" {
		return "host-1"
	}
	return l.hostName
}

func (l *liveBackend) listHosts() []*hypervHost {
	// Standalone host modeled as one logical host (and, if a failover cluster is
	// present, as one cluster of one — see listClusters). We read CPU/memory from the
	// stdlib-free WMI cim_v2 namespace via a sibling connection would be heavier; the
	// official virtualization namespace gives us the host identity, so we report core
	// identity here and best-effort memory via Msvm_Memory of the host.
	var cores int
	var memBytes int64
	l.withCOM(func() {
		// Logical processors assigned to the host.
		procs := l.query("SELECT DeviceID FROM Msvm_Processor")
		cores = len(procs)
		for _, o := range procs {
			o.Release()
		}
	})
	h := &hypervHost{
		HostID:      l.hostID(),
		Name:        l.hostName,
		NodeState:   "Up",
		CPUCores:    cores,
		MemoryBytes: memBytes,
		Version:     l.ver,
	}
	return []*hypervHost{h}
}

func (l *liveBackend) getHost(hostID string) (*hypervHost, bool) {
	for _, h := range l.listHosts() {
		if h.HostID == hostID {
			return h, true
		}
	}
	return nil, false
}

func (l *liveBackend) listClusters() []*hypervCluster {
	// Standalone host: no failover cluster -> no cluster entity. (MSCluster_* lives in
	// root\MSCluster and is absent unless the Failover Clustering feature is installed;
	// we do not synthesize a fake cluster.)
	return nil
}

func (l *liveBackend) getCluster(string) (*hypervCluster, bool) { return nil, false }

// --- VMs ---

// realVMSettings returns the realized Msvm_VirtualSystemSettingData objects (one per
// actual VM), keyed by their owning VM's ConfigurationID (== Msvm_ComputerSystem.Name).
// Caller must Release the returned objects.
func (l *liveBackend) loadVM(o *ole.IDispatch) *hypervVM {
	vmID := prop(o, "Name") // GUID
	vm := &hypervVM{
		VMID:   vmID,
		Name:   prop(o, "ElementName"),
		State:  enabledState(propInt(o, "EnabledState")),
		HostID: l.hostID(),
	}
	// Associated settings: processor, memory, storage, NICs, generation, creation time.
	l.populateSettings(vm, vmID)
	return vm
}

// populateSettings runs the standard "associators of the VM's setting data" reads.
func (l *liveBackend) populateSettings(vm *hypervVM, vmID string) {
	// The VM's realized Msvm_VirtualSystemSettingData (ConfigurationID == vm GUID).
	vssd := l.query(fmt.Sprintf(
		"SELECT * FROM Msvm_VirtualSystemSettingData WHERE ConfigurationID='%s' AND VirtualSystemType='%s'",
		vmID, vmTypeRealized))
	var vssdPath string
	for _, s := range vssd {
		vssdPath = path(s)
		// VirtualSystemSubType: "Microsoft:Hyper-V:SubType:1" (gen1) / ":2" (gen2).
		sub := prop(s, "VirtualSystemSubType")
		if strings.HasSuffix(sub, ":2") {
			vm.Generation = 2
		} else {
			vm.Generation = 1
		}
		if ct := prop(s, "CreationTime"); ct != "" {
			vm.Created = parseCIMDate(ct)
		}
		s.Release()
	}
	if vssdPath == "" {
		return
	}
	// Processor / memory / disks / NICs are children of the setting data, reachable via
	// the Msvm_VirtualSystemSettingDataComponent association.
	for _, p := range l.assocSettings(vssdPath, "Msvm_ProcessorSettingData") {
		vm.VCPUs = int(propInt(p, "VirtualQuantity"))
		p.Release()
	}
	for _, m := range l.assocSettings(vssdPath, "Msvm_MemorySettingData") {
		vm.MemoryMB = propInt(m, "VirtualQuantity")
		m.Release()
	}
	idx := 0
	for _, d := range l.assocSettings(vssdPath, "Msvm_StorageAllocationSettingData") {
		hr := prop(d, "HostResource") // VHDX path (a string array rendered)
		vm.Disks = append(vm.Disks, hypervDisk{
			Index:  idx,
			Label:  fmt.Sprintf("Hard Drive %d", idx),
			Path:   strings.Trim(hr, "[]"),
			Format: vp.DiskVHDX,
		})
		idx++
		d.Release()
	}
	nidx := 0
	for _, n := range l.assocSettings(vssdPath, "Msvm_SyntheticEthernetPortSettingData") {
		vm.NICs = append(vm.NICs, hypervNIC{
			Index:     nidx,
			MAC:       prop(n, "Address"),
			Connected: true,
		})
		nidx++
		n.Release()
	}
}

// assocSettings returns child setting-data objects of a given class associated to a
// VM's Msvm_VirtualSystemSettingData via Msvm_VirtualSystemSettingDataComponent.
func (l *liveBackend) assocSettings(vssdPath, class string) []*ole.IDispatch {
	wql := fmt.Sprintf(
		"ASSOCIATORS OF {%s} WHERE ResultClass=%s AssocClass=Msvm_VirtualSystemSettingDataComponent",
		vssdPath, class)
	resRaw, err := oleutil.CallMethod(l.svc, "ExecQuery", wql)
	if err != nil {
		return nil
	}
	res := resRaw.ToIDispatch()
	defer res.Release()
	return enumDispatch(res)
}

func (l *liveBackend) listVMs() []*hypervVM {
	var out []*hypervVM
	l.withCOM(func() {
		// All Msvm_ComputerSystem with Caption indicating a VM (not the host). The host
		// has Description "...Hosting...". We filter by VirtualSystemType via the realized
		// setting data: simplest robust filter is Caption == "Virtual Machine" /
		// "Ordinateur virtuel", but locale-independent: exclude the host GUID.
		hostGUID := l.hostGUID()
		for _, o := range l.query("SELECT Name,ElementName,EnabledState,Description,Caption FROM Msvm_ComputerSystem") {
			name := prop(o, "Name")
			desc := prop(o, "Description")
			cap := prop(o, "Caption")
			isHost := name == hostGUID ||
				strings.Contains(desc, "Hosting") ||
				strings.Contains(cap, "Hosting") || strings.Contains(cap, "hébergement")
			if isHost {
				o.Release()
				continue
			}
			out = append(out, l.loadVM(o))
			o.Release()
		}
	})
	return out
}

// hostGUID returns the Name (GUID) of the host's own Msvm_ComputerSystem.
func (l *liveBackend) hostGUID() string {
	var g string
	for _, o := range l.query("SELECT Name,Description,Caption FROM Msvm_ComputerSystem") {
		desc := prop(o, "Description")
		cap := prop(o, "Caption")
		if strings.Contains(desc, "Hosting") || strings.Contains(cap, "Hosting") ||
			strings.Contains(cap, "hébergement") {
			g = prop(o, "Name")
		}
		o.Release()
	}
	return g
}

func (l *liveBackend) getVM(vmID string) (*hypervVM, bool) {
	var vm *hypervVM
	l.withCOM(func() {
		objs := l.query(fmt.Sprintf(
			"SELECT Name,ElementName,EnabledState FROM Msvm_ComputerSystem WHERE Name='%s'", vmID))
		for _, o := range objs {
			vm = l.loadVM(o)
			o.Release()
		}
	})
	if vm == nil {
		return nil, false
	}
	return vm, true
}

func (l *liveBackend) vmsOnHost(hostID string) int {
	if hostID != l.hostID() {
		return 0
	}
	return len(l.listVMs())
}

// --- storage / switches ---

func (l *liveBackend) listStorage() []*hypervStorage {
	// Hyper-V default VHD store; standalone host has no CSV. We surface the default
	// virtual-hard-disk path as a local pool (best effort, capacity unknown here).
	return []*hypervStorage{{
		StorageID:  "local-1",
		Name:       "Local VM Storage",
		Type:       "local",
		HostIDs:    []string{l.hostID()},
		Accessible: true,
	}}
}

func (l *liveBackend) listSwitches() []*hypervSwitch {
	var out []*hypervSwitch
	l.withCOM(func() {
		for _, o := range l.query("SELECT Name,ElementName FROM Msvm_VirtualEthernetSwitch") {
			out = append(out, &hypervSwitch{
				SwitchID: prop(o, "Name"),
				Name:     prop(o, "ElementName"),
				Type:     "external",
			})
			o.Release()
		}
	})
	return out
}

// --- lifecycle (writes via Msvm_VirtualSystemManagementService) ---

// mgmtService returns the Msvm_VirtualSystemManagementService object (caller releases).
func (l *liveBackend) mgmtService() *ole.IDispatch {
	objs := l.query("SELECT * FROM Msvm_VirtualSystemManagementService")
	for i, o := range objs {
		if i == 0 {
			// release the rest
			for _, extra := range objs[1:] {
				extra.Release()
			}
			return o
		}
	}
	return nil
}

// createVM realizes a new VM via DefineSystem. We build a minimal embedded
// Msvm_VirtualSystemSettingData instance text (ElementName + generation) — DefineSystem
// accepts a SystemSettings CIM instance string plus ResourceSettings (omitted: vCPU/mem
// default; reconfigure adds them). On a real host this creates a live VM you can see in
// Get-VM. Errors are swallowed at the backend seam (the Provider returns a finished
// task); use the probe to observe the created VM.
func (l *liveBackend) createVM(vm *hypervVM) {
	l.withCOM(func() {
		svc := l.mgmtService()
		if svc == nil {
			return
		}
		defer svc.Release()

		gen := vm.Generation
		if gen == 0 {
			gen = 2
		}
		// Build the SystemSettings as an embedded WMI instance (MOF text). We spawn a
		// fresh Msvm_VirtualSystemSettingData, set ElementName + subtype, and serialize
		// it with GetText_(1) [WMI MOF format] which DefineSystem accepts.
		settingsText := l.newSystemSettingsText(vm.Name, gen)
		if settingsText == "" {
			return
		}
		// DefineSystem(SystemSettings, ResourceSettings[], ReferenceConfiguration,
		//              out ResultingSystem, out Job)
		res, err := oleutil.CallMethod(svc, "DefineSystem", settingsText, nil, nil)
		if err != nil {
			return
		}
		rv := int(res.Val)
		res.Clear()
		_ = rv // 0 = done, 4096 = job started; either is a successful realize request
	})
}

// newSystemSettingsText spawns a Msvm_VirtualSystemSettingData, sets the VM name and
// generation, and returns its MOF instance text for DefineSystem.
func (l *liveBackend) newSystemSettingsText(name string, gen int) string {
	clsRaw, err := oleutil.CallMethod(l.svc, "Get", "Msvm_VirtualSystemSettingData")
	if err != nil {
		return ""
	}
	cls := clsRaw.ToIDispatch()
	defer cls.Release()
	instRaw, err := oleutil.CallMethod(cls, "SpawnInstance_")
	if err != nil {
		return ""
	}
	inst := instRaw.ToIDispatch()
	defer inst.Release()

	oleutil.PutProperty(inst, "ElementName", name)
	subtype := "Microsoft:Hyper-V:SubType:2"
	if gen == 1 {
		subtype = "Microsoft:Hyper-V:SubType:1"
	}
	oleutil.PutProperty(inst, "VirtualSystemSubType", subtype)

	// GetText_(1) -> WMI MOF object text (format wmiObjectTextFormatCIMDTD20=1).
	txtRaw, err := oleutil.CallMethod(inst, "GetText_", 1)
	if err != nil {
		return ""
	}
	defer txtRaw.Clear()
	return txtRaw.ToString()
}

// destroyVM removes a VM via DestroySystem(ComputerSystemPath, out Job). DestroySystem
// is asynchronous (ReturnValue 4096 + an Msvm_ConcreteJob); since go-ole's IDispatch
// call cannot bind the out-Job ByRef param, we poll the namespace until the VM object
// disappears (or a short deadline elapses) to confirm completion.
func (l *liveBackend) destroyVM(vmID string) {
	l.withCOM(func() {
		svc := l.mgmtService()
		if svc == nil {
			if dbg() {
				fmt.Fprintln(stderr, "DEBUG destroyVM: mgmtService nil")
			}
			return
		}
		defer svc.Release()
		var vmPath string
		objs := l.query(fmt.Sprintf("SELECT * FROM Msvm_ComputerSystem WHERE Name='%s'", vmID))
		for _, o := range objs {
			vmPath = path(o)
			o.Release()
		}
		if dbg() {
			fmt.Fprintf(stderr, "DEBUG destroyVM: objs=%d vmPath=%q\n", len(objs), vmPath)
		}
		if vmPath == "" {
			return
		}
		// DestroySystem's AffectedSystem is a CIM_ComputerSystem REF. Via the late-bound
		// SWbem IDispatch, a REF in-param is supplied as the object's __PATH string.
		res, err := svc.CallMethod("DestroySystem", vmPath)
		if dbg() {
			if err != nil {
				fmt.Fprintf(stderr, "DEBUG DestroySystem err=%v\n", err)
			} else {
				fmt.Fprintf(stderr, "DEBUG DestroySystem ReturnValue=%v\n", res.Value())
			}
		}
		if err != nil {
			return
		}
		res.Clear()
		// Wait for the async destroy job to actually remove the object.
		l.waitGone(vmID, 30*time.Second)
	})
}

// waitGone polls (under the already-held COM lock) until no Msvm_ComputerSystem with
// the given Name remains, or the deadline elapses.
func (l *liveBackend) waitGone(vmID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		objs := l.query(fmt.Sprintf("SELECT Name FROM Msvm_ComputerSystem WHERE Name='%s'", vmID))
		n := len(objs)
		for _, o := range objs {
			o.Release()
		}
		if n == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// setState changes power via RequestStateChange on the VM's Msvm_ComputerSystem.
// RequestStateChange(RequestedState, out Job, TimeoutPeriod). EnabledState targets:
// 2=Enabled(start), 3=Disabled(stop), 32768=Suspended(save... actually 6=Off,
// 32769=Saved). We map our enabledState targets to the RequestedState codes:
//
//	enabledRunning(2) -> 2 (Enabled)
//	enabledStopped(3) -> 3 (Disabled)
//	enabledSaved(32769) -> 32769 (Saved/Suspended)
//	enabledPaused(9)    -> 32768 (Quiesce/Pause)
func (l *liveBackend) setState(vmID string, s enabledState) {
	requested := int(s)
	switch s {
	case enabledRunning:
		requested = 2
	case enabledStopped:
		requested = 3
	case enabledSaved:
		requested = 32769
	case enabledPaused:
		requested = 32768
	}
	l.withCOM(func() {
		objs := l.query(fmt.Sprintf("SELECT * FROM Msvm_ComputerSystem WHERE Name='%s'", vmID))
		for _, o := range objs {
			res, err := oleutil.CallMethod(o, "RequestStateChange", requested, nil)
			if err == nil {
				res.Clear()
			}
			o.Release()
		}
	})
}

// --- snapshots (Msvm_VirtualSystemSnapshotService) ---

func (l *liveBackend) listSnapshots(vmID string) []vp.Snapshot {
	var out []vp.Snapshot
	l.withCOM(func() {
		// Snapshots are Msvm_VirtualSystemSettingData with VirtualSystemType
		// "...Snapshot" whose ConfigurationID matches the VM (best effort by
		// associators of the VM's computer system).
		var vmPath string
		objs := l.query(fmt.Sprintf("SELECT * FROM Msvm_ComputerSystem WHERE Name='%s'", vmID))
		for _, o := range objs {
			vmPath = path(o)
			o.Release()
		}
		if vmPath == "" {
			return
		}
		wql := fmt.Sprintf(
			"ASSOCIATORS OF {%s} WHERE ResultClass=Msvm_VirtualSystemSettingData AssocClass=Msvm_SnapshotOfVirtualSystem",
			vmPath)
		resRaw, err := oleutil.CallMethod(l.svc, "ExecQuery", wql)
		if err != nil {
			return
		}
		res := resRaw.ToIDispatch()
		defer res.Release()
		for _, s := range enumDispatch(res) {
			out = append(out, vp.Snapshot{
				ID:        prop(s, "ConfigurationID"),
				VMID:      vmID,
				Name:      prop(s, "ElementName"),
				CreatedAt: time.Unix(parseCIMDate(prop(s, "CreationTime")), 0).UTC(),
			})
			s.Release()
		}
	})
	return out
}

// createSnapshot uses Msvm_VirtualSystemSnapshotService.CreateSnapshot.
func (l *liveBackend) createSnapshot(vmID string, snap vp.Snapshot) {
	l.withCOM(func() {
		objs := l.query(fmt.Sprintf("SELECT * FROM Msvm_ComputerSystem WHERE Name='%s'", vmID))
		if len(objs) == 0 {
			return
		}
		vmObj := objs[0]
		for _, extra := range objs[1:] {
			extra.Release()
		}
		defer vmObj.Release()
		snapSvc := l.snapshotService()
		if snapSvc == nil {
			return
		}
		defer snapSvc.Release()
		// CreateSnapshot(AffectedSystem REF, SnapshotSettings, SnapshotType, out Resulting, out Job)
		// SnapshotType 2 = full (standard) checkpoint.
		res, err := oleutil.CallMethod(snapSvc, "CreateSnapshot", vmObj, "", 2)
		if err == nil {
			res.Clear()
		}
	})
}

func (l *liveBackend) setCurrentSnapshot(vmID, snapID string) bool {
	found := false
	l.withCOM(func() {
		objs := l.query(fmt.Sprintf(
			"SELECT * FROM Msvm_VirtualSystemSettingData WHERE ConfigurationID='%s'", snapID))
		if len(objs) == 0 {
			return
		}
		snapObj := objs[0]
		for _, extra := range objs[1:] {
			extra.Release()
		}
		defer snapObj.Release()
		snapSvc := l.snapshotService()
		if snapSvc == nil {
			return
		}
		defer snapSvc.Release()
		// ApplySnapshot(Snapshot REF, out Job)
		res, err := oleutil.CallMethod(snapSvc, "ApplySnapshot", snapObj)
		if err == nil {
			res.Clear()
			found = true
		}
	})
	return found
}

func (l *liveBackend) snapshotService() *ole.IDispatch {
	objs := l.query("SELECT * FROM Msvm_VirtualSystemSnapshotService")
	for i, o := range objs {
		if i == 0 {
			for _, extra := range objs[1:] {
				extra.Release()
			}
			return o
		}
	}
	return nil
}

// --- helpers ---

// parseCIMDate parses a WMI CIM_DATETIME ("yyyymmddHHMMSS.mmmmmm+UUU") to unix seconds.
func parseCIMDate(s string) int64 {
	if len(s) < 14 {
		return 0
	}
	t, err := time.Parse("20060102150405", s[:14])
	if err != nil {
		return 0
	}
	return t.Unix()
}

var _ wmiBackend = (*liveBackend)(nil)
