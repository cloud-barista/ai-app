package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The first case is the device this prototype actually deployed on
// 2026-09-01 (aws+us-west-2+g5g.xlarge). Its name, memory and driver version are
// what nvidia-smi printed on that node:
//
//	name, memory.total [MiB], driver_version
//	NVIDIA T4G, 15360 MiB, 595.71.05
//
// The index, UUID, mig.mode.current and pci.bus_id columns are not from that
// capture; the probe asks for a different column set than the deployment did.
// The remaining cases are shapes the driver is documented to produce.
func TestParseNVIDIA(t *testing.T) {
	tests := []struct {
		name        string
		stdout      string
		wantDevices int
		check       func(t *testing.T, devices []AcceleratorDeviceAlias)
	}{
		{
			name:        "observed T4G",
			stdout:      "0, GPU-6f3a1c9e-2b44-4d18-9a70-1c2d3e4f5a6b, NVIDIA T4G, 15360, 595.71.05, N/A, 00000000:00:1E.0\n",
			wantDevices: 1,
			check: func(t *testing.T, d []AcceleratorDeviceAlias) {
				if d[0].Name != "NVIDIA T4G" || d[0].MemoryMiB != 15360 || d[0].DriverVersion != "595.71.05" {
					t.Errorf("device = %+v", d[0])
				}
				if d[0].Vendor != "nvidia" || d[0].Kind != kindGPU {
					t.Errorf("vendor/kind = %q/%q, want nvidia/gpu", d[0].Vendor, d[0].Kind)
				}
				if !d[0].MemoryKnown {
					t.Error("memory should be known")
				}
				if d[0].MIGKnown {
					t.Error("mig.mode.current was N/A, so MIG state is not known")
				}
				if d[0].PCIBusID != "0000:00:1e.0" {
					t.Errorf("pciBusId = %q, want the sysfs form so the PCI join works", d[0].PCIBusID)
				}
			},
		},
		{
			name:        "keeps the GPU- prefix on the uuid",
			stdout:      "0, GPU-abc, NVIDIA L4, 23034, 570.1, Disabled, 00000000:00:1E.0\n",
			wantDevices: 1,
			check: func(t *testing.T, d []AcceleratorDeviceAlias) {
				if d[0].UUID != "GPU-abc" {
					t.Errorf("uuid = %q, want the prefix kept", d[0].UUID)
				}
				if !d[0].MIGKnown || d[0].MIGEnabled {
					t.Errorf("mig = (%v,%v), want known and off", d[0].MIGKnown, d[0].MIGEnabled)
				}
			},
		},
		{
			name:        "N/A memory is unknown, not zero",
			stdout:      "0, GPU-abc, NVIDIA A100, N/A, 570.1, Enabled, 00000000:00:1E.0\n",
			wantDevices: 1,
			check: func(t *testing.T, d []AcceleratorDeviceAlias) {
				if d[0].MemoryKnown {
					t.Error("memory was N/A and must not be reported as known")
				}
				if d[0].MemoryMiB != 0 {
					t.Errorf("memoryMiB = %d, want the zero value left untouched", d[0].MemoryMiB)
				}
				if !d[0].MIGEnabled {
					t.Error("mig.mode.current was Enabled")
				}
			},
		},
		{
			name: "several devices",
			stdout: "0, GPU-a, NVIDIA L4, 23034, 570.1, Disabled, 00000000:00:1E.0\n" +
				"1, GPU-b, NVIDIA L4, 23034, 570.1, Disabled, 00000000:00:1F.0\n",
			wantDevices: 2,
		},
		{
			name:        "empty output is no device",
			stdout:      "\n",
			wantDevices: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			devices := parseNVIDIA(tc.stdout)
			if len(devices) != tc.wantDevices {
				t.Fatalf("got %d devices, want %d", len(devices), tc.wantDevices)
			}
			if tc.check != nil {
				aliases := make([]AcceleratorDeviceAlias, len(devices))
				for i := range devices {
					aliases[i] = AcceleratorDeviceAlias(devices[i])
				}
				tc.check(t, aliases)
			}
		})
	}
}

