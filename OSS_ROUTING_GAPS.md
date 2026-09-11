# Bifrost OSS：会话粘性与故障恢复的两个 Gap

日期：2026-09-11

检查基线：当前仓库 commit `0145f674e`

范围：仅讨论本 checkout 的 OSS LLM 路由能力，不将 Enterprise 文档或界面入口视为 OSS 已实现功能。结论来自代码检查，未进行真实供应商故障演练。

> 后续实现：`feat/session-routing-outage-recovery` 分支已新增可选策略，见 [配置与范围](docs/features/routing-resilience.mdx) 和 [容器测试](tests/routingresilience/README.md)。下文保留基线问题描述。

## 背景与目标

希望由 Gateway 自动完成 Load Balance 和跨模型、跨 provider fallback，避免管理员频繁手动切换。用户保持原有客户端配置和应用 session，并尽可能减少缓存损失、等待时间及切换副作用。

当前 OSS 已有加权路由、API key stickiness、单请求 retry 和 fallback，但这些能力尚未形成“健康时保持会话目标，故障时持续避让并自动恢复”的完整行为。

| Gap | 当前行为 | 期望行为 |
| --- | --- | --- |
| Provider/model 层缺少 session stickiness | 每次请求可重新加权选择目标；选定目标后才复用 key | 新 session 按权重分配，后续轮次优先复用同一目标，保护 prompt cache 局部性 |
| 缺少跨请求故障记忆与主动恢复 | 本次失败可以 fallback，下次仍可能选中同一个坏目标 | 自动暂时摘除故障目标，后台探测，恢复后重新接收流量 |

## Gap 1：加权路由没有保护同一 session 的 provider/model 稳定性

### 当前行为

路由分成两个层次：

```text
每次请求
  → 按权重选择 provider/model
  → 在该 provider/model 内选择 API key，应用 key stickiness
```

Routing rule 的 target 选择和 Governance 的 provider 分流均使用加权随机，没有在这一步根据 session 复用上一次的 provider/model。

已有的 key stickiness 以 `provider + model + session` 为绑定维度，保存对应的 API key ID：

- 需要有效 session ID 和 KV store，且当前不是 fallback attempt。
- 首次建立绑定时，通过 key selector 选择 key，默认使用加权随机。
- 后续请求再次来到同一 provider/model 时，复用仍符合条件的 key，并刷新 TTL。
- 默认 TTL 为 1 小时，可通过 `x-bf-session-ttl` 覆盖。
- sticky key 在 retry 中保持固定，包括限流重试；fallback 阶段关闭这层 stickiness。

这意味着 key 绑定在 provider/model 选择之后生效，不能阻止上层目标发生变化。已有的 complexity session 状态也只保存复杂度等级，不保存模型选择或对话历史。

### 问题示例

同一 session 连续三轮请求，A 和 B 的权重均为 50%：

| 轮次 | 路由结果 | 缓存影响 |
| --- | --- | --- |
| 1 | A / Model X / Key A2 | 在 A 对应路径上建立 prompt cache |
| 2 | B / Model X / Key B1 | 无法通过现有 stickiness 复用 A 路径上的缓存 |
| 3 | A / Model X / Key A2 | 可能复用第一轮缓存，取决于上游缓存条件及有效期 |

不是每次切换都必然 cache miss，也不是固定 key 就保证 cache hit；准确的问题是：当前路由没有主动保护跨轮次的缓存局部性，也不依据缓存命中情况选择目标。

### 用户影响

- 同一对话的缓存可能分散到多个 provider，增加重复预填充、输入成本和首 token 等待时间。
- 若加权目标是不同模型，还可能出现回答风格、能力及工具行为变化。
- 调整 key 权重无法解决上层 provider/model 来回切换的问题。
- 单次 fallback 成功后，没有通用的机制将 session 重新绑定到成功的备用 provider/model。

### 期望行为

以 session 为初始分流单位，而不是对每轮请求重新抽取 provider/model：

1. 新 session 在符合授权、能力与健康条件的目标中按权重分配。
2. 后续轮次优先复用原 provider/model，并保留适用的 key stickiness。
3. 原目标不可用时自动 fallback；成功后更新会话目标，避免下一轮立即回到故障目标。
4. 权重调整主要影响新 session，尽量不打散已有绑定。
5. 绑定不能绕过权限、模型许可、预算限制或管理员禁用；不再符合条件时必须重新选择。

这是一项待补能力，并非当前 OSS 的配置选项。它改善缓存命中的条件，不承诺上游一定命中缓存。按 session 分配也意味着请求量比例不一定严格等于权重，因为不同 session 的长度不同。

### 代码依据

- [plugins/routing/rules/engine.go](plugins/routing/rules/engine.go)：`selectWeightedTarget`，加权随机选择模型目标。
- [plugins/governance/main.go](plugins/governance/main.go)：`LoadBalanceProvider`，provider 加权选择及备用链生成。
- [core/bifrost.go](core/bifrost.go)：`selectKeyFromProviderForModelWithPool`，key 绑定、retry 固定 key、fallback 不启用 stickiness。
- [core/utils.go](core/utils.go)：`buildSessionKey`，绑定维度为 provider/model/session。
- [core/schemas/kvstore.go](core/schemas/kvstore.go)：`DefaultSessionStickyTTL`。
- [plugins/routing/complexitysession.go](plugins/routing/complexitysession.go)：只保存复杂度等级的 session 状态。

## Gap 2：缺少 LLM 路由的跨请求熔断、冷却摘除与后台恢复探测

### 当前行为

