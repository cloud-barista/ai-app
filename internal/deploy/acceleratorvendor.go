package deploy

import (
	"encoding/csv"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// notAvailable lists what a driver prints instead of a value it cannot report.
//
// None of them is zero. Writing any of them down as zero reads as "idle" or "no
// memory", and a reader cannot tell that apart from a real measurement.
//
// nvidia-smi uses two spellings in the same line, which is why the bracketed
// form is here as well as the bare one. Measured on this machine, one query
// returned both: temperature.memory as "N/A" and mig.mode.current as "[N/A]".
// "not found" is what a vGPU host with no CUDA runtime answers for the CUDA
// version, and the deprecation notice is a whole sentence a driver returns
// where a reading belongs: 610.57.04 on this machine puts it in display_mode,
// power_state and graphics_clock. Neither is an error, and neither is a value.
var notAvailable = []string{
	"n/a",
	"not supported",
	"not found",
	"unknown error",
	"insufficient permissions",
	"requested functionality has been deprecated",
}

// bdfPattern matches a PCI address in either the four digit form sysfs writes or
// the eight digit form nvidia-smi and the NPU tools write.
var bdfPattern = regexp.MustCompile(`[0-9a-fA-F]{4,8}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]`)

// memoryPattern matches a memory figure with its unit, as the NPU tools print it
// inside a used/total pair.
var memoryPattern = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(MiB|GiB|MB|GB)`)

// parseNVIDIA reads the headerless CSV from nvidia-smi --query-gpu.
//
// The column order is fixed by the query in acceleratorProbeCommand:
// index, uuid, name, memory.total, driver_version, mig.mode.current, pci.bus_id.
func parseNVIDIA(section string) []model.AcceleratorDevice {
	devices := []model.AcceleratorDevice{}

	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := splitCSV(line)
		if len(fields) < 6 {
			continue
		}

		device := model.AcceleratorDevice{
			Source: sectionNVIDIA,
			Kind:   kindGPU,
			Vendor: "nvidia",
			// The UUID keeps its "GPU-" prefix. Stripping it splits one card into
			// two identities wherever the prefixed form is also used as a join key.
			UUID:          valueOrEmpty(fields[1]),
			Name:          valueOrEmpty(fields[2]),
			DriverVersion: valueOrEmpty(fields[4]),
		}
		if index, err := strconv.Atoi(fields[0]); err == nil {
			device.Index = index
		}
		if mib, ok := parseNumber(fields[3]); ok {
			device.MemoryMiB = int(mib)
			device.MemoryKnown = true
		}
		if mig, ok := parseMIGMode(fields[5]); ok {
			device.MIGEnabled = mig
			device.MIGKnown = true
		}
		if len(fields) > 6 {
			device.PCIBusID = normalizeBusID(valueOrEmpty(fields[6]))
		}

		devices = append(devices, device)
	}

	return devices
}

// parseHLSMI reads Intel Gaudi's hl-smi, which answers in the same headerless CSV
// shape as nvidia-smi but has no MIG column.
func parseHLSMI(section string) []model.AcceleratorDevice {
	devices := []model.AcceleratorDevice{}

	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := splitCSV(line)
		if len(fields) < 5 {
			continue
		}

		device := model.AcceleratorDevice{
			Source:        sectionHL,
			Kind:          kindNPU,
			Vendor:        "intel",
			UUID:          valueOrEmpty(fields[1]),
			Name:          valueOrEmpty(fields[2]),
			DriverVersion: valueOrEmpty(fields[4]),
		}
		if index, err := strconv.Atoi(fields[0]); err == nil {
			device.Index = index
		}
		if mib, ok := parseNumber(fields[3]); ok {
			device.MemoryMiB = int(mib)
			device.MemoryKnown = true
		}
		if len(fields) > 5 {
			device.PCIBusID = normalizeBusID(valueOrEmpty(fields[5]))
		}

		devices = append(devices, device)
	}

	return devices
}

// parseROCmCSV reads `rocm-smi ... --csv`, the fallback format.
//
// Columns are looked up by header name rather than by position: rocm-smi
// changes both the order and the wording between releases, and a positional
// read would put a driver version in the memory field without erroring. The
// fields themselves go through a real CSV reader, because the vendor name
// carries a comma and is therefore quoted.
//
// Unlike the JSON path this one has no captured output to test against, so it
// stays the fallback rather than the first choice.
func parseROCmCSV(section string) []model.AcceleratorDevice {
	devices := []model.AcceleratorDevice{}

	var header []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := splitCSV(line)
		if header == nil {
			if len(fields) > 1 && strings.EqualFold(fields[0], "device") {
				header = fields
			}

			continue
		}
		if len(fields) < 2 || !strings.HasPrefix(strings.ToLower(fields[0]), "card") {
			continue
		}

		column := func(want ...string) string {
			for i, name := range header {
				if i >= len(fields) {
					break
				}
				lowered := strings.ToLower(name)
				for _, w := range want {
					if strings.Contains(lowered, w) {
						return strings.TrimSpace(fields[i])
					}
				}
			}

			return ""
		}

		device := model.AcceleratorDevice{
			Source:        sectionROCm,
			Kind:          kindGPU,
			Vendor:        "amd",
			Index:         len(devices),
			UUID:          valueOrEmpty(column("unique id", "uuid")),
			Name:          valueOrEmpty(column("card series", "card model", "product name")),
			DriverVersion: valueOrEmpty(column("driver version")),
			PCIBusID:      normalizeBusID(valueOrEmpty(column("pci bus", "bus"))),
		}
		// The same refusal as the JSON path: an identifier is not a name, and
		// passing it on would overwrite what the PCI table already worked out.
		if isHexIdentifier(device.Name) {
			device.Name = ""
		}
		// rocm-smi counts VRAM in bytes, unlike every other tool here.
		if bytes, ok := parseNumber(column("vram total memory")); ok && bytes > 0 {
			device.MemoryMiB = int(bytes / (1024 * 1024))
			device.MemoryKnown = true
		}

		devices = append(devices, device)
	}

	return devices
}

// rocmCardPattern matches the keys rocm-smi uses for each card in its JSON.
// Everything else at the top level is a section, and "system" is the one that
// carries the driver version.
var rocmCardPattern = regexp.MustCompile(`^card(\d+)$`)

// rocm-smi labels its JSON keys with the strings it prints in table mode, and
// it has renamed them across releases: "GPU ID" became "Device ID" in 6.02,
// and 6.12 both recased "Card series" to "Card Series" and added "Device Name".
// Lookups are case insensitive, so only genuine renames need an entry here,
// listed in the order they should be preferred.
var (
	rocmNameKeys     = []string{"device name", "card series", "card model", "card sku"}
	rocmUniqueIDKeys = []string{"unique id", "guid"}
	rocmBusKeys      = []string{"pci bus"}
	rocmVRAMKeys     = []string{"vram total memory (b)"}
	rocmDriverKeys   = []string{"driver version"}
)

// parseROCm reads whichever of rocm-smi's two output formats arrived.
//
// Both are accepted because the probe asks for JSON and falls back to CSV, and
// a release that rejects one flag should cost the reading nothing. The format
// is decided by the first character rather than by what was asked for, so the
// parser stays right even if the shell's fallback changes.
func parseROCm(section string) []model.AcceleratorDevice {
	trimmed := strings.TrimSpace(section)
	if trimmed == "" {
		return []model.AcceleratorDevice{}
	}

	if strings.HasPrefix(trimmed, "{") {
		return parseROCmJSON(trimmed)
	}

	return parseROCmCSV(trimmed)
}

// parseROCmJSON reads `rocm-smi ... --json`.
//
// The document is an object keyed by card, plus a "system" section holding the
// driver version, which is reported once for the host rather than per card.
//
// This is the format the probe asks for first: real captures of it exist to
// test against across four ROCm releases, and none exist for the CSV.
func parseROCmJSON(section string) []model.AcceleratorDevice {
	devices := []model.AcceleratorDevice{}
	trimmed := strings.TrimSpace(section)

	// Every value in this document is a string, including the numbers.
	var report map[string]map[string]string
	if err := json.Unmarshal([]byte(trimmed), &report); err != nil {
		return devices
	}

	driver := ""
	if system, ok := report["system"]; ok {
		driver = valueOrEmpty(lookupFold(system, rocmDriverKeys...))
	}

	// Card keys sort numerically, not as text: card10 comes after card9.
	indexes := make([]int, 0, len(report))
	byIndex := make(map[int]map[string]string, len(report))
	for key, body := range report {
		match := rocmCardPattern.FindStringSubmatch(strings.ToLower(key))
		if match == nil {
			continue
		}
		index, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		indexes = append(indexes, index)
		byIndex[index] = body
	}
	sort.Ints(indexes)

	for _, index := range indexes {
		card := byIndex[index]

		device := model.AcceleratorDevice{
			Source:        sectionROCm,
			Kind:          kindGPU,
			Vendor:        "amd",
			Index:         index,
			Name:          valueOrEmpty(lookupFold(card, rocmNameKeys...)),
			UUID:          valueOrEmpty(lookupFold(card, rocmUniqueIDKeys...)),
			DriverVersion: driver,
			PCIBusID:      normalizeBusID(valueOrEmpty(lookupFold(card, rocmBusKeys...))),
		}
		// Older releases fill the name keys with a bare identifier rather than
		// a name: 4.30 puts the vendor id "0x1002" in Card Series, and the Vega
		// capture has only Card Model, "0xc1e". Passing that on would overwrite
		// the architecture the PCI table already named the card with, so an
		// answer that is only a hex number is treated as no answer.
		if isHexIdentifier(device.Name) {
			device.Name = ""
		}
		// rocm-smi counts VRAM in bytes, unlike every other tool here.
		if bytes, ok := parseNumber(lookupFold(card, rocmVRAMKeys...)); ok && bytes > 0 {
			device.MemoryMiB = int(bytes / (1024 * 1024))
			device.MemoryKnown = true
		}

		devices = append(devices, device)
	}

	return devices
}

// hexIdentifier matches a bare hex value, which is what rocm-smi puts in its
// name keys when it has no name to give.
var hexIdentifier = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)

func isHexIdentifier(value string) bool {
	return hexIdentifier.MatchString(strings.TrimSpace(value))
}

// lookupFold returns the first of the wanted keys present in the section,
// ignoring case. The keys are already lowercase at the call sites.
func lookupFold(section map[string]string, wanted ...string) string {
	folded := make(map[string]string, len(section))
	for key, value := range section {
		folded[strings.ToLower(strings.TrimSpace(key))] = value
	}

	for _, want := range wanted {
		if value, ok := folded[want]; ok && strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

// parseNPUTable reads the text tables the NPU tools print.
//
// rbln-stat and furiosa-smi both lay out one device per row with a PCI address in
// it, but neither has a documented stable column order and neither offers a
// machine format this probe can rely on across releases. So the row is scanned
// for the things that are unambiguous wherever they sit - the PCI address, the
// device index, a memory figure with its unit - and anything else is left to the
// PCI inventory, which already named the card.
//
// The result of a parse that finds nothing is not an error: the inventory has the
// device either way, and what is lost is the memory reading, which the report says
// is unknown rather than guessing at it.
func parseNPUTable(section, source, vendor string) []model.AcceleratorDevice {
	devices := []model.AcceleratorDevice{}

	for _, line := range strings.Split(section, "\n") {
		busID := bdfPattern.FindString(line)
		if busID == "" {
			continue
		}

		device := model.AcceleratorDevice{
			Source:   source,
			Kind:     kindNPU,
			Vendor:   vendor,
			Index:    len(devices),
			PCIBusID: normalizeBusID(busID),
		}
		if index, ok := parseDeviceIndex(line); ok {
			device.Index = index
		}
		if mib, ok := parseLargestMemory(line); ok {
			device.MemoryMiB = mib
			device.MemoryKnown = true
		}

		devices = append(devices, device)
	}

	return devices
}

// parseTPUInfo reads the tpu-info listing.
//
// A Cloud TPU is not on a PCI bus the guest can enumerate the way an NPU card is,
// so unlike the NPU tables this is the only source for the device and a row
// without a PCI address still counts.
func parseTPUInfo(section string) []model.AcceleratorDevice {
	devices := []model.AcceleratorDevice{}

	for _, line := range strings.Split(section, "\n") {
		lowered := strings.ToLower(line)
		if !strings.Contains(lowered, "chip") && !strings.Contains(lowered, "device") {
			continue
		}
		index, ok := parseDeviceIndex(line)
		if !ok {
			continue
		}

		device := model.AcceleratorDevice{
			Source: sectionTPU,
			Kind:   kindTPU,
			Vendor: "google",
			Index:  index,
			Name:   parseTPUAccelerator(line),
		}
		if mib, ok := parseLargestMemory(line); ok {
			device.MemoryMiB = mib
			device.MemoryKnown = true
		}

		devices = append(devices, device)
	}

	return devices
}

// tpuGeneration matches the accelerator names Cloud TPU uses, for example v5e.
var tpuGeneration = regexp.MustCompile(`(?i)\bv\d+[a-z]*\b`)

func parseTPUAccelerator(line string) string {
	if match := tpuGeneration.FindString(line); match != "" {
		return "TPU " + strings.ToLower(match)
	}
	return ""
}

// deviceIndexPattern matches the device labels the NPU and TPU tools print.
var deviceIndexPattern = regexp.MustCompile(`(?i)\b(?:npu|device|chip|card)\s*#?(\d+)\b`)

func parseDeviceIndex(line string) (int, bool) {
	match := deviceIndexPattern.FindStringSubmatch(line)
	if match == nil {
		return 0, false
	}
	index, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return index, true
}

// parseLargestMemory reads the biggest memory figure on a row, in MiB.
//
// These tools print memory as a used/total pair, and the total is the larger of
// the two. Taking the larger is what makes the reading comparable to the memory
// floor the catalog states, which is a capacity and not a usage.
func parseLargestMemory(line string) (int, bool) {
	matches := memoryPattern.FindAllStringSubmatch(line, -1)
	if matches == nil {
		return 0, false
	}

	largest := 0
	for _, match := range matches {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(match[2]) {
		case "gib":
			value *= 1024
		case "gb":
			value = value * 1000 * 1000 * 1000 / (1024 * 1024)
		case "mb":
			value = value * 1000 * 1000 / (1024 * 1024)
		}
		if int(value) > largest {
			largest = int(value)
		}
	}

	if largest == 0 {
		return 0, false
	}
	return largest, true
}

// splitCSV reads one comma separated line the way the emitting tool wrote it.
//
// A field is quoted whenever it contains a comma, and at least one of these
// tools has such a field: AMD's own CLI tests note that the vendor name carries
// a comma on some backends and read their output with a real CSV reader for
// that reason. Splitting on every comma instead shifts every later column by
// one, which does not fail - it silently puts the byte count in the driver
// version and the driver version where the bus address should be, and the bus
// address is the key the PCI join runs on, so the card ends up reported twice.
//
// LazyQuotes and a free field count keep a malformed line from failing the
// whole reading: this is an inventory of whatever answered, not a parser with a
// schema to enforce.
func splitCSV(line string) []string {
	reader := csv.NewReader(strings.NewReader(line))
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1

	fields, err := reader.Read()
	if err != nil {
		return nil
	}

	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}

	return fields
}