// The sysfs lines are the shape the probe script writes. The first is the device
// present on the machine this was developed on, read straight out of
// /sys/bus/pci/devices: class 0x030000, vendor 0x10de, device 0x2184, driver
// nvidia. The rest use the vendor and device identifiers the pci.ids database
// carries for those accelerators.
func TestParsePCIInventory(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantVendor string
		wantKind   string
		wantName   string
	}{
		{
			name:       "observed NVIDIA display device",
			line:       "0000:01:00.0 0x030000 0x10de 0x2184 nvidia -",
			wantVendor: "nvidia",
			wantKind:   kindGPU,
			wantName:   "0x2184",
		},
		{
			name:       "Rebellions NPU is a processing accelerator",
			line:       "0000:51:00.0 0x120000 0x1eff 0x1220 rbln -",
			wantVendor: "rebellions",
			wantKind:   kindNPU,
			wantName:   "RBLN-CA22",
		},
		{
			name:       "FuriosaAI RNGD",
			line:       "0000:27:00.0 0x120000 0x1ed2 0x0001 furiosa -",
			wantVendor: "furiosa",
			wantKind:   kindNPU,
			wantName:   "RNGD",
		},
		{
			name:       "AWS Trainium",
			line:       "0000:00:1e.0 0x120000 0x1d0f 0x7164 neuron -",
			wantVendor: "aws",
			wantKind:   kindNPU,
			wantName:   "Trainium",
		},
		{
			name:       "an unknown vendor still reports as an accelerator",
			line:       "0000:03:00.0 0x120000 0xdead 0xbeef - -",
			wantVendor: "0xdead",
			wantKind:   kindNPU,
			wantName:   "0xbeef",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			devices := parsePCIInventory(tc.line + "\n")
			if len(devices) != 1 {
				t.Fatalf("got %d devices, want 1", len(devices))
			}
			d := devices[0]
			if d.Vendor != tc.wantVendor || d.Kind != tc.wantKind || d.Name != tc.wantName {
				t.Errorf("vendor/kind/name = %q/%q/%q, want %q/%q/%q",
					d.Vendor, d.Kind, d.Name, tc.wantVendor, tc.wantKind, tc.wantName)
			}
			if d.Source != sourcePCI {
				t.Errorf("source = %q, want %q so the reading is not mistaken for a driver's", d.Source, sourcePCI)
			}
			if d.MemoryKnown {
				t.Error("the PCI bus never reports memory, so it must not be marked known")
			}
		})
	}
}

func TestParsePCIInventorySkipsNonAccelerators(t *testing.T) {
	// A network card and a bridge sit in classes the probe must not report.
	stdout := "0000:00:02.0 0x020000 0x8086 0x1502 e1000e -\n" +
		"0000:00:1c.0 0x060400 0x8086 0x1901 pcieport -\n"
	if devices := parsePCIInventory(stdout); len(devices) != 0 {
		t.Errorf("got %d devices, want 0: only display and accelerator classes count", len(devices))
	}
}

func TestParsePCIInventoryMarksVirtualFunctions(t *testing.T) {
	stdout := "0000:51:00.0 0x120000 0x1eff 0x1220 rbln -\n" +
		"0000:51:00.1 0x120000 0x1eff 0x1221 rbln vf\n"
	devices := parsePCIInventory(stdout)
	if len(devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(devices))
	}
	if devices[0].VirtualFunction {
		t.Error("the physical function must not be marked as virtual")
	}
	if !devices[1].VirtualFunction {
		t.Error("the virtual function must be marked, or it doubles the device count")
	}
	if got := len(physicalDevices(devices)); got != 1 {
		t.Errorf("physical devices = %d, want 1", got)
	}
}

func TestSplitProbeSections(t *testing.T) {
	stdout := sectionMarker + sectionPCI + "===\n" +
		"0000:01:00.0 0x030000 0x10de 0x2184 nvidia -\n" +
		sectionMarker + sectionNVIDIA + "===\n" +
		"0, GPU-a, NVIDIA L4, 23034, 570.1, Disabled, 00000000:01:00.0\n" +
		sectionMarker + sectionROCm + "===\n" +
		sectionMarker + "end===\n"

	sections := splitProbeSections(stdout)
	if !strings.Contains(sections[sectionPCI], "0x10de") {
		t.Errorf("pci section = %q", sections[sectionPCI])
	}
	if !strings.Contains(sections[sectionNVIDIA], "NVIDIA L4") {
		t.Errorf("nvidia section = %q", sections[sectionNVIDIA])
	}
	if strings.TrimSpace(sections[sectionROCm]) != "" {
		t.Errorf("rocm section should be empty, got %q", sections[sectionROCm])
	}
	if _, ok := sections["end"]; ok {
		t.Error("the end marker must not become a section")
	}
}

