package deploy

import "github.com/ish-hcc/ai-agent-proto/internal/model"

// AcceleratorDeviceAlias lets the table test read fields without importing the
// model package into every case.
type AcceleratorDeviceAlias = model.AcceleratorDevice

// stubReport wraps a device list so the comparison can be exercised without a
// live node.
func stubReport(devices []model.AcceleratorDevice) *model.AcceleratorReport {
	return &model.AcceleratorReport{
		InfraID:  "test",
		Probed:   true,
		Mode:     acceleratorMode(devices),
		Devices:  devices,
		Findings: []string{},
	}
}
