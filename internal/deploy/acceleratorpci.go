package deploy

import (
	"strconv"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// Accelerator kinds. The kind is the class of AI semiconductor, which the vendor
// alone does not decide: a vendor can ship more than one class.
const (
	kindGPU = "gpu"
	kindNPU = "npu"
	kindTPU = "tpu"
)

// sourcePCI names readings taken off the PCI bus rather than from a driver.
const sourcePCI = "pci"

// PCI device classes that can carry an accelerator.
//
// 0x03 is the display controller class, which is where GPUs sit whether or not
// anything is plugged into them. 0x12 is "Processing accelerators", which is
// where inference NPUs sit. Restricting to these two keeps the inventory from
// listing every network card on the node.
const (
	pciClassDisplay     = 0x03
	pciClassAccelerator = 0x12
)

// pciVendor describes one accelerator vendor as the PCI bus identifies it.
type pciVendor struct {
	name string
	// kind is the class this vendor's devices belong to when the PCI class code
	// does not say. It is a default, not an override: a class 0x12 device is an
	// NPU even if its vendor also sells GPUs.
	kind string
	// devices maps a PCI device ID to the model name. A vendor with no entry for
	// a device still gets its name reported, with the raw device ID as the model,
	// because knowing the vendor is already most of the answer.
	devices map[uint64]string
}

// pciVendors is the accelerator vendor table, keyed by PCI vendor ID.
//
// The IDs and model names are the ones in the pci.ids database that pciutils and
// the kernel both use. Kubernetes Node Feature Discovery identifies these same
// devices the same way: the Rebellions NPU operator labels a node from
// feature.node.kubernetes.io/pci-1eff.present, and Furiosa's from
// pci-1200_1ed2.present, which is this class code and this vendor ID.
//
// Only the accelerator vendors are listed. A device from a vendor that is not
// here still appears in the inventory when its PCI class says accelerator; it
// just carries the raw vendor ID instead of a name.
var pciVendors = map[uint64]pciVendor{
	0x10de: {name: "nvidia", kind: kindGPU},
	0x1002: {name: "amd", kind: kindGPU, devices: amdDeviceNames},
	0x8086: {name: "intel", kind: kindGPU},
	0x1eff: {
		name: "rebellions",
		kind: kindNPU,
		devices: map[uint64]string{
			0x1020: "RBLN-CA2", 0x1021: "RBLN-CA2 (VF)",
			0x1110: "RBLN-CA11", 0x1111: "RBLN-CA11 (VF)",
			0x1120: "RBLN-CA12", 0x1121: "RBLN-CA12 (VF)",
			0x1210: "RBLN-CA21", 0x1211: "RBLN-CA21 (VF)",
			0x1220: "RBLN-CA22", 0x1221: "RBLN-CA22 (VF)",
			0x1250: "RBLN-CA25", 0x1251: "RBLN-CA25 (VF)",
			0x2030: "RBLN-CR03",
		},
	},
	0x1ed2: {
		name: "furiosa",
		kind: kindNPU,
		devices: map[uint64]string{
			0x0000: "Warboy", 0x0001: "RNGD", 0x0002: "RNGD-Plus", 0x2222: "RNGD-S",
		},
	},
	0x1da3: {
		name: "intel",
		kind: kindNPU,
		devices: map[uint64]string{
			0x0001: "Goya", 0x0030: "Greco",
			0x1000: "Gaudi", 0x1010: "Gaudi", 0x1020: "Gaudi2",
			0x1060: "Gaudi3", 0x1063: "Gaudi3",
		},
	},
	0x1d0f: {
		name: "aws",
		kind: kindNPU,
		devices: map[uint64]string{
			0x7064: "Inferentia", 0x7164: "Trainium", 0x7264: "Inferentia2",
			0x7364: "Trainium2", 0x7564: "Trainium3", 0x7565: "Trainium3",
		},
	},
	0x1ae0: {name: "google", kind: kindTPU},
	0x1e52: {
		name: "tenstorrent",
		kind: kindNPU,
		devices: map[uint64]string{
			0x401e: "Wormhole", 0xb140: "Blackhole", 0xfaca: "Grayskull",
		},
	},
	0x1d95: {name: "graphcore", kind: kindNPU},
	0x1f56: {name: "sapeon", kind: kindNPU},
	0x1ff4: {
		name: "deepx",
		kind: kindNPU,
		devices: map[uint64]string{
			0x0100: "M1", 0x0101: "M1 H1", 0x0102: "M1 H1 V-NPU",
			0x0110: "M1M", 0x0111: "M1M H1M", 0x0112: "M1M H1M V-NPU",
			0x2001: "VPU",
		},
	},
	0x209f: {name: "mobilint", kind: kindNPU},
	0x1ed5: {name: "moorethreads", kind: kindGPU},
	0xcabc: {name: "cambricon", kind: kindNPU},
}

// parsePCIInventory reads the PCI section of the probe output.
//
// Each line is one device, written by the probe script as
//
//	<bdf> <class> <vendor> <device> <driver> <physfn>
//
// with "-" for a field the node could not supply. The inventory is the only part
// of the probe that needs nothing installed on the node, so it is what separates
// "there is no accelerator here" from "the accelerator is here and its driver
// never bound" - two states a vendor tool reports identically, by saying nothing.
func parsePCIInventory(stdout string) []model.AcceleratorDevice {
	devices := []model.AcceleratorDevice{}

	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		class, err := parseHex(fields[1])
		if err != nil {
			continue
		}
		// The class code is 24 bits: base class, subclass, programming interface.
		// Only the base class decides whether this is an accelerator.
		switch class >> 16 {
		case pciClassDisplay, pciClassAccelerator:
		default:
			continue
		}

		vendorID, err := parseHex(fields[2])
		if err != nil {
			continue
		}
		deviceID, err := parseHex(fields[3])
		if err != nil {
			continue
		}

		device := model.AcceleratorDevice{
			Index:        len(devices),
			Source:       sourcePCI,
			PCIBusID:     fields[0],
			KernelDriver: dashToEmpty(fields[4]),
			Kind:         pciKind(class, vendorID),
			Vendor:       pciVendorName(vendorID),
			Name:         pciModelName(vendorID, deviceID),
			// A PCI entry never carries memory: the bus says what the device is,
			// not how much of it is free. Leaving MemoryKnown false is the point.
			MemoryKnown: false,
		}
		if len(fields) > 5 {
			// physfn is present only on a virtual function, and it points at the
			// physical function the VF was carved from.
			device.VirtualFunction = dashToEmpty(fields[5]) != ""
		}

		devices = append(devices, device)
	}

	return devices
}