// A node still answering the previous single-command probe has no markers at all.
// Reading that as nvidia output keeps it working instead of reporting nothing.
func TestSplitProbeSectionsFallsBackToNVIDIA(t *testing.T) {
	sections := splitProbeSections("0, GPU-a, NVIDIA L4, 23034, 570.1, Disabled\n")
	if !strings.Contains(sections[sectionNVIDIA], "NVIDIA L4") {
		t.Errorf("unmarked output should be read as nvidia, got %v", sections)
	}
}

// The driver and the bus write the same address differently. If they are not
// normalised the join fails and one card is reported twice.
func TestMergeJoinsVendorReadingOntoPCIEntry(t *testing.T) {
	stdout := sectionMarker + sectionPCI + "===\n" +
		"0000:01:00.0 0x030000 0x10de 0x2184 nvidia -\n" +
		sectionMarker + sectionNVIDIA + "===\n" +
		"0, GPU-a, NVIDIA GeForce GTX 1660 Ti, 6144, 610.57.04, Disabled, 00000000:01:00.0\n" +
		sectionMarker + "end===\n"

	devices, sources := readNodeAccelerators(stdout)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1: the bus entry and the driver reading are one card", len(devices))
	}
	d := devices[0]
	if d.Source != sectionNVIDIA {
		t.Errorf("source = %q, want the driver to win where it answered", d.Source)
	}
	if d.Name != "NVIDIA GeForce GTX 1660 Ti" || !d.MemoryKnown || d.MemoryMiB != 6144 {
		t.Errorf("device = %+v, want the driver's name and memory", d)
	}
	if d.KernelDriver != "nvidia" {
		t.Errorf("kernelDriver = %q, want the bus fact kept", d.KernelDriver)
	}
	if len(sources) != 2 {
		t.Errorf("sources = %v, want both pci and nvidia-smi", sources)
	}
}

// This is the case the PCI layer exists for: the card is plugged in and no
// driver ever bound to it, so every vendor tool reports nothing at all.
func TestUnboundDeviceIsReportedNotSilentlyMissing(t *testing.T) {
	stdout := sectionMarker + sectionPCI + "===\n" +
		"0000:51:00.0 0x120000 0x1eff 0x1220 - -\n" +
		sectionMarker + sectionNVIDIA + "===\n" +
		sectionMarker + "end===\n"

	devices, _ := readNodeAccelerators(stdout)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	if devices[0].Name != "RBLN-CA22" || devices[0].Kind != kindNPU {
		t.Errorf("device = %+v, want the NPU named off the bus", devices[0])
	}
	if devices[0].KernelDriver != "" {
		t.Errorf("kernelDriver = %q, want empty: nothing bound", devices[0].KernelDriver)
	}

	report := stubReport(devices)
	findings := compareToExpectation(report, 1)
	if !containsSubstring(findings, "no kernel driver bound") {
		t.Errorf("findings = %v, want the unbound driver called out", findings)
	}
}

func TestParseNPUTableReadsBusAndMemory(t *testing.T) {
	// The column wording is not relied on; the address and the memory pair are.
	section := "NPU  Name        Device   PCI BUS ID     Temp   Power   Memory        Util\n" +
		"0    RBLN-CA22   rbln0    0000:51:00.0   41C    38W     0.00/15.62 GiB  0%\n"

	devices := parseNPUTable(section, sectionRBLN, "rebellions")
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	d := devices[0]
	if d.PCIBusID != "0000:51:00.0" {
		t.Errorf("pciBusId = %q", d.PCIBusID)
	}
	if d.Kind != kindNPU || d.Vendor != "rebellions" {
		t.Errorf("kind/vendor = %q/%q", d.Kind, d.Vendor)
	}
	if !d.MemoryKnown || d.MemoryMiB != 15994 {
		t.Errorf("memory = (%v,%d), want the total of the used/total pair in MiB", d.MemoryKnown, d.MemoryMiB)
	}
}

func TestAcceleratorModeMIGWins(t *testing.T) {
	devices := parseNVIDIA(
		"0, GPU-a, NVIDIA A100, 81920, 570.1, Disabled, 00000000:01:00.0\n" +
			"1, GPU-b, NVIDIA A100, 81920, 570.1, Enabled, 00000000:02:00.0\n")
	if got := acceleratorMode(devices); got != modeMIG {
		t.Errorf("mode = %q, want %q: MIG on any device changes what is observable", got, modeMIG)
	}
}

