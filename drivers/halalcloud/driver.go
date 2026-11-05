package halalcloud

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/halalcloud/golang-sdk-lite/halalcloud/apiclient"
	sdkConfig "github.com/halalcloud/golang-sdk-lite/halalcloud/config"
	sdkUser "github.com/halalcloud/golang-sdk-lite/halalcloud/services/user"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"

	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/httpx"
)

// Driver 把 2dland（清真云 / halalcloud）网盘挂载到 LitePan。
//
// 用官方 SDK 直连开放 API，不经过 OpenList 或 WebDAV 网关：
// 写入走「建上传任务 → 按块传 CID → makeFile 收尾」，支持服务端秒传。
type Driver struct {
	add     Addition
	client  *sdk.Client
	files   *sdkUserFile.UserFileService
	users   *sdkUser.UserService
	store   *configStore
	http    *http.Client
	persist driver.AuthPersistFunc
}

var config = driver.Config{
	Name:        "halalcloud",
	DisplayName: "2dland（清真云）",
	Description: "官方开放 API 直连，支持分块上传与秒传，不依赖 OpenList / WebDAV 网关",
	CardTags:    []string{"官方 API", "分块上传", "秒传"},
	SortOrder:   98,
	AuthLabel:   "Client ID / Secret",
	CardColor:   "#5b8def",
	CardLogo:    "/logos/2dland.svg",
	DefaultRoot: "/",
	AuthType:    driver.AuthNone,
}

func New() driver.Driver { return &Driver{} }

func init() { driver.Register(New) }

func (d *Driver) Config() driver.Config { return config }

func (d *Driver) GetAddition() any { return &d.add }

// SetAuthPersister 由 manager 注入：SDK 续期 refresh token 后回写给 accounts 表。
func (d *Driver) SetAuthPersister(fn driver.AuthPersistFunc) { d.persist = fn }

func (d *Driver) Init(ctx context.Context) error {
	clientID := strings.TrimSpace(d.add.ClientID)
	secret := strings.TrimSpace(d.add.ClientSecret)
	if clientID == "" {
		return domain.Errorf(domain.CodeValidation, "Client ID 不能为空")
	}
	if secret == "" {
		return domain.Errorf(domain.CodeValidation, "Client Secret 不能为空")
	}
	if strings.TrimSpace(d.add.RootPath) == "" {
		d.add.RootPath = "/"
	}
	host := normalizeHost(d.add.Host)
	timeout := defaultTimeout
	if d.add.Timeout > 0 {
		timeout = time.Duration(d.add.Timeout) * time.Second
	}
	d.http = &http.Client{Timeout: timeout}
	store := &configStore{values: map[string]string{}}
	if token := strings.TrimSpace(d.add.RefreshToken); token != "" {
		store.values[refreshTokenKey] = token
	}
	store.onChange = func(key, value string) {
		if key != refreshTokenKey {
			return
		}
		d.add.RefreshToken = value
		if d.persist == nil {
			return
		}
		// 续期在 SDK 请求链路内触发，这里用独立 context，避免随请求取消而丢写。
		go func() { _ = d.persist(context.Background(), domain.AuthCredentials{RefreshToken: value}) }()
	}
	d.store = store
	d.client = sdk.NewClient(d.http, host, clientID, secret, store, sdk.WithTimeout(timeout))
	d.files = sdkUserFile.NewUserFileService(d.client)
	d.users = sdkUser.NewUserService(d.client)
	_ = ctx
	return nil
}

func (d *Driver) Drop(context.Context) error {
	if d.http != nil {
		d.http.CloseIdleConnections()
	}
	d.client, d.files, d.users, d.store, d.http = nil, nil, nil, nil, nil
	return nil
}

func (d *Driver) Ping(ctx context.Context) error {
	if d.users == nil {
		return domain.Errorf(domain.CodeInternal, "2dland 客户端未初始化")
	}
	if _, err := d.users.Get(ctx, &sdkUser.User{}); err != nil {
		return mapError(err)
	}
	return nil
}

