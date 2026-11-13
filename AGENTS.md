# AGENTS.md

LitePan 的独立演进版（源自 [Ponphil/LitePan](https://github.com/Ponphil/LitePan)）。
许可证 **PolyForm Noncommercial 1.0.0**，仅限非商业用途。

## 定位

**这是独立产品，不是上游的补丁队列。** 上游只作为参考上游保留，用于偶尔取它的新功能或
修它的 bug；不承诺与本 fork 长期保持可合并。

因此：

- **没有落点约束。** 该改上游文件就改，该重构就重构，该删就删，`module` 路径不是禁区，
  格式也不是。冲突是合并那一刻的成本，不是写代码时预支的枷锁。
- **唯一的省钱建议（不是规则）**：能放新文件就放新文件。将来真要 merge，冲突面由自己的
  结构决定，而不是由上游结构决定。
- 同步机制、冲突取舍、台账、GHCR 部署见 [`docs/upstream-sync.md`](docs/upstream-sync.md)。

## 沟通约定

- **不要在回答里反复提醒同步风险、冲突风险、落点纪律。** 规则在这里，需要时直接执行，
  口头提醒是噪音。
- 中文回答；代码、命令、日志、报错保持原文。

## 工程约定

```bash
make test        # go test -race ./...   二开功能必须带测试
make lint        # golangci-lint v2.12.2
make build       # go build -tags fuse ./...
make build-web   # 构建前端并自动还原产物
```

- **不提交前端构建产物。** `internal/api/web/**` 是 `web/vite.config.ts` 的输出，构建后跑
  `make restore-web`（每个 clone 需 `make setup` 一次，启用 `merge=ours` driver）。
- **版本号**只改 `web/src/version.ts`（UI 显示）与 `internal/buildinfo`（`/api` 返回），
  单独一个 commit；或用 `-ldflags "-X litepan/internal/buildinfo.Version=..."` 覆盖。
- **tag 命名** `v<上游版本>.<序号>`，如 `v0.5.7-beta.1`。
- **提交前缀**：`fork: feat/fix ...`、`upstream: ...`。
- 前端改动至少要过 SFC 编译（`@vue/compiler-sfc` 的 `parse` + `compileTemplate` +
  `compileScript`）与 `node --check`——本机 `npm ci` 可能被 `EALLOWREMOTE` 拦住（`xlsx` 走 CDN）。