func TestAcceleratorModeUnknownWithoutDevices(t *testing.T) {
	if got := acceleratorMode(nil); got != modeUnknown {
		t.Errorf("mode = %q, want %q", got, modeUnknown)
	}
}

// The catalog states a class, so an NPU answering where a GPU was asked for is a
// mismatch even when the count and the memory fit.
func TestCompareReportsKindMismatch(t *testing.T) {
	devices := parsePCIInventory("0000:51:00.0 0x120000 0x1eff 0x1220 rbln -\n")
	report := stubReport(devices)
	report.Expected.Type = "gpu"
	report.Expected.MinCount = 1

	if !containsSubstring(compareToExpectation(report, 1), "is a npu, the application asks for gpu") {
		t.Errorf("findings = %v, want the class mismatch reported", compareToExpectation(report, 1))
	}
}

func containsSubstring(findings []string, want string) bool {
	for _, finding := range findings {
		if strings.Contains(finding, want) {
			return true
		}
	}
	return false
}

// nvidia-smi writes a space after each comma and quotes nothing, so the reader
// has to keep working on the unquoted shape it was already reading.
func TestSplitCSVKeepsUnquotedSpacedFields(t *testing.T) {
	fields := splitCSV("0, GPU-abc, NVIDIA L4, 23034, 570.1, Disabled, 00000000:01:00.0")
	want := []string{"0", "GPU-abc", "NVIDIA L4", "23034", "570.1", "Disabled", "00000000:01:00.0"}

	if len(fields) != len(want) {
		t.Fatalf("got %d fields %q, want %d", len(fields), fields, len(want))
	}
	for i := range want {
		if fields[i] != want[i] {
			t.Errorf("field %d = %q, want %q", i, fields[i], want[i])
		}
	}
}

// The document shape is the one AMD's CLI emits: a gpu_data array of per GPU
// objects split into sections, with every measured field carrying its unit as
// {"value": N, "unit": "..."} and an unreadable field written as the plain
// string "N/A" (ROCm/rocm-systems, projects/amdsmi/amdsmi_cli/subcommands/
// static.py and tests/python/unit/gpu/test_cli_static_bus_pcie.py).
func TestParseAMDSMI(t *testing.T) {
	section := `{"gpu_data":[
	  {"asic":{"market_name":"Instinct MI300X","vendor_name":"Advanced Micro Devices, Inc.","device_id":"0x74a1"},
	   "bus":{"bdf":"0000:0C:00.0"},
	   "vram":{"size":{"value":196608,"unit":"MB"}},
	   "driver":{"version":"6.10.5"}}
	]}`

	devices := parseAMDSMI(section)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}

	d := devices[0]
	if d.Name != "Instinct MI300X" || d.Vendor != "amd" || d.Kind != kindGPU {
		t.Errorf("device = %+v", d)
	}
	if d.Source != sectionAMDSMI {
		t.Errorf("source = %q, want %q", d.Source, sectionAMDSMI)
	}
	if d.DriverVersion != "6.10.5" {
		t.Errorf("driverVersion = %q", d.DriverVersion)
	}
	if d.PCIBusID != "0000:0c:00.0" {
		t.Errorf("pciBusId = %q, want the sysfs form so the PCI join works", d.PCIBusID)
	}
	// 196608 is the card's 192 GiB in binary megabytes; read as decimal MB it
	// would come out as 187.5 GiB and disagree with the catalog for no reason.
	if !d.MemoryKnown || d.MemoryMiB != 196608 {
		t.Errorf("memory = (%v,%d), want known and 196608 MiB", d.MemoryKnown, d.MemoryMiB)
	}
}

// An unreadable field is the string "N/A" where a {value,unit} object would be.
// It has to stay unknown: zero here would read as a card with no memory.
func TestParseAMDSMIKeepsNotAvailableUnknown(t *testing.T) {
	section := `{"gpu_data":[
	  {"asic":{"market_name":"N/A"},"bus":{"bdf":"N/A"},
	   "vram":{"size":"N/A"},"driver":{"version":"N/A"}}]}`

	devices := parseAMDSMI(section)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}

	d := devices[0]
	if d.MemoryKnown || d.MemoryMiB != 0 {
		t.Errorf("memory = (%v,%d), want unknown and untouched", d.MemoryKnown, d.MemoryMiB)
	}
	if d.Name != "" || d.DriverVersion != "" || d.PCIBusID != "" {
		t.Errorf("N/A must not survive as a value: %+v", d)
	}
}

