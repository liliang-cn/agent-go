// cognitive_layer 验证 AgentGo 认知记忆层的完整功能：
//
//   - FileMemoryStore：带 RevisionHistory 的记忆持久化
//   - _index/ 多文件索引：observations.md / facts.md 等
//   - Reflect()：LLM 把 facts 整合为 observations，带冲突检测
//   - IndexNavigator：LLM 读索引导航，无向量检索
//   - GetEvolution()：追溯 fact → observation 演化路径
//
// 运行方式（需要 Ollama 已启动，model: qwen3.5:latest）：
//
//	go run examples/memory/cognitive_layer/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liliang-cn/agent-go/v2/pkg/config"
	"github.com/liliang-cn/agent-go/v2/pkg/domain"
	"github.com/liliang-cn/agent-go/v2/pkg/memory"
	"github.com/liliang-cn/agent-go/v2/pkg/services"
	"github.com/liliang-cn/agent-go/v2/pkg/store"
)

func main() {
	ctx := context.Background()

	// ── 1. 初始化 LLM（从 agentgo.toml 读取，使用 ollama qwen3.5:latest） ────────
	agentgoCfg, err := config.Load("")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	globalPool := services.GetGlobalPoolService()
	if err := globalPool.Initialize(ctx, agentgoCfg); err != nil {
		log.Fatalf("初始化 LLM Pool 失败: %v", err)
	}

	llm, err := globalPool.GetLLMService()
	if err != nil {
		log.Fatalf("获取 LLM 失败（请确认 Ollama 已启动且 qwen3.5:latest 已拉取）: %v", err)
	}
	fmt.Println("✅ LLM 已就绪（来自 agentgo.toml）")

	// ── 2. 初始化 FileMemoryStore ──────────────────────────────────────────────
	memDir := filepath.Join(os.TempDir(), "agentgo-cognitive-demo")
	os.RemoveAll(memDir)
	defer os.RemoveAll(memDir)

	fileStore, err := store.NewFileMemoryStore(memDir)
	if err != nil {
		log.Fatalf("创建 FileMemoryStore 失败: %v", err)
	}
	fileStore.WithLLM(llm)

	memSvc := memory.NewService(fileStore, llm, nil, memory.DefaultConfig())
	fmt.Printf("✅ FileMemoryStore 已初始化: %s\n\n", memDir)

	sessionID := "demo-session-001"

	// ── 3. 写入一组 Facts（来源标注为 user_input） ─────────────────────────────
	fmt.Println("════════════════════════════════════════")
	fmt.Println("步骤 1: 写入 Facts（带 SourceType）")
	fmt.Println("════════════════════════════════════════")

	facts := []*domain.Memory{
		{
			ID:         "fact-001",
			SessionID:  sessionID,
			Type:       domain.MemoryTypeFact,
			Content:    "用户主要使用 Go 语言进行后端开发，已有 3 年经验。",
			Importance: 0.9,
			SourceType: domain.MemorySourceUserInput,
			ValidFrom:  time.Now(),
			CreatedAt:  time.Now(),
		},
		{
			ID:         "fact-002",
			SessionID:  sessionID,
			Type:       domain.MemoryTypeFact,
			Content:    "用户偏好本地优先（Local-First）的架构，不喜欢云依赖。",
			Importance: 0.85,
			SourceType: domain.MemorySourceUserInput,
			ValidFrom:  time.Now(),
			CreatedAt:  time.Now(),
		},
		{
			ID:         "fact-003",
			SessionID:  sessionID,
			Type:       domain.MemoryTypeFact,
			Content:    "用户最近在研究 RAG 系统，特别关注向量检索的精度问题。",
			Importance: 0.8,
			SourceType: domain.MemorySourceUserInput,
			ValidFrom:  time.Now(),
			CreatedAt:  time.Now(),
		},
		{
			ID:         "fact-004",
			SessionID:  sessionID,
			Type:       domain.MemoryTypeFact,
			Content:    "用户认为传统向量 RAG 的切片方式会破坏文档逻辑结构，影响检索质量。",
			Importance: 0.75,
			SourceType: domain.MemorySourceInferred, // agent 从行为推断
			ValidFrom:  time.Now(),
			CreatedAt:  time.Now(),
		},
		{
			ID:         "fact-005",
			SessionID:  sessionID,
			Type:       domain.MemoryTypePreference,
			Content:    "用户倾向于使用 SQLite 而不是 PostgreSQL，因为不想引入额外的基础设施依赖。",
			Importance: 0.8,
			SourceType: domain.MemorySourceUserInput,
			ValidFrom:  time.Now(),
			CreatedAt:  time.Now(),
		},
	}

	for _, f := range facts {
		if err := memSvc.Add(ctx, f); err != nil {
			log.Fatalf("写入 fact %s 失败: %v", f.ID, err)
		}
		fmt.Printf("  + [%s] %s (source: %s)\n", f.ID, truncate(f.Content, 50), f.SourceType)
	}

	// ── 4. 查看 _index/ 多文件索引 ───────────────────────────────────────────
	fmt.Println("\n════════════════════════════════════════")
	fmt.Println("步骤 2: 查看 _index/ 索引文件")
	fmt.Println("════════════════════════════════════════")

	// 强制重建索引
	if err := fileStore.RebuildIndex(ctx); err != nil {
		log.Fatalf("重建索引失败: %v", err)
	}

	indexDir := filepath.Join(memDir, "_index")
	entries, _ := os.ReadDir(indexDir)
	for _, e := range entries {
		fmt.Printf("\n📄 %s:\n", e.Name())
		content, _ := os.ReadFile(filepath.Join(indexDir, e.Name()))
		// 只显示 markdown body，跳过 frontmatter
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "type:") ||
				strings.HasPrefix(line, "total:") || strings.HasPrefix(line, "updated_at:") {
				continue
			}
			if strings.TrimSpace(line) != "" {
				fmt.Printf("  %s\n", line)
			}
		}
	}

	// ── 5. Reflect()：LLM 将 Facts 整合为 Observations ───────────────────────
	fmt.Println("\n════════════════════════════════════════")
	fmt.Println("步骤 3: Reflect() — LLM 整合 Facts → Observations")
	fmt.Println("════════════════════════════════════════")
	fmt.Println("  (调用 LLM 中，请稍候...)")

	msg, err := fileStore.Reflect(ctx, sessionID)
	if err != nil {
		log.Fatalf("Reflect 失败: %v", err)
	}
	fmt.Printf("  ✅ %s\n", msg)

	// 显示生成的 observations
	all, _, err := fileStore.List(ctx, 100, 0)
	if err != nil {
		log.Fatalf("List 失败: %v", err)
	}

	fmt.Println("\n  生成的 Observations:")
	var obsIDs []string
	for _, m := range all {
		if m.Type == domain.MemoryTypeObservation {
			obsIDs = append(obsIDs, m.ID)
			conflictTag := ""
			if m.Conflicting {
				conflictTag = " ⚠️ [冲突]"
			}
			fmt.Printf("  📝 [%s] confidence=%.2f%s\n     %s\n     evidence: %v\n",
				m.ID, m.Confidence, conflictTag, m.Content, m.EvidenceIDs)
		}
	}

	// ── 6. IndexNavigator：LLM 读索引导航检索 ────────────────────────────────
	fmt.Println("\n════════════════════════════════════════")
	fmt.Println("步骤 4: IndexNavigator — 无向量 LLM 导航检索")
	fmt.Println("════════════════════════════════════════")

	// 重建索引（让 observations 出现在索引中）
	_ = fileStore.RebuildIndex(ctx)

	query := "用户对 RAG 和向量检索有什么看法？"
	fmt.Printf("  查询: %q\n  (LLM 读索引中...)\n", query)

	nav := memory.NewIndexNavigator(fileStore, llm)
	results, err := nav.Navigate(ctx, query, 5)
	if err != nil {
		log.Printf("  Navigator 失败（非致命）: %v", err)
	} else {
		fmt.Printf("  找到 %d 条相关记忆:\n", len(results))
		for _, m := range results {
			fmt.Printf("  → [%s/%s] %s\n", m.Type, m.ID, truncate(m.Content, 60))
		}
	}

	// ── 7. MarkStale + RevisionHistory ────────────────────────────────────────
	fmt.Println("\n════════════════════════════════════════")
	fmt.Println("步骤 5: MarkStale + RevisionHistory 验证")
	fmt.Println("════════════════════════════════════════")

	// 模拟：fact-001 被一个新的更准确的事实取代
	newFactID := "fact-001-v2"
	newFact := &domain.Memory{
		ID:         newFactID,
		SessionID:  sessionID,
		Type:       domain.MemoryTypeFact,
		Content:    "用户主要使用 Go 语言进行后端开发，已有 5 年经验（更新：用户于近期晋升为高级工程师）。",
		Importance: 0.95,
		SourceType: domain.MemorySourceUserInput,
		ValidFrom:  time.Now(),
		CreatedAt:  time.Now(),
	}
	_ = memSvc.Add(ctx, newFact)

	if err := fileStore.MarkStale(ctx, "fact-001", newFactID); err != nil {
		log.Printf("  MarkStale 失败: %v", err)
	} else {
		// 读取旧 fact 验证 RevisionHistory
		old, _ := fileStore.Get(ctx, "fact-001")
		fmt.Printf("  旧 fact-001 状态:\n")
		fmt.Printf("    SupersededBy: %s\n", old.SupersededBy)
		if old.ValidTo != nil {
			fmt.Printf("    ValidTo: %s\n", old.ValidTo.Format(time.RFC3339))
		}
		if len(old.RevisionHistory) > 0 {
			rev := old.RevisionHistory[len(old.RevisionHistory)-1]
			fmt.Printf("    最新修订: by=%s at=%s summary=%q\n",
				rev.By, rev.At.Format(time.RFC3339), rev.Summary)
		}
		fmt.Printf("  ✅ RevisionHistory 工作正常\n")
	}

	// ── 8. GetEvolution()：追溯 fact 演化路径 ────────────────────────────────
	fmt.Println("\n════════════════════════════════════════")
	fmt.Println("步骤 6: GetEvolution() — 记忆演化路径")
	fmt.Println("════════════════════════════════════════")

	if len(obsIDs) > 0 {
		// 取第一条 observation 的一个证据 fact，追溯演化
		firstObs, _ := fileStore.Get(ctx, obsIDs[0])
		if len(firstObs.EvidenceIDs) > 0 {
			pivotID := firstObs.EvidenceIDs[0]
			fmt.Printf("  从 fact %q 追溯演化...\n", pivotID)

			node, err := memSvc.GetEvolution(ctx, pivotID)
			if err != nil {
				log.Printf("  GetEvolution 失败: %v", err)
			} else {
				printEvolution(node, 0)
			}
		}
	} else {
		fmt.Println("  (无 observation，跳过演化追溯)")
	}

	// ── 9. 索引文件最终状态 ───────────────────────────────────────────────────
	fmt.Println("\n════════════════════════════════════════")
	fmt.Println("步骤 7: 最终 _index/ 状态（含 stale 标记）")
	fmt.Println("════════════════════════════════════════")

	_ = fileStore.RebuildIndex(ctx)
	entries, _ = os.ReadDir(indexDir)
	for _, e := range entries {
		content, _ := os.ReadFile(filepath.Join(indexDir, e.Name()))
		lines := strings.Split(string(content), "\n")
		fmt.Printf("\n📄 _index/%s:\n", e.Name())
		inBody := false
		for _, line := range lines {
			if line == "---" {
				if inBody {
					break
				}
				continue
			}
			if strings.HasPrefix(line, "#") {
				inBody = true
			}
			if inBody && strings.TrimSpace(line) != "" {
				fmt.Printf("  %s\n", line)
			}
		}
	}

	fmt.Println("\n✅ 认知记忆层功能验证完成！")
	fmt.Printf("   记忆文件目录: %s\n", memDir)
}

// printEvolution 递归打印演化树
func printEvolution(node *memory.MemoryEvolutionNode, depth int) {
	if node == nil {
		return
	}
	indent := strings.Repeat("  ", depth)
	staleTag := ""
	if store.IsStale(node.Memory) {
		staleTag = " [stale]"
	}
	fmt.Printf("%s🔹 [%s/%s]%s\n%s   %s\n",
		indent, node.Memory.Type, node.Memory.ID, staleTag,
		indent, truncate(node.Memory.Content, 70))

	if node.EvidenceOf != nil {
		fmt.Printf("%s   └─ 成为 observation 的证据: [%s] %s\n",
			indent, node.EvidenceOf.ID, truncate(node.EvidenceOf.Content, 60))
	}

	for _, child := range node.Children {
		fmt.Printf("%s   ↳ 被取代为:\n", indent)
		printEvolution(child, depth+2)
	}
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