// ExplainConnectionError 给保存账号时的连通性错误补人话。
func (d *Driver) ExplainConnectionError(technical string, saving bool) string {
	prefix := "添加失败"
	if saving {
		prefix = "保存失败"
	}
	lower := strings.ToLower(technical)
	switch {
	case strings.Contains(lower, "401") || strings.Contains(lower, "unauthorized"):
		return prefix + "：Client ID / Client Secret 不正确或已取消授权"
	case strings.Contains(lower, "403") || strings.Contains(lower, "forbidden"):
		return prefix + "：该凭证无权访问，检查 2dland 授权管理里的凭证状态"
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused"):
		return prefix + "：无法连接 2dland API，检查网络与 API 地址"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline"):
		return prefix + "：连接 2dland 超时，可调大请求超时"
	default:
		return ""
	}
}

func normalizeHost(raw string) string {
	host := strings.TrimSpace(raw)
	if host == "" {
		return defaultHost
	}
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	return strings.TrimSuffix(host, "/")
}

// configStore 是 SDK 用来存取 token 的配置槽位。
//
// refresh token 一旦更新就通过 onChange 回写到账号配置，
// 否则进程重启后要重新授权。
type configStore struct {
	mu       sync.Mutex
	values   map[string]string
	onChange func(key, value string)
}

var _ sdkConfig.ConfigStore = (*configStore)(nil)

func (s *configStore) GetConfig(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *configStore) SetConfig(key, value string) error {
	s.mu.Lock()
	old := s.values[key]
	s.values[key] = value
	changed := old != value
	s.mu.Unlock()
	if changed && s.onChange != nil {
		s.onChange(key, value)
	}
	return nil
}

func (s *configStore) DeleteConfig(key string) error {
	s.mu.Lock()
	delete(s.values, key)
	s.mu.Unlock()
	return nil
}

func (s *configStore) ListConfigs() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, nil
}

func (s *configStore) ClearConfigs() error {
	s.mu.Lock()
	s.values = map[string]string{}
	s.mu.Unlock()
	return nil
}

func (s *configStore) GetRefreshToken() (string, error) { return s.GetConfig(refreshTokenKey) }

func (s *configStore) SetRefreshToken(token string) error { return s.SetConfig(refreshTokenKey, token) }

func (s *configStore) GetAccessToken() (string, error) { return s.GetConfig(accessTokenKey) }

func (s *configStore) SetAccessToken(token string) error { return s.SetConfig(accessTokenKey, token) }

// SetToken 一次性写入 access/refresh（SDK 刷新令牌后的回调）。
func (s *configStore) SetToken(accessToken, refresh string, expiresIn int64) error {
	if accessToken != "" {
		if err := s.SetAccessToken(accessToken); err != nil {
			return err
		}
	}
	if refresh != "" {
		if err := s.SetRefreshToken(refresh); err != nil {
			return err
		}
	}
	_ = expiresIn
	return nil
}

func (d *Driver) uploadHTTPClient() *http.Client {
	if d.http != nil {
		return d.http
	}
	return httpx.NewUploadClient(&http.Client{}, 0, false)
}

func (d *Driver) rootPath() string {
	p := strings.TrimSpace(d.add.RootPath)
	if p == "" {
		return "/"
	}
	return "/" + strings.Trim(strings.TrimSpace(p), "/")
}

// normalizePath 把各种根目录占位统一成绝对路径。
func (d *Driver) normalizePath(id string) string {
	p := strings.TrimSpace(id)
	if p == "" || p == "/" || p == "0" || strings.EqualFold(p, "root") {
		return d.rootPath()
	}
	p = "/" + strings.Trim(p, "/")
	if root := d.rootPath(); root != "/" && !strings.HasPrefix(p, root+"/") && p != root {
		return root + p
	}
	return p
}

func (d *Driver) childPath(parent, name string) string {
	base := strings.TrimSuffix(d.normalizePath(parent), "/")
	return base + "/" + strings.TrimSpace(name)
}
