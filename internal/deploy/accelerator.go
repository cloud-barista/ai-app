package deploy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/tumblebug"
)

// probeTimeoutMinutes bounds the remote query. It only reads device state, so it
// either answers in seconds or the node is not reachable.
const probeTimeoutMinutes = 3

// Accelerator modes, in the order they win when more than one signal fits.
const (
	modeMIG       = "mig"
	modeBareMetal = "bare_metal"
	modeUnknown   = "unknown"
)

// ProbeAccelerator reads what the deployed nodes actually carry and compares it
// against what the spec catalog promised.
//
// The catalog is a promise made at planning time from a spec record; this is the
// device answering. They disagree often enough to be worth checking: an
// accelerator image whose driver never bound, a spec whose accelerator fields are
// a vGPU profile rather than a card, a node that came up without the device at all.
func (s *Service) ProbeAccelerator(ctx context.Context, nsID, infraID string, app *model.AppSpec) (*model.AcceleratorReport, error) {
	report := &model.AcceleratorReport{
		InfraID:  infraID,
		Mode:     modeUnknown,
		Devices:  []model.AcceleratorDevice{},
		Findings: []string{},
	}
	if app != nil {
		report.Expected = model.AcceleratorExpectation{
			Type:         app.Accelerator.Type,
			Model:        app.Accelerator.Model,
			MinCount:     app.Accelerator.MinCount,
			MinMemoryGiB: app.Accelerator.MinMemoryGiB,
		}
	}

	req := &tumblebug.InfraCmdReq{
		Command:        []string{acceleratorProbeCommand},
		UserName:       probeUserName(app),
		TimeoutMinutes: probeTimeoutMinutes,
	}
	results, err := s.tumblebug.RunCommand(ctx, nsID, infraID, req)
	if err != nil {
		return nil, err
	}

	sources := map[string]bool{}
	for _, node := range results.Results {
		devices, nodeSources := readNodeAccelerators(node.Stdout["0"])
		for _, source := range nodeSources {
			sources[source] = true
		}
		if len(devices) == 0 {
			report.Findings = append(report.Findings, fmt.Sprintf(
				"%s: nothing on the PCI bus looks like an accelerator and no vendor tool answered", node.NodeID))
			continue
		}
		for i := range devices {
			devices[i].NodeID = node.NodeID
		}
		report.Devices = append(report.Devices, devices...)
	}

	report.Probed = true
	report.Sources = sortedKeys(sources)
	report.Mode = acceleratorMode(report.Devices)
	report.Findings = append(report.Findings, compareToExpectation(report, len(results.Results))...)
	report.Message = probeMessage(report)

	log.Info().Str("nsId", nsID).Str("infraId", infraID).Str("mode", report.Mode).
		Strs("sources", report.Sources).
		Int("devices", len(report.Devices)).Int("findings", len(report.Findings)).
		Msg("Probed accelerator")

	return report, nil
}

// readNodeAccelerators turns one node's probe output into its device list.
//
// The PCI inventory is the spine and the vendor readings are grafted onto it by
// bus address. That direction matters: a vendor tool only reports devices whose
// driver is loaded, so taking the vendor list as the spine would drop exactly the
// device this probe is meant to catch - present, expensive, and unusable because
// its driver never bound.
func readNodeAccelerators(stdout string) (devices []model.AcceleratorDevice, sources []string) {
	sections := splitProbeSections(stdout)
	answered := map[string]bool{}

	record := func(name string, parsed []model.AcceleratorDevice) []model.AcceleratorDevice {
		if len(parsed) > 0 {
			answered[name] = true
		}
		return parsed
	}

	inventory := record(sectionPCI, parsePCIInventory(sections[sectionPCI]))

	readings := make([]model.AcceleratorDevice, 0, 8)
	readings = append(readings, record(sectionNVIDIA, parseNVIDIA(sections[sectionNVIDIA]))...)
	readings = append(readings, record(sectionROCm, parseROCm(sections[sectionROCm]))...)
	// amd-smi is appended after rocm-smi because the merge lets the later
	// reading win, and on a node carrying both the supported tool should be the
	// one that survives. rocm-smi takes only critical fixes from ROCm 7.0.
	readings = append(readings, record(sectionAMDSMI, parseAMDSMI(sections[sectionAMDSMI]))...)
	readings = append(readings, record(sectionHL, parseHLSMI(sections[sectionHL]))...)
	readings = append(readings, record(sectionRBLN,
		parseNPUTable(sections[sectionRBLN], sectionRBLN, "rebellions"))...)
	readings = append(readings, record(sectionFuriosa,
		parseNPUTable(sections[sectionFuriosa], sectionFuriosa, "furiosa"))...)
	readings = append(readings, record(sectionTPU, parseTPUInfo(sections[sectionTPU]))...)

	return mergeAccelerators(inventory, readings), sortedKeys(answered)
}

