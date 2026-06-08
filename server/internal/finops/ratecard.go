// Package finops is UniHV's unified cost & rightsizing engine. It estimates the
// running cost of every entity in the UNIFIED inventory — virtual machines AND
// containers, across every hypervisor AND orchestrator — from a single,
// configurable rate card, then surfaces total spend, top spenders and
// rightsizing recommendations (idle / oversized resources with projected
// savings).
//
// This is a differentiator no competitor matches: vCenter/Prism/Proxmox cost
// add-ons stop at VMs on ONE hypervisor; Portainer/Rancher have no cost view at
// all. UniHV prices VMs and containers side by side in one pane because it reads
// the cross-domain inventory aggregator (see internal/inventory).
//
// Everything here is pure: cost is a deterministic function of (rate card,
// allocation, utilization), which makes the whole engine trivially unit-testable
// with crafted fixtures and free of any hypervisor/orchestrator SDK import.
package finops

import "encoding/json"

// RateCard is the configurable price list used to estimate cost. All rates are
// expressed PER HOUR so the engine can scale to any reporting period. A zero
// rate means "do not charge for this dimension" (e.g. storage-only pricing).
type RateCard struct {
	// Currency is an ISO-4217-ish display code (e.g. "USD", "EUR"). Display only.
	Currency string `json:"currency"`
	// VCPUHour is the price of one allocated vCPU for one hour.
	VCPUHour float64 `json:"vcpuHour"`
	// GBRAMHour is the price of one allocated GiB of RAM for one hour.
	GBRAMHour float64 `json:"gbRamHour"`
	// GBStorageMonth is the price of one provisioned GiB of disk for one month
	// (storage is conventionally billed monthly; the engine converts internally).
	GBStorageMonth float64 `json:"gbStorageMonth"`
	// ContainerVCPUHour / ContainerGBRAMHour optionally price containers at a
	// different (usually lower) rate than VMs. When zero, the VM rates apply, so a
	// single set of numbers still prices both domains.
	ContainerVCPUHour  float64 `json:"containerVcpuHour"`
	ContainerGBRAMHour float64 `json:"containerGbRamHour"`
}

// DefaultRateCard is a sensible starting point modeled on typical small-cloud
// list prices. Operators tune it from the FinOps settings panel.
func DefaultRateCard() RateCard {
	return RateCard{
		Currency:           "USD",
		VCPUHour:           0.0400,
		GBRAMHour:          0.0050,
		GBStorageMonth:     0.1000,
		ContainerVCPUHour:  0.0200,
		ContainerGBRAMHour: 0.0030,
	}
}

// HoursPerMonth is the conventional billing month used to amortize the monthly
// storage rate into the engine's hourly basis (730 = 365*24/12).
const HoursPerMonth = 730.0

// Normalize clamps negative rates to zero and folds empty container rates back
// onto the VM rates so a single price set prices both domains. It never mutates
// the receiver.
func (rc RateCard) Normalize() RateCard {
	clamp := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		return v
	}
	out := RateCard{
		Currency:           rc.Currency,
		VCPUHour:           clamp(rc.VCPUHour),
		GBRAMHour:          clamp(rc.GBRAMHour),
		GBStorageMonth:     clamp(rc.GBStorageMonth),
		ContainerVCPUHour:  clamp(rc.ContainerVCPUHour),
		ContainerGBRAMHour: clamp(rc.ContainerGBRAMHour),
	}
	if out.Currency == "" {
		out.Currency = "USD"
	}
	if out.ContainerVCPUHour == 0 {
		out.ContainerVCPUHour = out.VCPUHour
	}
	if out.ContainerGBRAMHour == 0 {
		out.ContainerGBRAMHour = out.GBRAMHour
	}
	return out
}

// storageHour converts the monthly storage rate to an hourly rate.
func (rc RateCard) storageHour() float64 { return rc.GBStorageMonth / HoursPerMonth }

// MarshalRateCard / UnmarshalRateCard persist a rate card as a settings JSON
// blob, falling back to the default when the stored value is empty or invalid so
// the engine always has a usable card.
func MarshalRateCard(rc RateCard) string {
	b, err := json.Marshal(rc)
	if err != nil {
		return ""
	}
	return string(b)
}

// UnmarshalRateCard parses a stored rate card, returning the default (and ok=false)
// when the input is empty or malformed.
func UnmarshalRateCard(s string) (rc RateCard, ok bool) {
	if s == "" {
		return DefaultRateCard(), false
	}
	if err := json.Unmarshal([]byte(s), &rc); err != nil {
		return DefaultRateCard(), false
	}
	return rc.Normalize(), true
}
