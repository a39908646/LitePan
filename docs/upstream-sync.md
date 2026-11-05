# 上游同步手册

本仓库是 [Ponphil/LitePan](https://github.com/Ponphil/LitePan) 的 fork，用于二次开发
（功能增强 + bug 修复）。**不向上游贡献代码，不对上游产生任何影响**。

> 许可证为 **PolyForm Noncommercial 1.0.0**：仅限非商业用途。
> 涉及售卖 / 闭源商业分发前，必须先取得上游授权。

---

## 1. 分支模型：单分支

```
main            ← 唯一开发主线，二开提交直接落这里
upstream/main   ← 只读引用，git fetch upstream 更新，永不写
origin/main     ← 自己的 GitHub 备份
```

**不需要"跟随上游"的分支**：上游状态由 `upstream/main` 这个远端引用表示，随时可 diff / merge。
冲突量与分支数量无关，只取决于同步频率和改动落点。

发版从 `main` 打 tag，命名建议 `<上游版本>-<序号>`，如 `v0.5.7-Beta-1`。

---

## 2. 每次同步（推荐 1–2 周一次，或上游发版时）

```bash
make sync              # = bash scripts/sync-upstream.sh，交互式：报告 → 确认 → 合并 → 测试
make sync-check        # 只看上游更新了什么，不做任何改动
```

脚本内部做的事，也可以用纯 git 手工完成：

```bash
git fetch upstream --tags                       # ① 拉取（只读，不动工作区）
git log  --oneline HEAD..upstream/main          # ② 看规模
git diff --stat     HEAD...upstream/main
git tag pre-sync-$(date +%Y%m%d-%H%M%S)         # ③ 打退路
git merge --no-ff upstream/main                 # ④ 合并
make test && make build                         # ⑤ 验证
git push origin main --tags                     # ⑥ 备份
```

**别攒。** 上游改动活跃，攒 2 个月冲突会从几行涨到几十个文件——因为上游改过的行和你
改过的行重叠概率随时间上升。

**回退**：未 push 用 `git reset --hard pre-sync-xxx`；已 push 用 `git revert -m 1 <merge-sha>`。

### ⚠️ 硬禁忌

**永远不要点 GitHub 网页上的 "Sync fork" 按钮。** 单分支方案下你的 main 有二开提交，
快进合并不成立，按钮里会建议 **"Discard commits"** —— 误点等于丢掉本地未 push 的提交。
同步只在本地做。

辅助措施（已配置，可重复执行）：

```bash
git remote set-url --push upstream DISABLED   # upstream 物理上无法 push
```

---

## 3. 冲突处理规则

**最高原则：冲突时永远是"上游是最新基线，我的补丁重贴上去"，而不是"我的版本为主，
把上游塞进来"。** 反过来做，第一次省事，第三次同步就崩。

| 类型 | 典型文件 | 处理 |
|---|---|---|
| **生成物** | `internal/api/web/**` | 不看内容。取任意一侧后重新 `npm run build` |
| **上游重构了结构** | 上游拆/并包、改签名 | 以上游为新基线，把自己的改动重贴到新结构上 |
| **同文件不同行** | `internal/api/router.go` 双加路由 | 两边都留（这就是要求"只在块尾追加"的原因） |

生成物冲突的具体操作：

```bash
git checkout --ours -- internal/api/web
(cd web && npm run build)      # 重新生成
git add -A && git commit --no-edit
```

---

## 4. 落点纪律（长期成本的核心）

**原则：自己的代码放进自己能完全拥有的新文件；上游文件只允许"块尾追加几行"。**

### 上游冲突热点（近 200 次提交改动次数）

| 文件 | 次数 | 说明 |
|---|---|---|
| `internal/api/web/index.html` | 110 | 前端产物（已用 `merge=ours` 处理） |
| `internal/api/router.go` | 35 | 672 行，单文件集中注册所有路由 |
| `internal/settings/registry.go` | 25 | `defaultSpecs()` 返回设置项字面量 |
| `web/src/components/admin/*Panel.vue` | ~21 | 上游高频重构的界面组件 |
| `internal/app/wire_services.go` | 19 | 依赖注入装配 |
| `web/src/version.ts` | 16 | 品牌与版本号唯一来源 |
| `internal/app/wire_http.go` | 16 | HTTP 依赖装配 |

### 落点约定

| 要加的东西 | ❌ 不要 | ✅ 应该 |
|---|---|---|
| 新后端功能 | 塞进现有 `internal/` 包 | 新包 `internal/<feature>/`，接入点只加一行 |
| 新 API 路由 | 往 `router.go` 中间插入 | 在 `r.Route("/api", ...)` **块尾**追加 `r.Route("/x-foo", registerFoo)`；handler 放自己的文件 |
| 新设置项 | 改 `registry.go` 的 `defaultSpecs()` | 自己的文件里定义 `extraSpecs`；在冷文件 `internal/settings/service.go:33` 改一行 `specs := append(defaultSpecs(), extraSpecs()...)` |
| 新驱动 | 改驱动抽象层 | `drivers/all.go` 加一行 import（上游已是此模式） |
| 改前端页面 | 直接改 `components/admin/*Panel.vue` | 新页面放 `web/src/views/your-*.vue`；复用上游组件用 wrapper / 插槽，不改原件 |
| 前端路由 | 中间插入 | `web/src/router` 路由表**尾部**追加 |
| 品牌 / 版本号 | 分散改多处 | 只改 `web/src/version.ts` + `internal/buildinfo`，单独一个 commit；或用 `-ldflags "-X litepan/internal/buildinfo.Version=..."` 覆盖，文件不动 |

### 三条硬约束

1. **不要改 `go.mod` 的 `module litepan`**。改成 `github.com/xxx/litepan` 会让全仓 import
   路径变化，上游每一次改动都变成全仓冲突。这是 fork 最贵的错误。
2. **不要重新格式化上游文件**。别顺手对全仓跑 `gofmt` / prettier，空白改动会让后续每次
   merge 都撞车。
3. **不要删上游文件**。下游删除 + 上游修改 = 永久 `modify/delete` 冲突。用不上就留着。

### 提交信息约定

```
fork: feat 新增 XXX 面板
fork: fix 修正 YYY 在 ZZZ 下的误判
upstream: 合并 upstream/main (v0.5.8)
```

常用查询：

```bash
git log --oneline --grep="^fork:"          # 全部二开面
git log --oneline upstream/main..HEAD      # 同上，按提交图
git diff upstream/main --stat              # 我改了哪些文件（合并前扫一眼）
```

---

## 5. 前端构建产物

`internal/api/web/` 是 `web/vite.config.ts` 的构建输出目录（`outDir` + `emptyOutDir: true`），
191 个文件入库（164 个带内容 hash 的资源、160 个 `.gz` 预压缩文件），经 `//go:embed web`
编进二进制。上游发版时 `Dockerfile` 会重新构建并覆盖，即上游自己也无视仓库里这份产物。

**处理方式**：`.gitattributes` 标记 `internal/api/web/** merge=ours -diff` —— 合并时冲突
一律取本地、不做文本 diff。需要每个 clone 跑一次 driver 配置：

```bash
make setup      # = git config merge.ours.driver true
```

**机制说明**：`merge=ours` 只在**双方都改了同一文件**时才生效（静默取本地）；只有上游改时
正常合入上游版本。两种结果都无需人工介入。另一个后果：该目录在 diff 里显示为
`Bin N -> M bytes`（由 `-diff` 控制），不会输出几百行压缩产物文本。

**日常规矩**：本地构建后不要提交这些产物。

```bash
(cd web && npm run build) && git restore internal/api/web
make restore-web        # 等价，且额外删除未跟踪的新 hash 文件（git clean -fdq）
make build-web          # 构建 + 自动还原，一步到位
```

---

## 6. 验证

```bash
make test          # go test -race ./...  ← 上游测试 + 你的测试一起跑，最有效的合并验证
make lint          # golangci-lint v2.12.2
make build         # go build -tags fuse ./...
make build-nofuse
```

**给二开功能补测试。** `make test` 是唯一能发现"编译通过了但功能被上游改了行为"的手段——
这类静默失效比编译错误危险得多。

---

## 7. 同步台账

每次同步追加一行。三个月后靠它才知道"某处为什么和上游不同"。

| 同步日期 | 上游版本 / tag | 上游提交数 | 冲突文件 | 处理取舍 | 验证 |
|---|---|---|---|---|---|
| （示例）2025-01-15 | v0.5.7-Beta → v0.5.8 | 42 | `internal/api/router.go`（双加路由）<br>`internal/api/web/**`（产物） | 路由两边都留；产物取本地后 rebuild | `make test` ✅ |

---

## 8. 明确不做

| 不做 | 原因 |
|---|---|
| 给上游开 PR / issue | 要求零影响，不贡献代码 |
| 用 GitHub "Sync fork" 按钮 | 单分支下是 Discard 陷阱 |
| rebase 二开历史 | 每次上游更新要重放全部提交，越往后越痛 |
| 把上游当 Go module 依赖 | 单体应用 + `//go:embed`，拆不动 |
| patch 队列（quilt / stgit） | 改动量不足以抵消工具成本 |
| 改 module path | 见 §4 硬约束 1 |

---

## 9. Docker 镜像（GHCR）

上游镜像在 Docker Hub（`ponphil/litepan:beta`），是**纯上游代码**，不含任何二开。
二开镜像由 `.github/workflows/docker-ghcr.yml` 构建并推到 GHCR：

| 项 | 值 |
|---|---|
| 镜像 | `ghcr.io/a39908646/litepan` |
| 触发 | 推 tag（`v*`）→ `latest` + tag 名 + `sha-xxxxxxx`；手动 `workflow_dispatch` → `edge` + `sha-xxxxxxx` |
| 架构 | `linux/amd64`（要 arm64 改 workflow 里 `platforms` 一行，并给 web 阶段加 `--platform=$BUILDPLATFORM`） |
| 版本注入 | 构建前把版本号 sed 进 `web/src/version.ts`（UI 显示）和 `internal/buildinfo/version.go`（`/api` 返回），只在 runner 工作区改，不提交；sed 后断言校验，上游改了文件结构会直接失败 |

**为什么不改 Dockerfile 加 ldflags**：`Dockerfile` 是上游文件，改了每次同步都要处理冲突（§4 落点纪律）。
sed + 断言在 CI 里完成同样的事，仓库里的上游文件保持原样。

### NAS 上使用

`docker-compose.ghcr.yml` 是一个 **只覆盖 `image` 的覆盖层**，基础配置（`volumes` / `devices` /
`privileged` / `pid: host` / `network_mode`）仍用上游那两个 compose 文件，一行都不用改：

```bash
docker compose -f docker-compose.fnos.yml -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.fnos.yml -f docker-compose.ghcr.yml up -d
```

回滚：把 `image:` 换成上一次的 `vX.Y.Z-fork.<n>` 或 `sha-xxxxxxx`（Actions 页面能看到全部 tag）。

### 三个必踩的坑

1. **GHCR 包默认 private**，即使仓库是 public。NAS `docker pull` 报 `401 Unauthorized` 时的解法：
   - 公共镜像：GitHub → 你的头像 → Packages → `litepan` → Settings → Danger Zone → Change visibility → Public
   - 保持私有：NAS 上 `docker login ghcr.io -u a39908646 -p <PAT>`（PAT 勾 `read:packages`）
2. **fork 的 Actions 要手动 enable 一次**（仓库 Actions 页面），否则推 tag 后什么都不发生。
3. **别只依赖 `latest`**：NAS 上 `pull` 完不记版本号，出问题只能靠 `sha-xxxxxxx` 回滚。
