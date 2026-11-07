package accounts

import (
	"time"

	"mcode/internal/providers/multiaccount"
)

// 账号状态常量
const (
	StatusActive    = "active"
	StatusDisabled  = "disabled"
	StatusInvalid   = "invalid"
	StatusForbidden = "forbidden"

	SystemAccountID = "system"
)

// Index 对应 <dataDir>/agy-accounts.json 的全局元数据索引结构。
type Index struct {
	Version          int       `json:"version"`
	DefaultAccountID string    `json:"defaultAccountId"`
	Accounts         []Account `json:"accounts"`
}

// Account 对应单一账号实体元数据。
type Account struct {
	ID            string                `json:"id"`
	Email         string                `json:"email"`
	Name          string                `json:"name"`
	Label         string                `json:"label"`
	Status        string                `json:"status"`
	StatusReason  string                `json:"statusReason"`
	On            bool                  `json:"on"`
	Order         int                   `json:"order"`
	Weight        int                   `json:"weight"`
	ProjectID     string                `json:"projectId"`
	Tier          string                `json:"tier"`
	DeviceProfile DeviceProfile         `json:"deviceProfile"`
	Quota         map[string]QuotaGroup `json:"quota"`
	QuotaReadAt   string                `json:"quotaReadAt"`
	QuotaStale    bool                  `json:"quotaStale"`
	AddedAt       string                `json:"addedAt"`
	CoolingUntil  time.Time             `json:"-"`
	LastWarmup    *WarmupState          `json:"lastWarmup,omitempty"`
}

// WarmupState 记录最近一次 Gemini 预热的状态与周期追踪。
type WarmupState struct {
	TargetResetAt    string `json:"targetResetAt,omitempty"`    // 预热针对的 Gemini 周期重置时间（RFC3339）
	AttemptedAt      string `json:"attemptedAt,omitempty"`      // 发起尝试的时间（RFC3339）
	Model            string `json:"model,omitempty"`            // 实际成功使用的模型 ID
	Status           string `json:"status,omitempty"`           // 状态: success / pending_confirmation / failed / network_unknown
	Message          string `json:"message,omitempty"`          // 简明提示或错误信息
	ConfirmedResetAt string `json:"confirmedResetAt,omitempty"` // 服务端已确认生效的新周期重置时间（RFC3339）
	Attempts         int    `json:"attempts,omitempty"`         // 同一目标周期明确失败的尝试计数
}

// QuotaGroup 存储某一模型分组的额度快照。
type QuotaGroup struct {
	UsedPercent float64 `json:"usedPercent"`
	ResetAt     string  `json:"resetAt"`
}

// DeviceProfile 按照规划 §6.1 定义的设备指纹模型。
type DeviceProfile struct {
	MachineId        string `json:"machineId"`
	MacMachineId     string `json:"macMachineId"`
	DevDeviceId      string `json:"devDeviceId"`
	SqmId            string `json:"sqmId"`
	ServiceMachineId string `json:"serviceMachineId"`
}

// OAuthTokenFile 对应隔离目录下的 antigravity-oauth-token 文件结构。
type OAuthTokenFile struct {
	AuthMethod string         `json:"auth_method"`
	IDToken    string         `json:"id_token,omitempty"`
	Token      OAuthTokenData `json:"token"`
}

// OAuthTokenData 对应 token 凭证细节。
type OAuthTokenData struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
}

// UpdatePatch 用于更新账号配置的补丁结构体。
type UpdatePatch struct {
	Label  *string `json:"label,omitempty"`
	On     *bool   `json:"on,omitempty"`
	Order  *int    `json:"order,omitempty"`
	Weight *int    `json:"weight,omitempty"`
}

// RoutingStore 对应 <dataDir>/agy-routing.json 的持久化结构。
type RoutingStore struct {
	multiaccount.Config
	Accounts   map[string]AccountRoutingOverride `json:"accounts,omitempty"`
	AutoWarmup *bool                             `json:"autoWarmup,omitempty"`
}

// IsAutoWarmupEnabled 返回自动预热是否开启。缺失字段默认保持为开启状态。
func (r *RoutingStore) IsAutoWarmupEnabled() bool {
	if r == nil || r.AutoWarmup == nil {
		return true
	}
	return *r.AutoWarmup
}

// AccountRoutingOverride 单个账号在路由配置中的覆盖参数。
type AccountRoutingOverride struct {
	On     *bool `json:"on,omitempty"`
	Order  *int  `json:"order,omitempty"`
	Weight *int  `json:"weight,omitempty"`
}
