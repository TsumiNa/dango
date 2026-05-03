# Orchestrator / Runner / Executor 架构说明

这份文档描述 `internal/engine/orchestrator.go`（简称 `or`）、`internal/engine/runner/runner.go`（简称 `ru`）和 `internal/engine/executor.go`（简称 `ex`）之间的调用关系和数据流关系。

当前设计把三者分成两条正交链路：

- 管理调用关系：`or` 对外接受 request，在 `StartRequest` 中创建 request-scoped stream，生成 coarse plan，装配并启动 `ru`；`ru` 维护单个 plan 的生命周期并调度多个 `ex`；上层程序通过 `or` 的 query API 和 `Response.Stream` 观察处理状态，不需要感知 `ru` 和 `ex`。
- 数据流关系：skill 间的数据交换统一收敛为带 front matter 的 markdown exchange document。`ru` 只负责搬运、补齐 runner/node 元数据、持久化和转发这些文档，不解释 skill 业务语义。

## 调用关系

`or` 是外部控制面，`ru` 是单个 request/plan 的生命周期宿主，`ex` 是单个 node 的执行单元。

```mermaid
sequenceDiagram
    autonumber
    participant C as User / API / CLI
    participant O as or: Orchestrator
    participant S as Request Stream
    participant OS as Orchestrator Skill
    participant SR as Skill Registry
    participant RR as Runner Registry
    participant Q as Priority Queue
    participant R as ru: Runner
    participant D as DAG Engine
    participant E as ex: Executor(s)

    C->>O: StartRequest req
    O->>S: create request-scoped stream
    O->>OS: plan(request, skill summaries)
    OS-->>O: strict JSON CoarsePlan or RejectReason
    O-->>S: planning reasoning / output / status
    O-->>S: planning exchange markdown
    O->>SR: resolve lightweight skills + AddSkillConfig
    SR-->>O: skill configs
    O->>O: buildPlanNodes / newRunnerFromPlan
    O->>RR: store Runner + Node graph
    O->>Q: enqueue by priority
    Q-->>R: start when admitted
    R->>R: StartManaged
    Note over R: created -> polishing -> awaiting_review<br/>-> executing -> report -> settled
    R->>R: prepareNodeExecutors / BindForRunner + session reuse
    R->>E: Polish(ctx)
    E-->>R: polish exchange markdown
    R->>D: schedule dependency-ready nodes
    D->>E: Execute(ctx)
    E-->>D: execution exchange markdown
    D-->>R: completed node
    R->>E: Report(ctx)
    E-->>R: report exchange markdown
    R-->>S: runner / executor / skill stream events
    C->>S: subscribe(resp.Stream)
    C->>O: QueryRunner / LoadRunnerRecords
    O->>RR: lookup runner state
    RR-->>O: RunnerView
    O-->>C: runner status and stored views
```

关键边界：

- `or` 管外部 API、skill 注册、coarse plan 生成、runner 装配、runner registry、query/subscribe、队列和启动准入。
- `ru` 管单个 plan 的生命周期、executor binding、DAG 调度、dynamic node append、snapshot/update/event、report 汇总和终态。
- `ex` 管单个 node 在 `Polish`、`Execute`、`Report` 三个阶段的局部工作。

## 数据流关系

`ex` 的默认输出不再是任意 Go 值，而是 markdown exchange document。这个文档可以落库、落本地文件，也方便 human review 和 skill 继续消费。

`or` 的 planner / reviewer prompt 边界仍然使用严格 JSON：规划阶段返回 `{"plan": ...}` 或 `{"reject": ...}`，review / replan 也都返回 JSON。为了和其他阶段的数据面保持一致，`or` 会把 planning 结果额外包装成一份 markdown exchange document 发到 request stream，并交给 renderer / artifact subscriber 落盘；同时 planner reasoning、review/replan reasoning 也通过同一条 request stream 对外可见。

每个 exchange document 使用 YAML front matter 描述路由和元数据，正文固定分为三段：

- `Memo`：长任务、多阶段任务的自主规划、进度和状态记录。
- `Reasoning`：human-debuggable 的推理摘要；如果 runtime skill 没有显式写入，`ex` 会尝试从 reasoning turn 补齐。
- `Handoff`：传递给目标 recipient 的输出、接力建议或请求。