// On a node carrying both tools the supported one has to be the reading that
// survives the merge, because rocm-smi takes only critical fixes from ROCm 7.0.
func TestAMDSMIWinsOverROCmSMI(t *testing.T) {
	stdout := sectionMarker + sectionPCI + "===\n" +
		"0000:0c:00.0 0x030000 0x1002 0x74a1 amdgpu -\n" +
		sectionMarker + sectionROCm + "===\n" +
		`{"card0":{"Card Series":"Instinct","PCI Bus":"0000:0C:00.0",` +
		`"VRAM Total Memory (B)":"137438953472"},"system":{"Driver version":"6.7.0"}}` + "\n" +
		sectionMarker + sectionAMDSMI + "===\n" +
		`{"gpu_data":[{"asic":{"market_name":"Instinct MI300X"},"bus":{"bdf":"0000:0C:00.0"},` +
		`"vram":{"size":{"value":196608,"unit":"MB"}},"driver":{"version":"6.10.5"}}]}` + "\n" +
		sectionMarker + "end===\n"

	devices, sources := readNodeAccelerators(stdout)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1: both tools describe the same card", len(devices))
	}
	if devices[0].Source != sectionAMDSMI || devices[0].DriverVersion != "6.10.5" {
		t.Errorf("device = %+v, want the amd-smi reading to win", devices[0])
	}
	if devices[0].KernelDriver != "amdgpu" {
		t.Errorf("kernelDriver = %q, want the bus fact kept", devices[0].KernelDriver)
	}
	if len(sources) != 3 {
		t.Errorf("sources = %v, want pci, rocm-smi and amd-smi all recorded", sources)
	}
}

// The device ids come from AMD's own table, so the two that this checks are
// ones the fixtures in this tree also carry: 0x738c is the MI100 that ROCm's
// enumerator files under Arcturus, and 0x74a1 is an MI300.
func TestParsePCIInventoryNamesAMDCards(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"0000:83:00.0 0x030000 0x1002 0x738c amdgpu -", "Arcturus (gfx908)"},
		{"0000:0c:00.0 0x030000 0x1002 0x74a1 amdgpu -", "Instinct MI300 series (gfx942)"},
		{"0000:07:00.0 0x030000 0x1002 0x7408 amdgpu -", "Aldebaran (gfx90a)"},
	}

	for _, tc := range tests {
		devices := parsePCIInventory(tc.line + "\n")
		if len(devices) != 1 {
			t.Fatalf("got %d devices for %q, want 1", len(devices), tc.line)
		}
		if devices[0].Name != tc.want {
			t.Errorf("name = %q, want %q", devices[0].Name, tc.want)
		}
		if devices[0].Vendor != "amd" {
			t.Errorf("vendor = %q, want amd", devices[0].Vendor)
		}
	}
}

// An id AMD's table does not list still has to report, with the raw identifier
// rather than a guess, because the card is on the bus either way.
func TestParsePCIInventoryKeepsUnlistedAMDDevice(t *testing.T) {
	devices := parsePCIInventory("0000:07:00.0 0x030000 0x1002 0xffff amdgpu -\n")
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}
	if devices[0].Vendor != "amd" || devices[0].Name != "0xffff" {
		t.Errorf("device = %+v, want vendor amd and the raw device id", devices[0])
	}
}

// The table is reproduced from AMD's, so a count guards against a bad import
// silently emptying it.
func TestAMDDeviceTableIsPopulated(t *testing.T) {
	if len(amdDeviceNames) < 200 {
		t.Errorf("amdDeviceNames has %d entries, want the ~210 AMD publishes", len(amdDeviceNames))
	}
	if got := amdDeviceNames[0x744c]; got != "Navi31 (gfx1100)" {
		t.Errorf("0x744c = %q, want the Navi31 label", got)
	}
}

// rocmFixture reads one of the captures in testdata. They are real
// `rocm-smi --json` output from AMD hardware, which this tree has none of, so
// they are the only thing that can tell this parser it works.
func rocmFixture(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", "rocm-smi", name))
	if err != nil {
		t.Fatalf("cannot read fixture: %v", err)
	}

	return string(body)
}