// pciKind reports the accelerator class of a device.
//
// The class code wins over the vendor table: a device the bus calls a processing
// accelerator is an NPU whatever else its vendor sells. The vendor is consulted
// only for display-class devices, where the bus cannot tell a GPU from a TPU
// presented as one.
func pciKind(class, vendorID uint64) string {
	if class>>16 == pciClassAccelerator {
		if vendor, ok := pciVendors[vendorID]; ok && vendor.kind != "" {
			return vendor.kind
		}
		return kindNPU
	}
	if vendor, ok := pciVendors[vendorID]; ok && vendor.kind != "" {
		return vendor.kind
	}
	return kindGPU
}

// pciVendorName reports the vendor name, or the raw identifier for a vendor the
// table does not know. The raw form is deliberate: an unrecognised accelerator
// should still be reportable, and a made-up name would be worse than a number.
func pciVendorName(vendorID uint64) string {
	if vendor, ok := pciVendors[vendorID]; ok {
		return vendor.name
	}
	return "0x" + strconv.FormatUint(vendorID, 16)
}

// pciModelName reports the device model, falling back to the raw identifier.
func pciModelName(vendorID, deviceID uint64) string {
	if vendor, ok := pciVendors[vendorID]; ok {
		if name, ok := vendor.devices[deviceID]; ok {
			return name
		}
	}
	return "0x" + strconv.FormatUint(deviceID, 16)
}

// parseHex reads a 0x-prefixed identifier as the kernel writes it in sysfs.
func parseHex(field string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(field), "0x"), 16, 32)
}

// dashToEmpty turns the probe script's placeholder back into an empty value.
func dashToEmpty(field string) string {
	field = strings.TrimSpace(field)
	if field == "-" {
		return ""
	}
	return field
}
