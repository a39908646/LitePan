package halalcloud

import "time"

const (
	// defaultHost 是 2dland（清真云）开放 API 的默认入口。
	defaultHost = "openapi.2dland.cn"
	// defaultTimeout 单次 API 请求超时。
	defaultTimeout = 60 * time.Second
	// defaultBlockSize 服务端未下发 BlockSize 时的分片大小（与官方 SDK 保持一致）。
	defaultBlockSize = 4 << 20
	// 分片/makeFile 失败重试次数与间隔。
	sliceRetryTimes  = 3
	sliceRetryPeriod = 5 * time.Second

	refreshTokenKey = "refresh_token"
	accessTokenKey  = "access_token"
)

// Addition 是 2dland（清真云）账号配置。
//
// 凭据来自「2dland 用户中心 → 授权管理 → 创建新的授权」，拿到 Client ID / Client Secret；
// 个人凭证不需要 OAuth 跳转，SDK 会自己换取并续期 access token。
type Addition struct {
	ClientID     string `json:"client_id" label:"Client ID" form:"required,pair=auth" example:"用户中心 → 授权管理 创建"`
	ClientSecret string `json:"client_secret" label:"Client Secret" type:"password" form:"required,pair=auth"`
	RootPath     string `json:"root_path" label:"子目录路径" form:"pair=base" example:"/"`
	Host         string `json:"host" label:"API 地址" default:"openapi.2dland.cn" form:"pair=base"`
	Timeout      flexInt `json:"timeout_seconds" label:"请求超时（秒）" type:"number" default:"60" form:"pair=opts1"`
	// RefreshToken 由 SDK 在令牌续期时回写，用户不需要填。
	RefreshToken string `json:"refresh_token" label:"Refresh Token" form:"hidden"`
}

// Int 取整数值（替代直接类型比较，便于表单传字符串时也能用）。
func (f flexInt) Int() int { return int(f) }