// mergeAccelerators joins the vendor readings onto the PCI inventory.
//
// The bus address is the join key because it is the only identifier both sides
// carry. A reading with no address, and a reading whose address is on no listed
// device, is kept as its own entry rather than dropped: a Cloud TPU is not on a
// bus the guest can enumerate, and dropping it would report the node as empty.
func mergeAccelerators(inventory, readings []model.AcceleratorDevice) []model.AcceleratorDevice {
	merged := make([]model.AcceleratorDevice, len(inventory))
	copy(merged, inventory)

	byBusID := make(map[string]int, len(merged))
	for i, device := range merged {
		if device.PCIBusID != "" {
			byBusID[device.PCIBusID] = i
		}
	}

	for _, reading := range readings {
		index, found := -1, false
		if reading.PCIBusID != "" {
			index, found = byBusID[reading.PCIBusID]
		}
		if !found {
			merged = append(merged, reading)
			continue
		}

		// The vendor reading wins wherever it has something to say, because it
		// comes from the driver that owns the device. The inventory keeps the two
		// things only the bus knows: which kernel driver bound, and whether this
		// is a virtual function.
		target := &merged[index]
		target.Source = reading.Source
		target.Index = reading.Index
		if reading.UUID != "" {
			target.UUID = reading.UUID
		}
		if reading.Name != "" {
			target.Name = reading.Name
		}
		if reading.Vendor != "" {
			target.Vendor = reading.Vendor
		}
		if reading.Kind != "" {
			target.Kind = reading.Kind
		}
		if reading.DriverVersion != "" {
			target.DriverVersion = reading.DriverVersion
		}
		if reading.MemoryKnown {
			target.MemoryMiB = reading.MemoryMiB
			target.MemoryKnown = true
		}
		if reading.MIGKnown {
			target.MIGEnabled = reading.MIGEnabled
			target.MIGKnown = true
		}
	}

	return merged
}

// acceleratorMode reports how the accelerator is presented to this node.
//
// MIG wins when any device reports it, because MIG changes what can be measured
// and that has to surface even on a mixed node. Passthrough and bare metal look
// the same from inside a guest, so they are not split here; the hypervisor is the
// only place that can tell them apart.
func acceleratorMode(devices []model.AcceleratorDevice) string {
	if len(devices) == 0 {
		return modeUnknown
	}
	for _, d := range devices {
		if d.MIGKnown && d.MIGEnabled {
			return modeMIG
		}
	}
	return modeBareMetal
}

// physicalDevices drops the SR-IOV virtual functions.
//
// A virtualisation host lists a physical function and every function carved out
// of it. Counting all of them against a per-node device floor would report eight
// accelerators on a node that has one.
func physicalDevices(devices []model.AcceleratorDevice) []model.AcceleratorDevice {
	physical := make([]model.AcceleratorDevice, 0, len(devices))
	for _, device := range devices {
		if device.VirtualFunction {
			continue
		}
		physical = append(physical, device)
	}
	return physical
}

// compareToExpectation reports where the device disagrees with the catalog.
func compareToExpectation(report *model.AcceleratorReport, nodeCount int) []string {
	findings := make([]string, 0, 4)
	expected := report.Expected
	physical := physicalDevices(report.Devices)

	// A device the bus listed but no driver claimed is worth saying whatever the
	// catalog asked for: the node is up and billing, and the accelerator on it
	// cannot be used until someone notices.
	for _, d := range report.Devices {
		if d.Source != sourcePCI {
			continue
		}
		if d.KernelDriver == "" {
			findings = append(findings, fmt.Sprintf(
				"%s is on the bus with no kernel driver bound, so the node is billing for a device nothing can use",
				d.Label()))
			continue
		}
		findings = append(findings, fmt.Sprintf(
			"%s was read from the PCI bus through the %q driver, so its memory and utilization are not available here",
			d.Label(), d.KernelDriver))
	}

	if expected.Type == "" {
		return findings
	}
	if len(physical) == 0 {
		findings = append(findings, fmt.Sprintf(
			"the application asks for %s but no accelerator answered on any node", expected.Type))
		return findings
	}

	if expected.MinCount > 0 && nodeCount > 0 {
		perNode := len(physical) / nodeCount
		if perNode < expected.MinCount {
			findings = append(findings, fmt.Sprintf(
				"the application asks for %d accelerators per node, the nodes report %d",
				expected.MinCount, perNode))
		}
	}

	// The catalog states a class, not a vendor, so a node answering with a
	// different class is a mismatch even when the count and the memory fit.
	for _, d := range physical {
		if d.Kind != "" && !strings.EqualFold(d.Kind, expected.Type) {
			findings = append(findings, fmt.Sprintf(
				"%s is a %s, the application asks for %s", d.Label(), d.Kind, expected.Type))
		}
		if expected.MinMemoryGiB > 0 && d.MemoryKnown {
			if gib := float64(d.MemoryMiB) / 1024; gib+0.5 < expected.MinMemoryGiB {
				findings = append(findings, fmt.Sprintf(
					"%s reports %.1f GiB, the application asks for at least %g GiB",
					d.Label(), gib, expected.MinMemoryGiB))
			}
		}
		if expected.Model != "" && d.Name != "" &&
			!strings.Contains(strings.ToLower(d.Name), strings.ToLower(expected.Model)) {
			findings = append(findings, fmt.Sprintf(
				"%s is a %q, the application names %q", d.Label(), d.Name, expected.Model))
		}
	}

	if report.Mode == modeMIG {
		findings = append(findings, "MIG is on, so the driver reports no device level utilization here; "+
			"per instance utilization comes from the hypervisor, not from this node")
	}
	return findings
}

func probeMessage(report *model.AcceleratorReport) string {
	physical := physicalDevices(report.Devices)
	switch {
	case len(physical) == 0:
		return "No accelerator answered on the deployed nodes"
	case len(report.Findings) == 0:
		return fmt.Sprintf("%d accelerator(s) match what the application asked for", len(physical))
	default:
		return fmt.Sprintf("%d accelerator(s) found, %d finding(s) to look at",
			len(physical), len(report.Findings))
	}
}

func probeUserName(app *model.AppSpec) string {
	if app != nil && app.Install.UserName != "" {
		return app.Install.UserName
	}
	return ""
}

// sortedKeys returns the set's members in a stable order, so that two runs over
// the same node produce the same report.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
