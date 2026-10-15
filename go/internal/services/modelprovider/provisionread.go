// provisionread.go: readProvisionablePersonalConfig (providerProvisioningSource.ts)
// — the full-read constraint both the provisioning source and target share.
package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// ReadProvisionablePersonalConfig ports readProvisionablePersonalConfig:
// 分发读取不能把坏文件的内存降级当成权威；Source 与 Target 使用同一完整读取约束.
func ReadProvisionablePersonalConfig(ctx context.Context, runtime *ProviderRuntime, filePath string) (*providerconfig.LayerSnapshot, error) {
	personal, err := runtime.PersonalRepo.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("本地 Personal Provider Config 无法同步: %s: %w", filePath, err)
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			rules := personal.Models.ToPersonalJSON()
			// 首次尚无 Personal 文件是合法空配置；已有内容后文件消失不能继续导出旧快照.
			if personal.Providers.Len() == 0 &&
				len(rules.ProviderModelRules) == 0 && len(rules.ManualProviderModelRules) == 0 &&
				len(personal.ProviderOrder) == 0 && personal.DefaultModelSelection == nil {
				return personal, nil
			}
		}
		return nil, fmt.Errorf("本地 Personal Provider Config 无法同步: %s: %w", filePath, err)
	}
	decoded, err := providerconfig.DecodeProviderConfigFile(raw)
	if err != nil {
		return nil, fmt.Errorf("本地 Personal Provider Config 无法同步: %s: %w", filePath, err)
	}
	encoded, err := providerconfig.EncodeProviderConfigFile(decoded)
	if err != nil {
		return nil, fmt.Errorf("本地 Personal Provider Config 无法同步: %s: %w", filePath, err)
	}
	canonical, err := json.Marshal(encoded)
	if err != nil {
		return nil, err
	}
	if providerconfig.SHA256Hex(canonical) != personal.Revision {
		return nil, errors.New("Personal Provider Config 在读取期间发生变化")
	}
	return personal, nil
}
