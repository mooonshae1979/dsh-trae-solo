# dsh-trae-solo — TRAE SOLO 免费模型网关 for DeepSeek Harness

把 **TRAE SOLO 免费对话额度** 接入 DeepSeek Harness：注册 `trae-solo` provider（自动被视觉桥/模型选择识别），并随插件分发 **traework2api** 多账号网关（登录、自动签到、积分监控、每模型并发限制、`developer` role 归一化）。

## 特性

- **Provider 接入**：`llm-pi-ai.providers.trae-solo` 一段配置即注册全部模型（DeepSeek V4 系列 / GLM / Doubao / Kimi / Qwen / MiniMax），模型选择器、视觉桥自动发现。
- **多账号网关**（`server/`，纯 Go 零依赖）：TRAE SOLO 免费对话通道 → OpenAI 兼容 `/v1/chat/completions` + `/v1/models`，支持多账号积分轮转、自动签到、token 自动刷新。
- **每模型并发限制**：`model_concurrency` 配置（如 DeepSeek-V4-Pro=500 / Flash=2500），超限返回 429。
- **`developer` role 归一化**：TRAE 上游只接受 `system/assistant/user/tool`；DSH 的 pi-ai 在模型 `reasoning=true` 时会把 system prompt 发成 `developer`，网关在代理层统一归一化为 `system`，避免 TRAE 4027 断流。
- **管理脚本**（`scripts/`）：`login.sh` 登录、`signin.sh` 批量签到、`credit.sh` 积分查询、`credits_api.py` 积分监控服务。

## 安装

```sh
# 从 GitHub 安装（含预构建 lib/ 与 server/ 网关源码）
dsh plugin --profile web add https://github.com/mooonshae1979/dsh-trae-solo
```

重启 `dsh web` 生效。

## 配置 provider（`~/.dsh/settings.yaml`）

```yaml
llm-pi-ai:
  providers:
    trae-solo:
      displayName: TRAE SOLO 免费模型（<网关地址>）
      api: openai-completions
      baseURL: http://<网关地址>:7864/v1   # traework2api 网关地址
      apiKeyEnv: TRAE_SOLO_API_KEY
      defaultContextWindow: 1048576
      models:
        - id: DeepSeek-V4-Pro
          name: DeepSeek V4 Pro
          contextWindow: 1048576
          reasoning: true
          reasoningEfforts:
            "off": null
            max: "max"          # TRAE 只有 default/max 两档
        - id: DeepSeek-V4-Flash-Official
          name: DeepSeek V4 Flash（0731 正式版）
          contextWindow: 1048576
          reasoning: true
          reasoningEfforts:
            "off": null
            max: "max"
        - id: DeepSeek-V4-Flash
          name: DeepSeek V4 Flash
          contextWindow: 1048576
          reasoning: true
          reasoningEfforts:
            "off": null
            max: "max"
        # GLM / Doubao / Kimi / Qwen / MiniMax 系列（不含 reasoning，TRAE 无对应开关）
        - id: glm-5.2
          name: GLM-5.2
          contextWindow: 1048576
        - id: Doubao-Seed-2.1-Pro
          name: Doubao Seed 2.1 Pro
          contextWindow: 262144
          input: [ text, image ]
        # ... 其余模型按需追加
```

> **要点**
> - **只有 DeepSeek 系列**有 `reasoning`（TRAE 只有 default/max 两档），其他模型不要加 `reasoning: true`——否则 DSH 会发 `reasoning_effort` 给 TRAE，且 system prompt 会以 `developer` role 发送（网关已归一化，但无 reasoning 开关的模型发了也无效）。
> - KEY 放 `~/.dsh/.credentials.yaml`：`TRAE_SOLO_API_KEY: <你的随机密钥>`。

## 部署网关（server/）

```bash
# 1. 准备凭证目录（放 trae-*.json）
mkdir -p auths data

# 2. 配置 API Key（Bearer 鉴权，key 只走 env）
cp .env.example .env
# 编辑 .env，把 changeme 换成你自己的随机密钥

# 3. 启动（Docker）或 systemd
docker compose up -d --build
# 或 systemd（见 server/README.md）

# 4. 验证
curl http://127.0.0.1:7864/healthz      # → ok
curl http://127.0.0.1:7864/v1/models     # → 模型列表
```

### 添加账号（`scripts/login.sh`）

```bash
cd server && ./login.sh
# 1. 打印带新 machine/device id 的登录链接（手机号 + 验证码）
# 2. 浏览器登录后复制 127.0.0.1:18080 回调链接粘回去
# 3. 自动换 token → 落盘 auths/trae-{uid}.json → 自动签到查积分
```

> 每个账号独立 `machine/device id`，TRAE 服务端视为不同设备，天然支持多账号同网关共存；无需伪造机器码。

### 并发限制（`config.json`）

```json
{
  "model_concurrency": {
    "DeepSeek-V4-Pro": 500,
    "DeepSeek-V4-Flash": 2500,
    "DeepSeek-V4-Flash-Official": 2500
  }
}
```

超限请求返回 `429 rate_limit`。

## Model Experience

### Request surface and condition

#### What the model sees

`trae-solo` 是一个 OpenAI 兼容 provider：DSH 把消息按 `openai-completions` 协议发给网关，网关改写为 TRAE `llm_utils_chat` 格式并转发。对模型而言，它与任何 OpenAI 兼容端点一致——消息、工具、流式输出、`reasoning_effort`（DeepSeek 系列）。

DeepSeek 系列声明 `reasoning: true` + `reasoningEfforts: {off, max}`，因此模型选择器会提供 `off`/`max` 两档思考档位，DSH 发送 `reasoning_effort: "max"`。

#### Token effect

对话 token 正常计入各 TRAE 账号的免费额度（网关按 `ide_user_ent_usage` 监控）。网关是**免费额度共享**，不产生额外 token 计量。

#### KV Cache effect

`reasoning_effort` 参数与消息前缀一起透传；DeepSeek 系列开启思考时，流内包含 `reasoning_content`。网关不注入会破坏前缀的头部，请求兼容 OpenAI 前缀缓存语义（若上游支持）。

## Known Limitations and Deferred Work

- **网关是免费额度共享器，非官方 API** — TRAE SOLO 免费通道有平台风控（如不可用模型触发 12h 连坐冷却，如 Kimi-K3）；多账号轮转属灰区，账号可能被封，请勿过度并发。
- **`reasoning` 仅 DeepSeek 系列** — TRAE 其他模型无思考开关，配置 `reasoning: true` 无效且会引入 `developer` role（网关已兜底归一化，但仍建议按表配置）。
- **上下文窗口为配置值** — `contextWindow` 按官方/社区公开值填入（DeepSeek 1M、GLM-5.2 1M、Kimi 256K 等），网关 `/v1/models` 的 `context_length` 是静态表；若 TRAE 侧调整，需同步更新。
- **`developer` role 归一化在网关层** — 若绕过本网关直连 TRAE 上游，仍需自行处理该 role。

## License

[MIT](LICENSE)