```mermaid
sequenceDiagram
    autonumber
    participant I as Planning Input
    participant O as or
    participant U as Request stream
    participant OS as Orchestrator Skill
    participant R as ru
    participant E as ex
    participant ST as RunnerStore

    I->>O: Request + skill summaries
    O->>OS: plan(request, skill summaries)
    OS-->>O: strict JSON plan / reject
    O-->>U: planning reasoning / output / status
    O-->>U: planning exchange markdown
    O->>R: Node graph + Node metadata + Executor
    R->>E: Polish(ctx)
    E-->>R: kind=dango.exchange, stage=polish
    R->>R: save PolishDocuments map[nodeID]markdown
    R->>OS: review(plan, polish_documents)
    Note over R,OS: markdown docs, not runner internals
    OS-->>R: review JSON: approved / reject
    R-->>U: review / replan reasoning and compact runner events

    alt review rejected
        R->>OS: replan(request, currentPlan, reason, polish_documents)
        OS-->>R: replan JSON / revised CoarsePlan
        R->>R: ReplanWith(revised plan, rebuilt nodes)
        R->>E: Polish(ctx)
        E-->>R: revised polish exchange markdown
    else review approved
        R->>R: AcceptPolishedPlan(plan)
    end

    loop dependency-ready nodes
        R->>E: Execute(ctx, parent exchange markdown)
        E-->>R: kind=dango.exchange, stage=execute, optional newNodes
        R->>R: save CompletedNodes map[nodeID]markdown
        opt optional newNodes
            R->>R: append to Node graph
        end
        R->>ST: append NodeCompleted(markdown)
        R-->>U: publish runner / executor / artifact events
    end

    par completed nodes
        R->>E: Report(ctx, execution markdown)
        E-->>R: kind=dango.exchange, stage=report
        R->>R: save ReportSummaries map[nodeID]markdown
        R->>ST: append report markdown
        R-->>U: publish report-stage stream events
    end
```

这里的重点是：`ru` 不需要理解 `Memo`、`Reasoning`、`Handoff` 的业务含义。它只做四件事：

- 接收 `ex` 返回的 exchange markdown。
- 为 exchange markdown 补齐 `runner_id`、`node_id`、`skill_name`、`task_description` 等元数据。
- 把 markdown 作为 node output、polish document 或 report summary 继续传递。
- 在持久化事件时把 exchange markdown 标记为 `data_encoding=markdown`，避免把 human-readable 数据降级成 JSON 字符串。

## Lifecycle Sequence

下面是当前 managed lifecycle 的端到端时序。对外入口只有 `or.StartRequest`；它返回 `Response{RunnerID, Stream}`，后续 polish、review、replan、execute、report 由 `ru.StartManaged` 推进。

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant O as or
    participant U as request stream
    participant OS as or skill
    participant R as ru
    participant E as ex
    participant ST as RunnerStore

    C->>O: StartRequest(ctx, req)
    O->>O: validate request / lock startup config
    O->>OS: plan(request, skill summaries)
    OS-->>O: strict JSON CoarsePlan or RejectReason
    O-->>U: planning reasoning / output / status
    O-->>U: planning exchange markdown

    alt rejected
        O-->>C: RequestRejectedError
    else planned
        O->>O: build Node graph + Executor per CoarsePlanNode
        O->>R: NewWithSetup(Setup{Plan, Nodes, PlannerSkill, PlanNodeBuilder})
        O->>O: store runner in registry
        O->>R: StartManaged(ctx) or queue by priority
        O-->>C: Response{runnerID, stream}
    end

    R->>R: StartPolish
    R->>R: prepareNodeExecutors(initial nodes)
    par each initial node
        R->>E: Polish(ctx)
        E-->>R: exchange markdown(stage=polish)
    end

    R->>OS: review(plan, polish_documents)
    OS-->>R: PlanReview

    loop until review approved
        alt rejected
            R->>R: RejectPolishedPlan(reason)
            R->>OS: replan(request, currentPlan, reason, polish_documents)
            OS-->>R: revised CoarsePlan
            R->>R: ReplanWith(revised plan, rebuilt nodes)
            R->>E: Polish(ctx)
            E-->>R: revised exchange markdown(stage=polish)
            R->>OS: review(...)
            OS-->>R: PlanReview
        else approved
            R->>R: AcceptPolishedPlan(plan)
        end
    end

    loop dependency-ready nodes
        R->>E: Execute(ctx, parent exchange markdown)
        E-->>R: exchange markdown(stage=execute), optional newNodes
        R->>ST: append NodeCompleted(markdown)
        R-->>U: runner / executor / artifact events
    end

    R->>R: Complete on EngineIdle
    par completed nodes
        R->>E: Report(ctx, execution markdown)
        E-->>R: exchange markdown(stage=report)
    end
    R->>ST: append terminal records
    R-->>U: phase=settled and terminal events
    O-->>C: query / request stream expose status through or APIs
