// modeled on server/internal/vprovider/kvm/live_libvirt.go (see CASTOR-REUSE.md)
//
// live_records.go translates decoded XAPI records (the real shapes returned by
// VM.get_all_records / host.get_all_records / pool.get_all_records /
// SR.get_all_records / network.get_all_records) into the xapi* model structs the
// pure-Go normalization core (xapi.go) already knows how to normalize into the
// vprovider contract.
package xen

import "strings"

// parseVMRecord maps a XAPI VM record -> xapiVM. Returns nil for control domains and
// snapshot/template VM objects, which the contract does not surface as VMs.
func parseVMRecord(ref string, rec *xmlrpcValue) *xapiVM {
	get := func(k string) string { return rec.field(k).text() }

	isControl := xapiBool(get("is_control_domain"))
	isSnapshot := xapiBool(get("is_a_snapshot"))
	isTemplate := xapiBool(get("is_a_template"))
	if isSnapshot || isTemplate {
		return nil
	}

	v := &xapiVM{
		Ref:        ref,
		UUID:       get("uuid"),
		NameLabel:  get("name_label"),
		PowerState: xapiPowerState(get("power_state")),
		ResidentOn: nonNullRef(get("resident_on")),
		VCPUsMax:   atoiSafe(get("VCPUs_max")),
		MemoryB:    atoi64Safe(get("memory_static_max")),
		HVM:        get("HVM_boot_policy") != "",
		IsControl:  isControl,
	}

	// platform map: firmware=uefi => UEFI.
	if plat := rec.field("platform"); plat != nil {
		if strings.EqualFold(plat.field("firmware").text(), "uefi") {
			v.UEFI = true
		}
	}
	// HVM boot params can also signal uefi.
	if hp := rec.field("HVM_boot_params"); hp != nil {
		if strings.EqualFold(hp.field("firmware").text(), "uefi") {
			v.UEFI = true
		}
	}

	// other_config map -> labels (best-effort).
	if oc := rec.field("other_config"); oc != nil {
		for k, fv := range oc.structMap() {
			if v.Labels == nil {
				v.Labels = map[string]string{}
			}
			v.Labels[k] = fv.text()
		}
	}

	// VBDs / VIFs are reference arrays in the VM record; the contract only needs
	// counts/refs for the summary, so record the refs. Full disk/NIC detail would
	// require VBD.get_record/VDI.get_record round-trips (done lazily by GetVM if
	// needed); the summary keeps the refs so the normalizer renders disks/NICs.
	for _, vb := range refArray(rec.field("VBDs")) {
		v.VBDs = append(v.VBDs, xapiVBD{Ref: vb})
	}
	for _, vi := range refArray(rec.field("VIFs")) {
		v.VIFs = append(v.VIFs, xapiVIF{Ref: vi, Attached: true})
	}
	return v
}

// parseHostRecord maps a XAPI host record -> xapiHost.
func parseHostRecord(ref string, rec *xmlrpcValue) *xapiHost {
	get := func(k string) string { return rec.field(k).text() }
	h := &xapiHost{
		Ref:       ref,
		UUID:      get("uuid"),
		NameLabel: get("name_label"),
		Enabled:   xapiBool(get("enabled")),
		Live:      true, // host_metrics.live requires a follow-up fetch; default live
	}
	// software_version map -> product version text.
	if sv := rec.field("software_version"); sv != nil {
		if pv := sv.field("product_version_text"); pv != nil && pv.text() != "" {
			h.Version = pv.text()
		} else if pb := sv.field("product_brand"); pb != nil {
			h.Version = strings.TrimSpace(pb.text() + " " + sv.field("product_version").text())
		}
	}
	// cpu_info map -> core count + speed.
	if ci := rec.field("cpu_info"); ci != nil {
		h.CPUCount = atoiSafe(ci.field("cpu_count").text())
		h.CPUMHz = atoiSafe(ci.field("speed").text())
	}
	return h
}

// parsePoolRecord maps a XAPI pool record -> xapiPool.
func parsePoolRecord(ref string, rec *xmlrpcValue) *xapiPool {
	get := func(k string) string { return rec.field(k).text() }
	return &xapiPool{
		Ref:       ref,
		UUID:      get("uuid"),
		NameLabel: get("name_label"),
		MasterRef: nonNullRef(get("master")),
		HAEnabled: xapiBool(get("ha_enabled")),
	}
}

// parseSRRecord maps a XAPI SR record -> xapiSR.
func parseSRRecord(ref string, rec *xmlrpcValue) *xapiSR {
	get := func(k string) string { return rec.field(k).text() }
	sr := &xapiSR{
		Ref:        ref,
		UUID:       get("uuid"),
		NameLabel:  get("name_label"),
		Type:       get("type"),
		PhysSizeB:  atoi64Safe(get("physical_size")),
		PhysUtilB:  atoi64Safe(get("physical_utilisation")),
		Shared:     xapiBool(get("shared")),
		Accessible: true,
	}
	// PBDs reference array -> connected host count (best-effort; refs only).
	for range refArray(rec.field("PBDs")) {
		sr.HostRefs = append(sr.HostRefs, "")
	}
	return sr
}

// parseNetworkRecord maps a XAPI network record -> xapiNetwork.
func parseNetworkRecord(ref string, rec *xmlrpcValue) *xapiNetwork {
	get := func(k string) string { return rec.field(k).text() }
	return &xapiNetwork{
		Ref:       ref,
		UUID:      get("uuid"),
		NameLabel: get("name_label"),
		Bridge:    get("bridge"),
		VLAN:      0,
	}
}

// refArray extracts an array of opaque refs (string values), dropping NULL refs.
func refArray(v *xmlrpcValue) []string {
	var out []string
	for _, item := range v.arrayValues() {
		r := item.text()
		if r != "" && r != "OpaqueRef:NULL" {
			out = append(out, r)
		}
	}
	return out
}

// nonNullRef normalizes XAPI's "OpaqueRef:NULL" sentinel to "".
func nonNullRef(ref string) string {
	if ref == "OpaqueRef:NULL" {
		return ""
	}
	return ref
}
