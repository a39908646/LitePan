# 脚本离线下载开放端点

给外部脚本（油猴 / 油猴类工具）用的离线下载接口。管理员 session 那套 `/api/files/offline-download/*`
不适合脚本：既不能用管理员密码，也不该让一把凭据操作全部网盘。

## 鉴权

`Authorization: Bearer <API Key>`，Key 必须是 **task 类型**（`apikey.ValidateTask`）。

账号绑定写在 Key 的**备注（note）**里，端点不接受越界的 `account_id`：

```
{"offline_account_ids":[2]}      # 推荐
{"account_id":2}
account_id=2                     # 也认这种退化写法，可带其他文字
```

没写绑定 → 403 `该 API Key 未绑定网盘账号`。

## 端点

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/open/offline-download/capabilities` | 绑定账号 + 各自能力（原生/内置各自支持哪些协议） |
| POST | `/api/open/offline-download/urls` | 提交链接，`provider_kind` 默认 `builtin` |
| GET | `/api/open/offline-download/tasks` | 任务列表/进度，`refresh=false` 可跳过远端刷新 |
| POST | `/api/open/offline-download/tasks/batch-delete` | `{task_ids:[...]}` |

提交示例（磁力 → 内置下载器）：

```bash
curl -X POST http://<nas>:5211/api/open/offline-download/urls \
  -H "Authorization: Bearer sk-xxx" -H "Content-Type: application/json" \
  -d '{"urls":["magnet:?xt=urn:btih:..."],"target_parent_id":"0","target_display_path":"/"}'
```

## 两个执行器

- `native`：网盘自己离线。123 云盘只吃 `http/https`（传 `magnet:` 会被 `validateSchemes` 挡掉）。
- `builtin`：NAS 自己下载（BT/HTTP）后上传进网盘，支持 `magnet`。**脚本默认用它。**

## 已知行为：任务记录会消失

`builtin` 下载完成后把文件交棒给上传队列，`completeBuiltinHandoff` 会删掉离线任务记录
（`internal/offlinedownload/builtin.go`）。因此脚本侧看到的表现是：

- 下载中：任务在列表里，带 `progress`
- 下载完、已交棒上传：任务从列表消失

脚本应把「任务消失」解释为「已交给上传队列」，而不是失败。