```

## Exchange Document Shape

示例：

```markdown
---
kind: dango.exchange
version: 1
stage: execute
runner_id: runner-123
node_id: summarize
skill_name: summarizer
task_description: Summarize the collected findings.
handoffs:
  - to: downstream
    intent: continue
    summary: Use this summary as downstream context.
---

## Memo

Progress and durable task state.

## Reasoning

Human-debuggable reasoning summary.

## Handoff

Recipient-facing output and next-step advice.
```

当前约定的 recipient / intent：

- `to: orchestrator`, `intent: review`：交给 or skill 做 plan review。
- `to: orchestrator`, `intent: summarize`：交给 or skill 或最终汇总阶段消费。
- `to: orchestrator`, `intent: rerun_previous`：请求 or skill 评估是否需要让前置 executor 修改参数并重新执行。
- `to: downstream`, `intent: continue`：交给后续依赖节点作为 parent output。

## 特殊路径：要求前置 Executor 重跑

常规数据流中，`ru` 不解释 skill 输出，只把 exchange markdown 传给下一阶段。唯一需要跨回控制面的特殊情况是：某个 `ex` 判断前置 `ex` 的参数需要调整并重新执行。

这条路径的设计是由 `ex` 在 exchange document 中写出 `to: orchestrator`、`intent: rerun_previous` 的 handoff；orchestrator skill 评估后，通过工具调用进入 `or` 控制面，由 `or` 定位目标 runner 并调用 `ru.AddNodes` 追加修正节点。

```mermaid
flowchart TB
    EX2[ex: downstream node]
    Doc[Exchange Markdown\nhandoff: to=orchestrator\nintent=rerun_previous]
    RU[ru: carries document\nno business interpretation]
    OS[or skill: evaluates rerun request]
    Tool[tool call\nrequest_rerun / add_nodes]
    OR[or: validates runner + skill + plan context]
    Add[ru.AddNodes]
    EX1R[replacement / repair ex node]

    EX2 --> Doc
    Doc --> RU
    RU --> OS
    OS --> Tool
    Tool --> OR
    OR --> Add
    Add --> EX1R
    EX1R --> RU
```

当前 PR 已落地 exchange document、`rerun_previous` intent 常量和 markdown 数据面；`or` skill 的 tool call 到 `ru.AddNodes` 是下一步控制面接入点。

## 持久化和可观察性

`RunnerStore` 继续使用 append-only JSONL runner record。变化点是 node output 如果是合法 exchange markdown，会以 `data_encoding=markdown` 存入 `StoredRunnerEvent.DataText`，而不是作为普通 JSON string 存入 `DataJSON`。对外实时观察面则统一走 `StartRequest` 返回的 `Response.Stream`。

```mermaid
flowchart LR
    E[ex output\nExchange Markdown] --> R[ru annotateExchangeOutput]
    R --> S[RunnerSnapshot.CompletedNodes]
    R --> U[Response.Stream Event.Delta]
    R --> P[RunnerStore JSONL\nDataEncoding=markdown\nDataText=raw markdown]
    O[or query/subscribe APIs] --> S
    O --> U
    O --> P
    C[Caller] --> O
```

这让同一份数据可以同时满足三种需求：

- 数据库存储：front matter 是稳定 metadata，markdown 正文保留 human-readable 内容。
- 本地文件存储：exchange document 可以直接落成 `.md`。
- skill 间传递：后续 skill 可以直接读取 parent output markdown，不需要理解 `ru` 内部结构。

## 一句话总结

`or` 负责对外和控制面，并创建 request-scoped stream；`ru` 负责单个 plan 的生命周期和 DAG 调度；`ex` 负责单个 node 的执行；三者之间的数据面统一使用带 front matter 的 markdown exchange document，而 outward observability 统一走 `Response.Stream`，让持久化、human review、skill handoff 和终端观察尽量对齐同一种结构。
