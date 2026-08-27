# qodercn-gateway

一个轻量、仅远程的网关，把 QoderCN Remote API（`gateway.qoder.com.cn`）通过标准的
**OpenAI** 和 **Anthropic** HTTP 接口暴露出来，让 Claude Code、Cline 等任意
OpenAI/Anthropic 兼容客户端可以直接接入 QoderCN 托管的模型（Qwen、Kimi、MiniMax、
GLM、DeepSeek 等）。

该项目是从[lingma-proxy]{https://github.com/Lutiancheng1/lingma-proxy}重构而来，去掉了gui和依赖本地环境的ipc模式，并优化了诸多原生工具、图片输入等内容。

## 接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| POST | `/v1/chat/completions`、`/api/v1/chat/completions` | OpenAI Chat Completions |
| POST | `/v1/messages` | Anthropic Messages |
| GET  | `/v1/models` | 模型列表 |
| POST | `/v1/images/search` | 图片搜索（网关 `imageSearch`） |
| POST | `/v1/images/generations` | 图片生成（网关 `generateImage`） |
| GET  | `/quota`、`/v1/quota` | 账户额度/用量快照 |
| GET  | `/version` | 构建版本 |
| GET  | `/`、`/health` | 健康检查 / 状态 |

Chat 与 Messages 通过 SSE 流式返回。网关支持原生 function-calling。

## 构建与运行

```bash
go build -o qodercn-gateway ./cmd/qodercn-gateway
./qodercn-gateway --host 127.0.0.1 --port 8095
```

凭证会自动从本地 QoderCN CLI 的登录缓存读取，也可用 `--remote-auth-file credentials.json`
显式指定。导出可移植的凭证/部署包（用于服务器部署）：

```bash
./qodercn-gateway --export-server-bundle bundle.zip
```

## 配置

支持命令行参数（见 `--help`）、JSON 配置文件（`--config qodercn-gateway.json`，参见
`config.example.json`）、环境变量三种方式——优先级：命令行 > 环境变量 > 配置文件。
常用项：`--remote-auth-file`、`--remote-base-url`、`--remote-proxy-url`、`--model`、
`--auth-keys-file`（入站 API key 白名单，一行一个、`#` 注释；留空 = 开放访问）。
每个入站 key 最长 50 字符，且只允许 `A-Z a-z 0-9 - _ * + =`（避免不同客户端的请求头兼容问题）；
不合规会在启动时直接报错拒绝运行。

可选特性开关（环境变量）：

- `QODERCN_INJECT_MEDIA_TOOLS=1` —— 在服务端以 agentic 循环方式声明并执行网关的
  `web_search` / `ImageSearch` / `TextPolish` 工具（对客户端隐藏）。
- `QODERCN_IMAGE_DEWATERMARK=1` —— 通过不可逆的几何去同步重编码，破坏生成图片中的
  鲁棒水印载荷（有效性未知）。

## Docker

```bash
docker build -t qodercn-gateway .
docker run -p 8095:8095 -v "$PWD/credentials.json:/credentials.json:ro" \
  qodercn-gateway --remote-auth-file /credentials.json
```

## 目录结构

- `cmd/qodercn-gateway` —— 入口 / 配置装配
- `internal/httpapi` —— OpenAI + Anthropic HTTP 层（含可选的服务端工具）
- `internal/remote` —— QoderCN Remote API 客户端（cosy 签名、SSE、图片、凭证）
- `internal/service` —— 请求编排
- `internal/tooltypes` —— 工具数据类型 + 请求侧抽取器
- `internal/deploy` —— 凭证 / 部署包导出