// valueOrEmpty drops a value the driver could not report.
func valueOrEmpty(field string) string {
	if isNotAvailable(field) {
		return ""
	}

	return strings.TrimSpace(field)
}

// isNotAvailable reports whether a field holds one of the driver's stand-ins
// for a reading it does not have, in either the bare or the bracketed spelling.
func isNotAvailable(field string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(field))
	trimmed = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")

	if trimmed == "" {
		return true
	}
	for _, sentinel := range notAvailable {
		if trimmed == sentinel {
			return true
		}
	}

	return false
}

// parseNumber reads a numeric field, reporting false for one the driver could not
// report. The caller records that as unknown rather than as zero.
func parseNumber(field string) (float64, bool) {
	if isNotAvailable(field) {
		return 0, false
	}
	field = strings.TrimSpace(field)
	value, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// parseMIGMode reads mig.mode.current. A card that does not support MIG answers
// N/A, which is not the same as MIG being off.
func parseMIGMode(field string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "enabled":
		return true, true
	case "disabled":
		return false, true
	default:
		return false, false
	}
}

// amdSMIReport is what `amd-smi static ... --json` writes.
//
// The document is either an object holding gpu_data or a bare array; AMD's own
// CLI tests accept both, so this does too.
type amdSMIReport struct {
	// A pointer so that a document carrying an empty gpu_data can be told from
	// one carrying no such key. The first is a node with no AMD card, which is
	// an answer; the second means this is the bare array form instead.
	GPUData *[]amdSMIDevice `json:"gpu_data"`
}