// Every capture has to yield its cards, with the four fields that matter, on
// every ROCm release represented. The expected values are read back out of the
// fixture itself only where they are version dependent; the counts and the
// shapes are written out, so a parser that quietly returns nothing fails here.
func TestParseROCmAgainstRealCaptures(t *testing.T) {
	tests := []struct {
		file      string
		wantCards int
		wantName  string
		wantBus   string
		wantMiB   int
	}{
		{"mi100_rocm571.json", 6, "Arcturus GL-XL [Instinct MI100]", "0000:1e:00.0", 32752},
		{"mi100_rocm602.json", 4, "Arcturus GL-XL [Instinct MI100]", "0000:83:00.0", 32752},
		// 4.30 had no name to give and put the vendor id in the field instead.
		{"rx6700xt_rocm430.json", 1, "", "0000:07:00.0", 12272},
		{"rx6700xt_rocm571.json", 1, "Navi 22 [Radeon RX 6700/6700 XT / 6800M]", "0000:07:00.0", 12272},
		{"rx6700xt_rocm602.json", 1, "Navi 22 [Radeon RX 6700/6700 XT / 6800M]", "0000:07:00.0", 12272},
		{"rx6700xt_rocm612.json", 1, "Navi 22 [Radeon RX 6700/6700 XT / 6800M]", "0000:07:00.0", 12272},
		// This capture carries only Card Model, and its value is a hex id.
		{"vega-10-XT.json", 1, "", "0000:04:00.0", 16368},
		{"vega-20-WKS-GL-XE.json", 1, "Radeon Instinct MI50 32GB", "0000:63:00.0", 32752},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			devices := parseROCm(rocmFixture(t, tc.file))

			if len(devices) != tc.wantCards {
				t.Fatalf("got %d cards, want %d", len(devices), tc.wantCards)
			}

			first := devices[0]
			if first.Vendor != "amd" || first.Kind != kindGPU || first.Source != sectionROCm {
				t.Errorf("vendor/kind/source = %q/%q/%q", first.Vendor, first.Kind, first.Source)
			}
			if first.Name != tc.wantName {
				t.Errorf("name = %q, want %q", first.Name, tc.wantName)
			}
			if first.PCIBusID != tc.wantBus {
				t.Errorf("pciBusId = %q, want %q", first.PCIBusID, tc.wantBus)
			}
			if !first.MemoryKnown || first.MemoryMiB != tc.wantMiB {
				t.Errorf("memory = (%v,%d), want known and %d MiB", first.MemoryKnown, first.MemoryMiB, tc.wantMiB)
			}
			// Every capture carries the driver version in its system section.
			if first.DriverVersion == "" {
				t.Error("driverVersion is empty: it lives in the system section, not on the card")
			}
			// The cards must come back in bus order, not in map order.
			for i, d := range devices {
				if d.Index != i {
					t.Errorf("device %d has index %d: card keys sort numerically", i, d.Index)
				}
			}
		})
	}
}

// A consumer card reports "N/A" for its unique id and a datacenter card reports
// a real one. N/A must not survive as a value: it would become a device
// identity that every such card shares.
func TestParseROCmDropsNotAvailableUniqueID(t *testing.T) {
	consumer := parseROCm(rocmFixture(t, "rx6700xt_rocm612.json"))
	if len(consumer) != 1 {
		t.Fatalf("got %d cards, want 1", len(consumer))
	}
	if consumer[0].UUID != "" {
		t.Errorf("uuid = %q, want empty: the capture says N/A", consumer[0].UUID)
	}

	datacenter := parseROCm(rocmFixture(t, "mi100_rocm602.json"))
	if len(datacenter) == 0 || datacenter[0].UUID == "" {
		t.Error("the MI100 capture carries a real unique id and it must survive")
	}
}

// The key renames are the thing most likely to break silently on a new release,
// so they are pinned against the captures that bracket each one.
func TestParseROCmSurvivesKeyRenames(t *testing.T) {
	// 5.7.1 spells it "GPU ID" and "Card series"; 6.1.2 spells them
	// "Device ID", "Card Series" and adds "Device Name".
	older := parseROCm(rocmFixture(t, "rx6700xt_rocm571.json"))
	newer := parseROCm(rocmFixture(t, "rx6700xt_rocm612.json"))

	if len(older) != 1 || len(newer) != 1 {
		t.Fatalf("got %d and %d cards, want 1 each", len(older), len(newer))
	}
	if older[0].Name == "" || newer[0].Name == "" {
		t.Errorf("names = %q and %q, want both filled across the rename", older[0].Name, newer[0].Name)
	}
	if older[0].PCIBusID != newer[0].PCIBusID {
		t.Errorf("bus = %q and %q, want the same card seen twice", older[0].PCIBusID, newer[0].PCIBusID)
	}
}

