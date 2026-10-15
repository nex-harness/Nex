// volumeprobe.go ports storage/adapters/volumeProbe.ts: identify the physical
// volume via the stat device id, walk up to the mount point, then statfs for
// capacity. Any failure returns nil (the UI shows usage only, no capacity);
// it never errors.
package storage

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
)

// fsVolumeProbe ports createFsVolumeProbe.
type fsVolumeProbe struct{}

// NewFsVolumeProbe returns the filesystem VolumeProbePort.
func NewFsVolumeProbe() VolumeProbePort { return fsVolumeProbe{} }

// Probe implements VolumeProbePort.
func (fsVolumeProbe) Probe(_ context.Context, path string) *Volume {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	deviceID := deviceIDOf(info)
	mountPoint := path
	// 逐级向上：父目录仍在同一 dev 上就继续；到根或 dev 变化即为挂载点
	// （Windows 会停在盘符根）。
	for {
		parent := filepath.Dir(mountPoint)
		if parent == mountPoint {
			break
		}
		parentInfo, err := os.Stat(parent)
		if err != nil {
			break
		}
		if deviceIDOf(parentInfo) != deviceID {
			break
		}
		mountPoint = parent
	}
	total, free, ok := statfsBytes(mountPoint)
	if !ok {
		return nil
	}
	return &Volume{
		DeviceID:   strconv.FormatUint(deviceID, 10),
		MountPoint: mountPoint,
		TotalBytes: total,
		FreeBytes:  free,
	}
}
