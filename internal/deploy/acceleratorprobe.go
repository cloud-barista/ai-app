package deploy

import (
	"strings"
)

// Probe section names. Each section holds one tool's untouched output, so the
// shell on the node only collects and every parser stays here in Go where it can
// be unit tested against a captured sample.
const (
	sectionPCI     = "pci"
	sectionNVIDIA  = "nvidia-smi"
	sectionAMDSMI  = "amd-smi"
	sectionROCm    = "rocm-smi"
	sectionRBLN    = "rbln-stat"
	sectionFuriosa = "furiosa-smi"
	sectionHL      = "hl-smi"
	sectionTPU     = "tpu-info"
)

// sectionMarker prefixes each section header in the probe output. It is long and
// specific so that a tool printing a banner of its own cannot be mistaken for it.
const sectionMarker = "===AIAPP-PROBE "

// acceleratorProbeCommand asks the node what it actually carries.
//
// It queries rather than collects. Continuous accelerator telemetry is a separate
// job with its own agent and schema; copying that pipeline into a deployment
// service would duplicate a collector with nowhere to put the samples.
//
// The PCI section runs first and unconditionally, because it is the only one that
// needs nothing installed. A vendor tool that is absent and an accelerator that is
// absent produce the same silence, and without the bus inventory those two stay
// indistinguishable - which is exactly the case this probe exists to catch, since
// an accelerator image whose driver never bound leaves a node looking healthy.
//
// AMD is asked twice. amd-smi is the supported tool: rocm-smi takes only critical
// fixes from ROCm 7.0 and is gone in 10.1, so a current node may carry no rocm-smi
// at all, while a node pinned to an older ROCm may carry no amd-smi. Asking both
// costs one absent binary on either kind of node, and the merge keeps whichever
// answered.
//
// rocm-smi itself is asked for JSON and then for CSV. JSON is the format with
// captured output to test against, but a release that does not take the flag
// answers with nothing at all rather than an error, and dropping to the other
// format costs one more invocation only on the node where the first came back
// empty.
//
// Every vendor section is guarded by command -v and has its stderr dropped, so a
// node carrying one vendor's tooling does not fail the probe for the others.
//
// A newline is printed before each marker because not every tool ends its
// output with one: rocm-smi's JSON does not, and without this the next marker
// lands on the closing brace, the section boundary disappears and both sections
// are lost. The parser guards against the same thing, since the shell is not
// the only way output reaches it.
const acceleratorProbeCommand = `
printf '\n' ; echo "` + sectionMarker + sectionPCI + `==="
for d in /sys/bus/pci/devices/*; do
  [ -r "$d/class" ] || continue
  cls=$(cat "$d/class" 2>/dev/null)
  case "$cls" in 0x03*|0x12*) ;; *) continue ;; esac
  drv="-"
  if [ -L "$d/driver" ]; then drv=$(basename "$(readlink -f "$d/driver" 2>/dev/null)"); fi
  pf="-"
  if [ -e "$d/physfn" ]; then pf="vf"; fi
  echo "$(basename "$d") $cls $(cat "$d/vendor" 2>/dev/null) $(cat "$d/device" 2>/dev/null) $drv $pf"
done 2>/dev/null
printf '\n' ; echo "` + sectionMarker + sectionNVIDIA + `==="
command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi \
  --query-gpu=index,uuid,name,memory.total,driver_version,mig.mode.current,pci.bus_id \
  --format=csv,noheader,nounits 2>/dev/null
printf '\n' ; echo "` + sectionMarker + sectionAMDSMI + `==="
command -v amd-smi >/dev/null 2>&1 && amd-smi static \
  --asic --bus --vram --driver --json 2>/dev/null
printf '\n' ; echo "` + sectionMarker + sectionROCm + `==="
if command -v rocm-smi >/dev/null 2>&1; then
  rocm_args="--showid --showproductname --showmeminfo vram --showdriverversion --showbus"
  rocm_out=$(rocm-smi $rocm_args --json 2>/dev/null)
  if [ -z "$rocm_out" ]; then rocm_out=$(rocm-smi $rocm_args --csv 2>/dev/null); fi
  printf '%s\n' "$rocm_out"
fi
printf '\n' ; echo "` + sectionMarker + sectionRBLN + `==="
command -v rbln-stat >/dev/null 2>&1 && rbln-stat 2>/dev/null
printf '\n' ; echo "` + sectionMarker + sectionFuriosa + `==="
command -v furiosa-smi >/dev/null 2>&1 && furiosa-smi info 2>/dev/null
printf '\n' ; echo "` + sectionMarker + sectionHL + `==="
command -v hl-smi >/dev/null 2>&1 && hl-smi \
  -Q index,uuid,name,memory.total,driver_version,bus_id -f csv,noheader,nounits 2>/dev/null
printf '\n' ; echo "` + sectionMarker + sectionTPU + `==="
command -v tpu-info >/dev/null 2>&1 && tpu-info 2>/dev/null
printf '\n' ; echo "` + sectionMarker + `end==="
`

// splitProbeSections cuts the probe output into its sections.
//
// Output that carries no marker at all is returned under sectionNVIDIA, so that a
// node still running against the previous single-command probe keeps being read
// instead of silently reporting nothing.
func splitProbeSections(stdout string) map[string]string {
	sections := make(map[string]string)

	if !strings.Contains(stdout, sectionMarker) {
		trimmed := strings.TrimSpace(stdout)
		if trimmed != "" {
			sections[sectionNVIDIA] = stdout
		}
		return sections
	}

	var (
		current string
		body    strings.Builder
	)

	flush := func() {
		if current != "" && current != "end" {
			sections[current] = body.String()
		}
		body.Reset()
	}

	for _, line := range strings.Split(stdout, "\n") {
		// The marker is looked for anywhere in the line, not only at its start:
		// a tool whose output does not end in a newline leaves the next marker
		// glued to its last character, and treating that line as ordinary body
		// would swallow the boundary and merge two sections into one.
		at := strings.Index(line, sectionMarker)
		if at >= 0 && strings.HasSuffix(strings.TrimSpace(line), "===") {
			if current != "" && at > 0 {
				body.WriteString(line[:at])
				body.WriteString("\n")
			}
			flush()
			name := strings.TrimSpace(line[at:])
			current = strings.TrimSuffix(strings.TrimPrefix(name, sectionMarker), "===")

			continue
		}
		if current == "" {
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()

	return sections
}

// normalizeBusID rewrites a PCI address to the form sysfs uses, so that an
// address from a vendor tool and one from the bus compare equal.
//
// nvidia-smi prints an eight digit domain and upper case hex; sysfs prints four
// digits and lower case. Comparing the two unnormalised silently fails to join,
// and the device then appears twice: once from its driver and once from the bus.
func normalizeBusID(busID string) string {
	busID = strings.ToLower(strings.TrimSpace(busID))
	if busID == "" {
		return ""
	}

	parts := strings.Split(busID, ":")
	if len(parts) != 3 {
		return busID
	}

	domain := strings.TrimLeft(parts[0], "0")
	for len(domain) < 4 {
		domain = "0" + domain
	}

	return domain + ":" + parts[1] + ":" + parts[2]
}
