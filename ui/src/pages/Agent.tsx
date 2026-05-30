import { ChangeEvent, FormEvent, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  useCreateAgent,
  useCreateTeam,
  useAgents,
  useTeams,
  useDispatchAgentTask,
  useDispatchTeamTask,
} from "@/hooks/useApi";
import type {
  AgentModel,
  CreateAgentRequest,
  CreateTeamRequest,
  Team,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Markdown } from "@/components/Markdown";

const selectClass =
  "flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

function formatDate(input: string | undefined, t: (key: string) => string) {
  if (!input) return t("unknown");
  const date = new Date(input);
  if (Number.isNaN(date.getTime())) return input;
  return date.toLocaleString();
}

function kindLabel(kind: AgentModel["kind"], t: (key: string) => string) {
  if (kind === "specialist") return t("kindSpecialist");
  if (kind === "agent") return t("kindAgent");
  return t("kindCaptain");
}

function capabilityBadges(agent: AgentModel, t: (key: string) => string) {
  const badges = [
    { enabled: agent.enable_ptc, label: t("capabilityPTC") },
    { enabled: agent.enable_memory, label: t("capabilityMemory") },
    { enabled: agent.enable_mcp, label: t("capabilityMCP") },
    { enabled: agent.enable_rag, label: t("capabilityRAG") },
  ];
  return badges.filter((badge) => badge.enabled);
}