OSS 可以在当前请求失败后执行 retry，并按备用链尝试其他 provider/model。但当前请求的失败不会自动形成供后续请求使用的 provider/model 冷却状态。

Core 中的失败 key 记录属于当前请求的 retry 周期，不等于跨请求、跨 session 的健康状态。当前 OSS 未找到以下完整机制：

- 记住某条 LLM 路由已故障，并在一段时间内从业务候选中摘除。
- 冷却期间定期发送独立的后台推理探测。
- 探测成功后提前解除摘除并恢复流量。

### 问题示例

```text
请求 1 → A 超时 → retry → fallback 到 B 成功
请求 2 → 加权路由再次选中 A → 超时 → retry → fallback 到 B 成功
请求 3 → 仍有可能重复上述过程
```

系统可以最终返回成功，但每个选中 A 的请求都可能重复付出故障检测和重试的等待成本。

### 用户影响

- 故障期间反复出现高延迟；fallback 的成功率掩盖了用户体验恶化。
- 请求持续打向已经不可用的目标，消耗连接、并发资源及重试预算。
- 管理员可能仍需手动调权或禁用目标，恢复后再手动启用。
- 与 session stickiness 组合后，如果没有健康状态优先级，粘性还可能持续把会话留在坏目标上。

### 期望行为

以下以“冷却 10 分钟、每 10 秒探测”为需求示例，数值应可配置，不是当前 OSS 已有配置：

1. 当目标满足故障判定条件时，记录跨请求可见的故障状态，进入 10 分钟冷却。
2. 冷却期间，新请求和已有 session 均绕过该目标，使用符合条件的健康目标。
3. 后台每 10 秒向被摘除的实际模型/部署发送一次低成本推理探测，不让探测 fallback 到其他目标而产生假成功。
4. 探测成功并满足恢复条件后，提前解除摘除；探测失败则继续保持冷却。
5. 冷却到期时以受控试探决定恢复，不因时间到期就把全部业务流量直接压回尚未恢复的目标。
6. 恢复的目标可重新承接新 session；已切换并稳定运行的 session 不应被强制迁回，避免再一次缓存损失。

```text
健康 → 达到故障阈值 → 冷却摘除，业务走备用
                         ↓
                   后台定期探测
                         ↓
               成功满足条件 → 恢复候选资格
               失败         → 保持摘除
```

### 需要明确的边界

- **故障粒度**：优先按 provider/model/部署记录，必要时细化到 key；单 key 限流不应默认禁用整个 provider。
- **触发条件**：区分网络故障、5xx、限流、凭证错误和用户参数错误；失败次数、观察窗口及恢复成功次数需要明确。
- **探测有效性**：简单请求必须验证对应故障路径；一个轻量请求成功，不一定证明大上下文或流式路径已经恢复。
- **并发与多实例**：避免每个业务请求或每个节点重复探测同一个目标；多实例部署需明确健康状态的共享范围。
- **可观察性**：应能看到摘除原因、冷却期限、最近探测结果及恢复事件。

### OSS 与其他健康机制的区别

仓库包含 Circuit Breaker 文档和 UI 入口，但文档明确标为 Enterprise，OSS 页面显示升级提示。文档描述的是响应 header 信号触发、冷却期间转移流量、到期后由下一个业务请求试探，并不能据此认为 OSS 已支持“冷却期间每 10 秒主动探测并提前恢复”。

仓库中的 OTel 导出熔断用于遥测 collector，HTTP `/health` 主要检查 Gateway 及存储依赖；它们不构成 LLM provider 路由的健康摘除机制。

### 代码与文档依据

- [core/bifrost.go](core/bifrost.go)：`executeRequestWithRetries` 内的 `deadKeyIDs` 为请求内状态；`shouldTryFallbacks` 决定当前请求是否进入备用链。
- [plugins/governance/main.go](plugins/governance/main.go)：`LoadBalanceProvider` 没有根据此前上游故障保存并应用冷却状态。
- [docs/enterprise/circuit-breaker.mdx](docs/enterprise/circuit-breaker.mdx)：Enterprise 熔断的功能范围与恢复行为。
- [OSS Circuit Breaker UI](ui/app/_fallbacks/enterprise/components/circuit-breaker/circuitBreakerView.tsx)：Enterprise 升级提示。
- [plugins/otel/main.go](plugins/otel/main.go)：遥测导出的局部熔断。
- [transports/bifrost-http/handlers/health.go](transports/bifrost-http/handlers/health.go)：Gateway 健康端点。

## 两个 Gap 的联合验收场景

以下是期望行为的验证场景，不代表现有测试已覆盖或实现已完成。

| 场景 | 期望结果 |
| --- | --- |
| 同一 session 连续多轮，目标均健康 | provider/model 保持稳定，适用时复用 key |
| 多个新 session 到来 | 在健康候选中按权重分配初始目标 |
| A 故障，会话从 A fallback 到 B 成功 | 会话后续留在 B；其他请求也绕过已摘除的 A |
| A 冷却期间后台探测成功 | 提前恢复 A 的候选资格，无需管理员操作 |
| A 冷却期结束但仍故障 | 受控试探失败后继续摘除，不恢复全部流量 |
| A 恢复，旧 session 已在 B 稳定运行 | 旧 session 保持 B，新 session 可再次分配到 A |
| 绑定目标的权限被撤销或被管理员禁用 | 粘性失效，重新选择符合条件的目标 |

这两项能力解决的是请求之间的目标稳定性和故障避让。流式输出中途的无缝续写、供应商私有会话状态迁移、工具执行的幂等性仍是独立问题，不能因为补齐这两个 Gap 就承诺零副作用。
