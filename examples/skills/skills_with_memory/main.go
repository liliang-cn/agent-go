package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/liliang-cn/agent-go/pkg/agent"
	"github.com/liliang-cn/agent-go/pkg/skills"
)

func main() {
	ctx := context.Background()
	homeDir, _ := os.UserHomeDir()
	testDir := filepath.Join(homeDir, ".agentgo", "data", "skills_memory_test")
	_ = os.RemoveAll(testDir)
	os.MkdirAll(testDir, 0755)

	// 获取 skills 目录路径
	cwd, _ := os.Getwd()
	skillsPath := filepath.Join(cwd, "examples", ".skills")

	fmt.Println("=== Skills + Memory 集成测试 ===")

	// 1. 创建带 Skills 和 Memory 的 Agent
	svc, err := agent.New("SkillMemoryBot").
		WithDBPath(filepath.Join(testDir, "agent.db")).
		WithMemory(
			agent.WithMemoryPath(filepath.Join(testDir, "memories")),
			agent.WithMemoryStoreType("file"),
		).
		WithSkills(agent.WithSkillsPaths(skillsPath)).
		Build()
	if err != nil {
		log.Fatalf("创建 Agent 失败: %v", err)
	}
	defer svc.Close()

	// 2. 显示可用的 Skills
	fmt.Println("【已加载的 Skills】")
	if svc.Skills != nil {
		skillsList, _ := svc.Skills.ListSkills(ctx, skills.SkillFilter{})
		for _, sk := range skillsList {
			fmt.Printf("  - %s: %s\n", sk.ID, sk.Description)
		}
	}
	fmt.Println()

	// 3. 第一轮：明确要求保存记忆 + 使用 Skill
	fmt.Println("【第一轮对话】保存用户偏好并使用 Skill")
	fmt.Println("用户: 请记住我是小明，一名 Python 数据科学开发者。然后用 test-skill 问候我。")

	res, err := svc.Chat(ctx, "请记住我是小明，一名 Python 数据科学开发者，我最喜欢用 Pandas 和 FastAPI。然后用 test-skill 问候我。")
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("SkillMemoryBot: %s\n\n", res.Text())

	// 4. 检查记忆文件
	fmt.Println("【检查记忆文件】")
	files, _ := filepath.Glob(filepath.Join(testDir, "memories", "entities", "*.md"))
	fmt.Printf("已保存 %d 条记忆:\n", len(files))
	for _, f := range files {
		content, _ := os.ReadFile(f)
		preview := string(content)
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		fmt.Printf("\n📄 %s\n%s\n", filepath.Base(f), preview)
	}

	// 5. 第二轮：验证记忆检索
	fmt.Println("\n【第二轮对话】验证记忆检索")
	fmt.Println("用户: 你还记得我是谁吗？我的技术栈是什么？")

	res, err = svc.Chat(ctx, "你还记得我是谁吗？我的技术栈是什么？")
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("SkillMemoryBot: %s\n\n", res.Text())

	// 6. 第三轮：使用另一个 Skill
	fmt.Println("【第三轮对话】使用 code-reviewer Skill")
	fmt.Println("用户: 请使用 code-reviewer skill 帮我审查这段代码：def add(a, b): return a + b")

	res, err = svc.Chat(ctx, "请使用 code-reviewer skill 帮我审查这段简单的 Python 代码：def add(a, b): return a + b")
	if err != nil {
		log.Fatalf("对话失败: %v", err)
	}
	fmt.Printf("SkillMemoryBot: %s\n", res.Text())

	fmt.Println("\n=== 测试完成 ===")
	fmt.Println("Skills + Memory 集成正常工作！")
}