function AgentCard({
  agent,
  onExecute,
  isExecuting,
}: {
  agent: AgentModel;
  onExecute: (instruction: string) => Promise<{ response: string; durationMs: number } | undefined>;
  isExecuting: boolean;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [instruction, setInstruction] = useState("");
  const [result, setResult] = useState<{ response: string; durationMs: number } | null>(null);

  const handleExecute = async (e: FormEvent) => {
    e.preventDefault();
    if (!instruction.trim()) return;
    const res = await onExecute(instruction.trim());
    setInstruction("");
    if (res) setResult(res);
  };

  return (
    <Card>
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        className="flex w-full items-center justify-between gap-4 px-5 py-4 text-left"
        data-testid={`agent-row-${agent.name}`}
      >
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-lg font-medium text-foreground">
              {agent.name}
            </span>
            <Badge variant="secondary">{kindLabel(agent.kind, t)}</Badge>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">{agent.description}</p>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {capabilityBadges(agent, t).map((badge) => (
              <Badge key={badge.label} variant="outline" className="text-muted-foreground">
                {badge.label}
              </Badge>
            ))}
          </div>
        </div>
        <span className="text-sm text-muted-foreground">{expanded ? "−" : "+"}</span>
      </button>

      {expanded && (
        <div className="border-t px-5 py-4">
          <p className="text-sm leading-7 text-muted-foreground">
            {agent.instructions}
          </p>

          <form onSubmit={handleExecute} className="mt-4 flex gap-2">
            <Input
              value={instruction}
              onChange={(e) => setInstruction(e.target.value)}
              placeholder={
                t("instructionPlaceholder") || "Enter instruction..."
              }
              disabled={isExecuting}
            />
            <Button type="submit" disabled={isExecuting || !instruction.trim()}>
              {isExecuting ? t("running") : t("run")}
            </Button>
          </form>

          {result && (
            <div
              className="mt-4 rounded-lg border border-border bg-muted/40 p-3"
              data-testid={`agent-result-${agent.name}`}
            >
              <div className="mb-1 flex items-center justify-between">
                <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  回答
                </span>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {(result.durationMs / 1000).toFixed(1)}s
                </span>
              </div>
              <Markdown>{result.response}</Markdown>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

function TeamCard({
  team,
  onExecute,
  isExecuting,
}: {
  team: Team;
  onExecute: (message: string) => Promise<{ taskId: string; ack: string } | undefined>;
  isExecuting: boolean;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [message, setMessage] = useState("");
  const [result, setResult] = useState<{ taskId: string; ack: string } | null>(null);

  const handleExecute = async (e: FormEvent) => {
    e.preventDefault();
    if (!message.trim()) return;
    const res = await onExecute(message.trim());
    setMessage("");
    if (res) setResult(res);
  };

  return (
    <Card>
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        className="flex w-full items-center justify-between gap-4 px-5 py-4 text-left"
        data-testid={`team-row-${team.id}`}
      >
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-lg font-medium text-foreground">
              {team.name}
            </span>
            <span className="rounded-md bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-800">
              {t("teams")}
            </span>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">{team.description}</p>
        </div>
        <span className="text-sm text-muted-foreground">{expanded ? "−" : "+"}</span>
      </button>

      {expanded && (
        <div className="border-t px-5 py-4">
          <div className="mb-3 flex flex-wrap gap-2">
            <span className="text-xs text-muted-foreground">
              {t("captainLabel")}:{" "}
              {team.lead_agent?.name ?? team.captain?.name ?? t("unknown")}
            </span>
            <span className="text-xs text-muted-foreground">
              {t("members")}: {team.members.length}
            </span>
            <span className="text-xs text-muted-foreground">
              {t("created")}: {formatDate(team.created_at, t)}
            </span>
          </div>

          {team.members.length > 0 && (
            <div className="mb-4">
              <p className="mb-2 text-xs uppercase tracking-[0.24em] text-muted-foreground">
                {t("members")}
              </p>
              <div className="flex flex-wrap gap-2">
                {team.members.map((member) => (
                  <Badge key={member.id} variant="secondary">
                    {member.name}
                  </Badge>
                ))}
              </div>
            </div>
          )}

          <form onSubmit={handleExecute} className="flex gap-2">
            <Input
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              placeholder={t("messagePlaceholder") || "Enter message..."}
              disabled={isExecuting}
            />
            <Button type="submit" disabled={isExecuting || !message.trim()}>
              {isExecuting ? t("running") : t("run")}
            </Button>
          </form>

          {result && (
            <div
              className="mt-4 rounded-lg border border-border bg-muted/40 p-3 text-sm"
              data-testid={`team-result-${team.id}`}
            >
              <p className="text-foreground">{result.ack || "任务已派发。"}</p>
              {result.taskId && (
                <p className="mt-1 text-xs text-muted-foreground">
                  团队任务为异步执行,结果请到{" "}
                  <Link
                    to={`/tasks/${result.taskId}`}
                    className="text-foreground underline"
                  >
                    任务 {result.taskId.slice(0, 8)}
                  </Link>{" "}
                  查看。
                </p>
              )}
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

export function Agent() {
  const { t } = useTranslation();
  const { data: teams = [] } = useTeams();
  const { data: agents = [], isLoading, error } = useAgents();
  const createAgent = useCreateAgent();
  const createTeam = useCreateTeam();
  const dispatchAgent = useDispatchAgentTask();
  const dispatchTeam = useDispatchTeamTask();

  const [showCreateForm, setShowCreateForm] = useState(false);
  const [showCreateTeamForm, setShowCreateTeamForm] = useState(false);
  const [teamForm, setTeamForm] = useState<CreateTeamRequest>({
    name: "",
    description: "",
  });
  const [createForm, setCreateForm] = useState<CreateAgentRequest>({
    kind: "specialist",
    team_id: "",
    name: "",
    description: "",
    instructions: "",
    model: "",
    required_llm_capability: 0,
    enable_rag: false,
    enable_memory: true,
    enable_ptc: true,
    enable_mcp: true,
    mcp_tools: [],
    skills: [],
  });
  const [executingTeam, setExecutingTeam] = useState<string | null>(null);
  const [dispatchError, setDispatchError] = useState<string | null>(null);

  // Separate built-in and custom agents
  const { builtinAgents, customAgents } = useMemo(() => {
    const builtin: AgentModel[] = [];
    const custom: AgentModel[] = [];
    const builtinNames = [
      "Concierge",
      "Assistant",
      "Operator",
      "Captain",
      "Stakeholder",
    ];

    agents.forEach((agent) => {
      if (
        builtinNames.some(
          (name) => agent.name.toLowerCase() === name.toLowerCase(),
        )
      ) {
        builtin.push(agent);
      } else {
        custom.push(agent);
      }
    });

    return { builtinAgents: builtin, customAgents: custom };
  }, [agents]);

  const handleCreateFormField =
    (field: "name" | "description" | "instructions" | "model") =>
    (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      setCreateForm((current) => ({ ...current, [field]: event.target.value }));
    };

  const handleCreateAgent = async (event: FormEvent) => {
    event.preventDefault();
    try {
      await createAgent.mutateAsync(createForm);
      setCreateForm({
        kind: "specialist",
        team_id: "",
        name: "",
        description: "",
        instructions: "",
        model: "",
        required_llm_capability: 0,
        enable_rag: false,
        enable_memory: true,
        enable_ptc: true,
        enable_mcp: true,
        mcp_tools: [],
        skills: [],
      });
      setShowCreateForm(false);
    } catch (mutationError) {
      console.error(mutationError);
    }
  };

  const handleCreateTeam = async (event: FormEvent) => {
    event.preventDefault();
    try {
      await createTeam.mutateAsync({
        name: teamForm.name.trim(),
        description: teamForm.description.trim(),
      });
      setTeamForm({ name: "", description: "" });
      setShowCreateTeamForm(false);
    } catch (mutationError) {
      console.error(mutationError);
    }
  };

  const handleAgentExecute = async (agentName: string, instruction: string) => {
    setDispatchError(null);
    try {
      const res = await dispatchAgent.mutateAsync({ name: agentName, instruction });
      return { response: res.response, durationMs: res.duration_ms };
    } catch (err) {
      setDispatchError(err instanceof Error ? err.message : "Execution failed");
      return undefined;
    }
  };

  const handleTeamExecute = async (teamId: string, message: string) => {
    setExecutingTeam(teamId);
    setDispatchError(null);
    try {
      const res = await dispatchTeam.mutateAsync({ teamId, message });
      return { taskId: res.task_id, ack: res.ack_message };
    } catch (err) {
      setDispatchError(err instanceof Error ? err.message : "Execution failed");
      return undefined;
    } finally {
      setExecutingTeam(null);
    }
  };

  return (
    <div className="space-y-8" data-testid="page-agent">
      {/* Create Buttons */}
      <section className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          onClick={() => setShowCreateTeamForm(!showCreateTeamForm)}
          data-testid="team-toggle-create"
        >
          {showCreateTeamForm ? t("close") : t("newTeam")}
        </Button>
        <Button
          variant="outline"
          onClick={() => setShowCreateForm(!showCreateForm)}
          data-testid="agent-toggle-create"
        >
          {showCreateForm ? t("close") : t("newAgent")}
        </Button>
      </section>

      {/* Create Team Form */}
      {showCreateTeamForm && (
        <form
          onSubmit={handleCreateTeam}
          className="space-y-3 rounded-lg border bg-card p-5 shadow-sm"
          data-testid="team-create-form"
        >
          <Input
            value={teamForm.name}
            onChange={(event) =>
              setTeamForm((current) => ({
                ...current,
                name: event.target.value,
              }))
            }
            placeholder={t("teamNamePlaceholder")}
            required
          />
          <Input
            value={teamForm.description}
            onChange={(event) =>
              setTeamForm((current) => ({
                ...current,
                description: event.target.value,
              }))
            }
            placeholder={t("teamDescriptionPlaceholder")}
            required
          />
          <Button
            type="submit"
            disabled={createTeam.isPending}
            className="w-full"
            data-testid="team-create-submit"
          >
            {createTeam.isPending ? t("creating") : t("createTeam")}
          </Button>
        </form>
      )}

      {/* Create Agent Form */}
      {showCreateForm && (
        <form
          onSubmit={handleCreateAgent}
          className="space-y-3 rounded-lg border bg-card p-5 shadow-sm"
          data-testid="agent-create-form"
        >
          <select
            value={createForm.team_id}
            onChange={(event) =>
              setCreateForm((current) => ({
                ...current,
                team_id: event.target.value,
              }))
            }
            className={selectClass}
          >
            <option value="">{t("defaultTeamOption")}</option>
            {teams.map((team) => (
              <option key={team.id} value={team.id}>
                {team.name}
              </option>
            ))}
          </select>
          <select
            value={createForm.kind}
            onChange={(event) =>
              setCreateForm((current) => ({
                ...current,
                kind: event.target.value as CreateAgentRequest["kind"],
              }))
            }
            className={selectClass}
          >
            <option value="agent">{t("kindAgent")}</option>
            <option value="specialist">{t("kindSpecialist")}</option>
            <option value="captain">{t("kindCaptain")}</option>
          </select>
          <Input
            value={createForm.name}
            onChange={handleCreateFormField("name")}
            placeholder={t("agentNamePlaceholder")}
            required
          />
          <Input
            value={createForm.description}
            onChange={handleCreateFormField("description")}
            placeholder={t("oneLineMission")}
            required
          />
          <Textarea
            value={createForm.instructions}
            onChange={handleCreateFormField("instructions")}
            placeholder={t("systemInstructions")}
            rows={4}
            className="resize-none"
            required
          />
          <fieldset className="rounded-lg border bg-muted/50 p-4">
            <legend className="px-1 text-xs font-semibold uppercase tracking-[0.22em] text-muted-foreground">
              {t("agentCapabilities")}
            </legend>
            <div className="mt-3 grid gap-3 md:grid-cols-2">
              <label className="flex items-start gap-3 rounded-md bg-card p-3 text-sm text-foreground">
                <input
                  type="checkbox"
                  checked={createForm.enable_ptc ?? true}
                  onChange={(event) =>
                    setCreateForm((current) => ({
                      ...current,
                      enable_ptc: event.target.checked,
                    }))
                  }
                  className="mt-1"
                />
                <span>
                  <span className="block font-medium text-foreground">
                    {t("capabilityPTC")}
                  </span>
                  <span className="mt-1 block text-xs text-muted-foreground">
                    {t("ptcDefaultOnHelp")}
                  </span>
                </span>
              </label>
              <label className="flex items-start gap-3 rounded-md bg-card p-3 text-sm text-foreground">
                <input
                  type="checkbox"
                  checked={createForm.enable_memory ?? true}
                  onChange={(event) =>
                    setCreateForm((current) => ({
                      ...current,
                      enable_memory: event.target.checked,
                    }))
                  }
                  className="mt-1"
                />
                <span>
                  <span className="block font-medium text-foreground">
                    {t("capabilityMemory")}
                  </span>
                  <span className="mt-1 block text-xs text-muted-foreground">
                    {t("memoryNoEmbeddingHelp")}
                  </span>
                </span>
              </label>
              <label className="flex items-start gap-3 rounded-md bg-card p-3 text-sm text-foreground">
                <input
                  type="checkbox"
                  checked={createForm.enable_mcp ?? true}
                  onChange={(event) =>
                    setCreateForm((current) => ({
                      ...current,
                      enable_mcp: event.target.checked,
                    }))
                  }
                  className="mt-1"
                />
                <span>
                  <span className="block font-medium text-foreground">
                    {t("capabilityMCP")}
                  </span>
                  <span className="mt-1 block text-xs text-muted-foreground">
                    {t("mcpCapabilityHelp")}
                  </span>
                </span>
              </label>
              <label className="flex items-start gap-3 rounded-md bg-card p-3 text-sm text-foreground">
                <input
                  type="checkbox"
                  checked={createForm.enable_rag ?? false}
                  onChange={(event) =>
                    setCreateForm((current) => ({
                      ...current,
                      enable_rag: event.target.checked,
                    }))
                  }
                  className="mt-1"
                />
                <span>
                  <span className="block font-medium text-foreground">
                    {t("capabilityRAG")}
                  </span>
                  <span className="mt-1 block text-xs text-muted-foreground">
                    {t("ragOptionalHelp")}
                  </span>
                </span>
              </label>
            </div>
          </fieldset>
          <Button
            type="submit"
            disabled={createAgent.isPending}
            className="w-full"
            data-testid="agent-create-submit"
          >
            {createAgent.isPending ? t("creating") : t("createSpecialist")}
          </Button>
        </form>
      )}

      {/* Error Display */}
      {dispatchError && (
        <div className="rounded-lg border border-rose-200 bg-rose-50 shadow-sm p-4 text-sm text-rose-700">
          {dispatchError}
        </div>
      )}

      {/* Loading/Error States */}
      {isLoading && (
        <div className="rounded-lg border bg-card shadow-sm p-5 text-sm text-muted-foreground">
          {t("loadingAgents")}
        </div>
      )}
      {error && (
        <div className="rounded-lg border border-rose-200 bg-rose-50 shadow-sm p-5 text-sm text-rose-700">
          {error.message}
        </div>
      )}

      {/* Teams Section */}
      <section>
        <div className="mb-4">
          <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">
            {t("teams")}
          </p>
          <h2 className="mt-2 text-2xl font-semibold text-foreground">
            {t("teams")}
          </h2>
        </div>

        {!isLoading && teams.length === 0 && (
          <div className="rounded-lg border border-dashed bg-muted/60 shadow-sm p-6 text-sm text-muted-foreground">
            {t("noTeamsRegistered") || t("noAgentsRegistered")}
          </div>
        )}

        <div className="grid gap-4 xl:grid-cols-2">
          {teams.map((team) => (
            <TeamCard
              key={team.id}
              team={team}
              onExecute={(message) => handleTeamExecute(team.id, message)}
              isExecuting={executingTeam === team.id}
            />
          ))}
        </div>
      </section>

      {/* Built-in Agents Section */}
      {builtinAgents.length > 0 && (
        <section>
          <div className="mb-4">
            <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">
              {t("builtinAgents") || "Built-in"}
            </p>
            <h2 className="mt-2 text-2xl font-semibold text-foreground">
              {t("builtinAgents") || "Built-in Agents"}
            </h2>
          </div>

          <div className="grid gap-4 xl:grid-cols-2">
            {builtinAgents.map((agent) => (
              <AgentCard
                key={agent.id}
                agent={agent}
                onExecute={(instruction) =>
                  handleAgentExecute(agent.name, instruction)
                }
                isExecuting={dispatchAgent.isPending}
              />
            ))}
          </div>
        </section>
      )}

      {/* Custom Agents Section */}
      <section>
        <div className="mb-4">
          <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">
            {t("customAgents") || "Custom"}
          </p>
          <h2 className="mt-2 text-2xl font-semibold text-foreground">
            {t("agents")}
          </h2>
        </div>

        {!isLoading && customAgents.length === 0 && (
          <div className="rounded-lg border border-dashed bg-muted/60 shadow-sm p-6 text-sm text-muted-foreground">
            {t("noAgentsRegistered")}
          </div>
        )}

        <div className="grid gap-4 xl:grid-cols-2">
          {customAgents.map((agent) => (
            <AgentCard
              key={agent.id}
              agent={agent}
              onExecute={(instruction) =>
                handleAgentExecute(agent.name, instruction)
              }
              isExecuting={dispatchAgent.isPending}
            />
          ))}
        </div>
      </section>
    </div>
  );
}