// amdSMIDevice is one GPU, split into the sections the static subcommand emits.
// Only the sections the probe asks for are modelled; the rest are ignored rather
// than rejected, so a newer amd-smi that adds a section still parses.
type amdSMIDevice struct {
	ASIC struct {
		MarketName string `json:"market_name"`
		VendorName string `json:"vendor_name"`
		DeviceID   string `json:"device_id"`
	} `json:"asic"`
	Bus struct {
		BDF string `json:"bdf"`
	} `json:"bus"`
	VRAM struct {
		Size amdSMIValue `json:"size"`
	} `json:"vram"`
	Driver struct {
		Version string `json:"version"`
	} `json:"driver"`
}

// amdSMIValue is a measured field in amd-smi's JSON, which carries its unit
// rather than being a bare number: {"value": 196608, "unit": "MB"}. A field the
// driver could not read is the plain string "N/A" instead, so the unit and the
// absence have to be decoded from the same position.
type amdSMIValue struct {
	Value   float64
	Unit    string
	Present bool
}

func (v *amdSMIValue) UnmarshalJSON(data []byte) error {
	var wrapped struct {
		Value *float64 `json:"value"`
		Unit  string   `json:"unit"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Value != nil {
		v.Value, v.Unit, v.Present = *wrapped.Value, wrapped.Unit, true

		return nil
	}

	// A bare number is accepted because the same key is a plain value in
	// csv and human modes, and a caller may feed either document in.
	var bare float64
	if err := json.Unmarshal(data, &bare); err == nil {
		v.Value, v.Present = bare, true

		return nil
	}

	// Anything else, "N/A" included, stays absent rather than becoming zero.
	return nil
}

// parseAMDSMI reads `amd-smi static --asic --bus --vram --driver --json`.
//
// amd-smi is the supported successor to rocm-smi, which takes only critical
// fixes from ROCm 7.0 and is removed in 10.1, so this is the path a current AMD
// node answers on.
func parseAMDSMI(section string) []model.AcceleratorDevice {
	devices := []model.AcceleratorDevice{}

	trimmed := strings.TrimSpace(section)
	if trimmed == "" {
		return devices
	}

	var report amdSMIReport
	if err := json.Unmarshal([]byte(trimmed), &report); err != nil || report.GPUData == nil {
		// The bare array form.
		var bare []amdSMIDevice
		if err := json.Unmarshal([]byte(trimmed), &bare); err != nil {
			return devices
		}
		report.GPUData = &bare
	}

	for i, gpu := range *report.GPUData {
		device := model.AcceleratorDevice{
			Source:        sectionAMDSMI,
			Kind:          kindGPU,
			Vendor:        "amd",
			Index:         i,
			Name:          valueOrEmpty(gpu.ASIC.MarketName),
			DriverVersion: valueOrEmpty(gpu.Driver.Version),
			PCIBusID:      normalizeBusID(valueOrEmpty(gpu.Bus.BDF)),
		}
		if mib, ok := amdSMIMemoryMiB(gpu.VRAM.Size); ok {
			device.MemoryMiB = mib
			device.MemoryKnown = true
		}

		devices = append(devices, device)
	}

	return devices
}

// amdSMIMemoryMiB converts a VRAM reading to MiB.
//
// amd-smi labels the unit "MB" but the number is binary: an MI300X reports
// 196608, and 196608 MiB is exactly the 192 GiB the card carries, where 196608
// decimal MB would be 187.5 GiB. So the label is followed only for the units
// where it is unambiguous, and a bare "MB" from this tool is read as MiB.
func amdSMIMemoryMiB(size amdSMIValue) (int, bool) {
	if !size.Present || size.Value <= 0 {
		return 0, false
	}

	switch strings.ToLower(strings.TrimSpace(size.Unit)) {
	case "gib", "gb":
		return int(size.Value * 1024), true
	case "kib", "kb":
		return int(size.Value / 1024), true
	case "b", "bytes":
		return int(size.Value / (1024 * 1024)), true
	default:
		// "MB", "MiB" and an absent unit all mean binary megabytes here.
		return int(size.Value), true
	}
}