// This is why a hex identifier is refused as a name. The Vega capture is real
// output whose only name key holds "0xc1e", and the card sits at the bus
// address that 0x687f, a Vega10 in AMD's table, occupies here. Letting the tool
// overwrite the architecture with its own hex string would make the reading
// worse than the bus alone, which is the opposite of what the merge is for.
func TestROCmHexNameDoesNotOverwriteThePCIName(t *testing.T) {
	stdout := sectionMarker + sectionPCI + "===\n" +
		"0000:04:00.0 0x030000 0x1002 0x687f amdgpu -\n" +
		sectionMarker + sectionROCm + "===\n" +
		rocmFixture(t, "vega-10-XT.json") +
		sectionMarker + "end===\n"

	devices, _ := readNodeAccelerators(stdout)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1: one card seen by the bus and the tool", len(devices))
	}

	d := devices[0]
	if d.Name != "Vega10 (gfx900)" {
		t.Errorf("name = %q, want the architecture the bus named", d.Name)
	}
	// The tool still wins where it actually has something: memory and identity.
	if !d.MemoryKnown || d.MemoryMiB != 16368 {
		t.Errorf("memory = (%v,%d), want the tool's reading", d.MemoryKnown, d.MemoryMiB)
	}
	if d.UUID != "0x2150e7d042a1124" {
		t.Errorf("uuid = %q, want the tool's unique id", d.UUID)
	}
	if d.Source != sectionROCm {
		t.Errorf("source = %q, want the tool to be credited for the reading", d.Source)
	}
}

// rocm-smi's JSON does not end in a newline, so on a node the next marker lands
// on its closing brace. Read naively that line is neither a marker nor useful
// body, and both sections are lost: the tool's output becomes unparseable and
// everything after it is filed under the wrong section.
func TestSplitProbeSectionsRecoversAGluedMarker(t *testing.T) {
	stdout := sectionMarker + sectionROCm + "===\n" +
		`{"card0":{"PCI Bus":"0000:04:00.0"}}` + sectionMarker + sectionRBLN + "===\n" +
		"0  RBLN-CA22  rbln0  0000:51:00.0  41C  38W  0.00/15.62 GiB  0%\n" +
		sectionMarker + "end===\n"

	sections := splitProbeSections(stdout)

	if !strings.Contains(sections[sectionROCm], `"card0"`) {
		t.Errorf("rocm section = %q, want the JSON that preceded the marker", sections[sectionROCm])
	}
	if strings.Contains(sections[sectionROCm], sectionMarker) {
		t.Error("the marker must not survive inside the section body")
	}
	if !strings.Contains(sections[sectionRBLN], "RBLN-CA22") {
		t.Errorf("rbln section = %q, want the line after the glued marker", sections[sectionRBLN])
	}
	if len(parseROCm(sections[sectionROCm])) != 1 {
		t.Error("the recovered section has to parse")
	}
}

// This is the line this machine's nvidia-smi actually printed for the query the
// probe sends. It matters because the driver uses two spellings for a missing
// value in the same output: temperature.memory came back as "N/A" while
// mig.mode.current came back as "[N/A]", and only the bare one used to be
// recognised. A bracketed sentinel read as a value becomes a device name of
// "[N/A]" or a MIG state that looks answered.
func TestParseNVIDIAObservedLineWithBracketedNotAvailable(t *testing.T) {
	line := "0, GPU-05548171-05c7-229a-e00e-59703ed40eb0, NVIDIA GeForce GTX 1660, " +
		"6144, 610.57.04, [N/A], 00000000:01:00.0\n"

	devices := parseNVIDIA(line)
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1", len(devices))
	}

	d := devices[0]
	if d.Name != "NVIDIA GeForce GTX 1660" || d.DriverVersion != "610.57.04" {
		t.Errorf("device = %+v", d)
	}
	if !d.MemoryKnown || d.MemoryMiB != 6144 {
		t.Errorf("memory = (%v,%d), want known and 6144 MiB", d.MemoryKnown, d.MemoryMiB)
	}
	if d.MIGKnown {
		t.Error("[N/A] means the card did not answer, so MIG state is not known")
	}
	if d.PCIBusID != "0000:01:00.0" {
		t.Errorf("pciBusId = %q", d.PCIBusID)
	}
}

