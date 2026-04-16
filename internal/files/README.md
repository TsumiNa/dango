# skillframe — 最小化 LLM Skill 框架

LLM にスキル（専門知識・手順書）を動的に注入するための Go フレームワーク。  
Claude の SKILL.md パターンをベースに、拡張可能な最小設計。

## アーキテクチャ

```
┌─────────────────────────────────────────────────────┐
│                     Executor                         │
│  query → Registry.Match() → buildSystemPrompt()     │
│        → LLM.Complete() → Result                    │
└──────┬──────────────┬──────────────┬────────────────┘
       │              │              │
  ┌────▼────┐   ┌─────▼─────┐  ┌────▼────┐
  │Registry │   │  Matcher   │  │   LLM   │
  │         │   │ (interface)│  │ (Client) │
  │ skills/ │   ├───────────┤  ├─────────┤
  │ ├─ A/   │   │ Keyword   │  │Anthropic│
  │ ├─ B/   │   │ LLM-based │  │ OpenAI  │
  │ └─ C/   │   │ Embedding │  │  Local  │
  └─────────┘   └───────────┘  └─────────┘
```

### 3段階のコンテキスト注入 (Progressive Disclosure)

1. **Metadata** — `name` + `description` (常にコンテキストに存在、ルーティング用)
2. **Body** — SKILL.md の本文 (マッチしたスキルのみ注入)
3. **Resources** — バンドルファイル (必要時のみ読み込み)

## ディレクトリ構造

```
skillframe/
├── skill/           # Skill 型定義、SKILL.md パーサー
│   └── skill.go
├── registry/        # スキルの登録・検索・マッチング
│   ├── registry.go      # Registry + KeywordMatcher
│   └── llm_matcher.go   # LLM ベースの高精度マッチャー
├── llm/             # LLM クライアント抽象
│   ├── client.go        # Client interface
│   └── anthropic.go     # Anthropic API 実装
├── executor/        # オーケストレーション
│   └── executor.go      # Match → Prompt 構築 → LLM 呼び出し
├── cmd/
│   └── main.go      # 使用例
└── skills/          # スキル定義 (SKILL.md ファイル群)
    ├── geotech-analysis/
    │   └── SKILL.md
    └── data-viz/
        └── SKILL.md
```

## Skill の書き方

```markdown
---
name: my-skill
description: "いつこのスキルを使うか。トリガーキーワードを含める。"
---

# スキルタイトル

## Overview
何をするスキルか。

## 手順
1. ステップ1
2. ステップ2

## ルール
- 守るべき制約
```

### バンドルリソース

```
my-skill/
├── SKILL.md
├── scripts/       # 実行可能コード
├── references/    # 追加ドキュメント
└── assets/        # テンプレート等
```

SKILL.md 内で `ReadResource("references/guide.md")` として参照可能。

## 拡張ポイント

### 1. Matcher を差し替える

```go
// Embedding ベースのマッチャー
type EmbeddingMatcher struct {
    embedder EmbeddingClient
    cache    map[string][]float64
}

func (m *EmbeddingMatcher) Score(query string, s *skill.Skill) float64 {
    qVec := m.embedder.Embed(query)
    sVec := m.cache[s.Name] // 事前計算済み
    return cosineSimilarity(qVec, sVec)
}

reg := registry.New(&EmbeddingMatcher{...}, log)
```

### 2. LLM プロバイダーを追加

```go
// llm.Client interface を実装するだけ
type OllamaClient struct { BaseURL string }

func (c *OllamaClient) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
    // Ollama API を呼ぶ
}
```

### 3. dango との統合

Executor を dango の Execution AI レイヤーに組み込む:

```go
// dango の task handler 内で
func handleTask(ctx context.Context, task *dango.Task) error {
    result, err := skillExecutor.Run(ctx, task.Prompt)
    if err != nil {
        return err
    }
    return task.WriteHandoff(result.Response.Content)
}
```

## 使い方

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/main.go
```

## ライセンス

MIT
