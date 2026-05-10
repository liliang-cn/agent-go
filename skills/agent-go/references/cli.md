# AgentGo CLI Usage

## Chat

```bash
agentgo chat
agentgo chat "Create a task plan for validating this repo"
agentgo chat --session my-session
```

Inside interactive chat:

```text
/plans
/plan ready <plan_id>
/plan submit <plan_id> <item_id> [agent_name]
```

## Agents

```bash
agentgo agent list
agentgo agent show Dispatcher
agentgo agent show Operator
agentgo agent run --agent Operator "Run git status and summarize it"
```

## Teams

```bash
agentgo team list
agentgo team add "Docs Team" --description "Documentation work"
agentgo team status "Docs Team"
```

## Tasks

```bash
agentgo task list
agentgo task get <task_id>
agentgo task inspect <task_id>
agentgo task trace <task_id>

# Yield/resume — pause a running task, resume with new user input
agentgo task yield <task_id> --reason "waiting for human approval"
agentgo task resume <task_id> "approved, continue"

# Checkpoint / replay — re-run from the latest message-history snapshot
agentgo task checkpoints <task_id>             # list snapshots
agentgo task replay <task_id>                  # rerun from latest
agentgo task replay <task_id> --checkpoint X   # rerun from a specific snapshot
agentgo task replay <task_id> --follow-up "..."

agentgo task cancel <task_id>
```

## Eval

```bash
agentgo eval                              # mock-only run (CI-safe)
agentgo eval --profile=live               # real-LLM run via configured provider
agentgo eval --profile=all                # both
agentgo eval --filter lint_dispatcher     # substring filter on scenario name
agentgo eval --runs 3 --save              # repeat each scenario 3x, save JSON to eval/results/
```

## LLM Providers

```bash
agentgo llm list
agentgo llm add --name local --url http://localhost:11434/v1 --model qwen2.5
agentgo llm add --name deepseek --url https://api.deepseek.com --model deepseek-v4-flash --key sk-...
agentgo llm update --name local --model qwen2.5
agentgo llm update --name local --enabled=false      # toggle without delete
agentgo llm test local                                # one round-trip probe
agentgo llm rank local                                # 6-test capability rank
agentgo llm delete local
```

## Storage

Default home:

```text
~/.agentgo/
├── data/agentgo.db
├── data/cortex.db
├── memories/
├── skills/
└── workspace/
```

Use a temporary runtime when validating CLI behavior:

```bash
AGENTGO_HOME="$(mktemp -d)" agentgo chat --session smoke
```