func TestIsNotAvailableCoversBothSpellings(t *testing.T) {
	// "Not Found" and the deprecation sentence are both real field values, not
	// errors: the first is what a vGPU host with no CUDA runtime answers for the
	// CUDA version, and the second is what 610.57.04 returned here for
	// display_mode, power_state and graphics_clock.
	for _, field := range []string{
		"N/A", "[N/A]", "n/a", " [Not Supported] ", "[Unknown Error]", "",
		"Not Found", "Requested functionality has been deprecated",
	} {
		if !isNotAvailable(field) {
			t.Errorf("%q should read as no value", field)
		}
	}
	for _, field := range []string{"0", "Disabled", "NVIDIA L4", "6144"} {
		if isNotAvailable(field) {
			t.Errorf("%q is a value and must survive", field)
		}
	}
}

// The probe asks rocm-smi for JSON and drops to CSV when a release does not
// take the flag, so both have to read. The format is decided by the content
// rather than by what was asked for.
func TestParseROCmAcceptsEitherFormat(t *testing.T) {
	csv := "device,Card series,Card vendor,VRAM Total Memory (B),Driver version,PCI Bus\n" +
		`card0,Instinct MI100,"Advanced Micro Devices, Inc.",34342961152,6.7.0,0000:83:00.0` + "\n"

	fromCSV := parseROCm(csv)
	fromJSON := parseROCm(rocmFixture(t, "mi100_rocm602.json"))

	if len(fromCSV) != 1 {
		t.Fatalf("csv gave %d cards, want 1", len(fromCSV))
	}
	if len(fromJSON) == 0 {
		t.Fatal("json gave no cards")
	}

	// The same card described in either format has to come out the same.
	if fromCSV[0].PCIBusID != fromJSON[0].PCIBusID {
		t.Errorf("bus = %q and %q", fromCSV[0].PCIBusID, fromJSON[0].PCIBusID)
	}
	if fromCSV[0].MemoryMiB != fromJSON[0].MemoryMiB {
		t.Errorf("memory = %d and %d MiB", fromCSV[0].MemoryMiB, fromJSON[0].MemoryMiB)
	}
	if fromCSV[0].Source != sectionROCm || fromJSON[0].Source != sectionROCm {
		t.Error("both are rocm-smi readings and must be credited as such")
	}
}

// Whichever format arrives, a name key holding a bare identifier is refused so
// that the architecture the PCI table worked out survives.
func TestParseROCmCSVAlsoRefusesAHexName(t *testing.T) {
	csv := "device,Card series,VRAM Total Memory (B),PCI Bus\n" +
		"card0,0x1002,12868124672,0000:07:00.0\n"

	devices := parseROCm(csv)
	if len(devices) != 1 {
		t.Fatalf("got %d cards, want 1", len(devices))
	}
	if devices[0].Name != "" {
		t.Errorf("name = %q, want empty: 0x1002 is the vendor id, not a name", devices[0].Name)
	}
}

func TestParseROCmOnEmptySection(t *testing.T) {
	if got := parseROCm("   \n"); len(got) != 0 {
		t.Errorf("got %d devices from an empty section, want 0", len(got))
	}
}

// amd-smi writes its cards either under gpu_data or as a bare array, and an
// empty gpu_data is a node with no AMD card rather than a document in the
// other shape. Telling those apart by whether the key is present, not by
// whether it holds anything, is what keeps a cardless node from being retried
// as an array and reported as a parse failure.
func TestParseAMDSMIDocumentShapes(t *testing.T) {
	card := `{"asic":{"market_name":"Instinct MI300X"},"bus":{"bdf":"0000:0C:00.0"},` +
		`"vram":{"size":{"value":196608,"unit":"MB"}},"driver":{"version":"6.10.5"}}`

	tests := []struct {
		name    string
		section string
		want    int
	}{
		{"gpu_data object", `{"gpu_data":[` + card + `]}`, 1},
		{"bare array", `[` + card + `]`, 1},
		{"no card on this node", `{"gpu_data":[]}`, 0},
		{"empty bare array", `[]`, 0},
		{"neither shape", `{"something_else":1}`, 0},
		{"not json at all", `amd-smi: command not found`, 0},
		{"nothing at all", "   \n", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAMDSMI(tc.section)
			if len(got) != tc.want {
				t.Errorf("got %d devices, want %d", len(got), tc.want)
			}
		})
	}
}
