import { ChangeEvent, FormEvent, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  useCreateAgent,
  useCreateTeam,
  useAgents,
  useTeams,
  useDispatchAgentTask,
  useDispatchTeamTask,
} from "../hooks/useApi";
import type {
  AgentModel,
  CreateAgentRequest,
  CreateTeamRequest,
  Team,
} from "../lib/api";

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

function AgentCard({
  agent,
  onExecute,
  isExecuting,
}: {
  agent: AgentModel;
  onExecute: (instruction: string) => void;
  isExecuting: boolean;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [instruction, setInstruction] = useState("");

  const handleExecute = (e: FormEvent) => {
    e.preventDefault();
    if (instruction.trim()) {
      onExecute(instruction.trim());
      setInstruction("");
    }
  };

  return (
    <article className="rounded-[24px] border border-sky-100 bg-white">
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        className="flex w-full items-center justify-between gap-4 px-5 py-4 text-left"
        data-testid={`agent-row-${agent.name}`}
      >
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-lg font-medium text-slate-900">
              {agent.name}
            </span>
            <span className="rounded-full bg-sky-100 px-2.5 py-1 text-xs text-sky-800">
              {kindLabel(agent.kind, t)}
            </span>
          </div>
          <p className="mt-1 text-sm text-slate-600">{agent.description}</p>
        </div>
        <span className="text-sm text-slate-400">{expanded ? "−" : "+"}</span>
      </button>

      {expanded && (
        <div className="border-t border-sky-100 px-5 py-4">
          <p className="text-sm leading-7 text-slate-600">
            {agent.instructions}
          </p>

          <form onSubmit={handleExecute} className="mt-4 flex gap-2">
            <input
              type="text"
              value={instruction}
              onChange={(e) => setInstruction(e.target.value)}
              placeholder={
                t("instructionPlaceholder") || "Enter instruction..."
              }
              className="dashboard-input flex-1"
              disabled={isExecuting}
            />
            <button
              type="submit"
              disabled={isExecuting || !instruction.trim()}
              className="dashboard-button px-4 py-2"
            >
              {isExecuting ? t("running") : t("run")}
            </button>
          </form>
        </div>
      )}
    </article>
  );
}

function TeamCard({
  team,
  onExecute,
  isExecuting,
}: {
  team: Team;
  onExecute: (message: string) => void;
  isExecuting: boolean;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [message, setMessage] = useState("");

  const handleExecute = (e: FormEvent) => {
    e.preventDefault();
    if (message.trim()) {
      onExecute(message.trim());
      setMessage("");
    }
  };

  return (
    <article className="rounded-[24px] border border-sky-100 bg-white">
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        className="flex w-full items-center justify-between gap-4 px-5 py-4 text-left"
        data-testid={`team-row-${team.id}`}
      >
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-lg font-medium text-slate-900">
              {team.name}
            </span>
            <span className="rounded-full bg-emerald-100 px-2.5 py-1 text-xs text-emerald-800">
              {t("teams")}
            </span>
          </div>
          <p className="mt-1 text-sm text-slate-600">{team.description}</p>
        </div>
        <span className="text-sm text-slate-400">{expanded ? "−" : "+"}</span>
      </button>

      {expanded && (
        <div className="border-t border-sky-100 px-5 py-4">
          <div className="mb-3 flex flex-wrap gap-2">
            <span className="text-xs text-slate-500">
              {t("captainLabel")}:{" "}
              {team.lead_agent?.name ?? team.captain?.name ?? t("unknown")}
            </span>
            <span className="text-xs text-slate-500">
              {t("members")}: {team.members.length}
            </span>
            <span className="text-xs text-slate-500">
              {t("created")}: {formatDate(team.created_at, t)}
            </span>
          </div>

          {team.members.length > 0 && (
            <div className="mb-4">
              <p className="mb-2 text-xs uppercase tracking-[0.24em] text-slate-500">
                {t("members")}
              </p>
              <div className="flex flex-wrap gap-2">
                {team.members.map((member) => (
                  <span
                    key={member.id}
                    className="rounded-full bg-slate-100 px-2.5 py-1 text-xs text-slate-700"
                  >
                    {member.name}
                  </span>
                ))}
              </div>
            </div>
          )}

          <form onSubmit={handleExecute} className="flex gap-2">
            <input
              type="text"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              placeholder={t("messagePlaceholder") || "Enter message..."}
              className="dashboard-input flex-1"
              disabled={isExecuting}
            />
            <button
              type="submit"
              disabled={isExecuting || !message.trim()}
              className="dashboard-button px-4 py-2"
            >
              {isExecuting ? t("running") : t("run")}
            </button>
          </form>
        </div>
      )}
    </article>
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
    enable_memory: false,
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
        enable_memory: false,
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
      await dispatchAgent.mutateAsync({ name: agentName, instruction });
    } catch (err) {
      setDispatchError(err instanceof Error ? err.message : "Execution failed");
    }
  };

  const handleTeamExecute = async (teamId: string, message: string) => {
    setExecutingTeam(teamId);
    setDispatchError(null);
    try {
      await dispatchTeam.mutateAsync({ teamId, message });
    } catch (err) {
      setDispatchError(err instanceof Error ? err.message : "Execution failed");
    } finally {
      setExecutingTeam(null);
    }
  };

  return (
    <div className="space-y-8" data-testid="page-agent">
      {/* Create Buttons */}
      <section className="flex flex-wrap gap-2">
        <button
          type="button"
          onClick={() => setShowCreateTeamForm(!showCreateTeamForm)}
          className="dashboard-secondary-button px-4 py-2 text-sm"
          data-testid="team-toggle-create"
        >
          {showCreateTeamForm ? t("close") : t("newTeam")}
        </button>
        <button
          type="button"
          onClick={() => setShowCreateForm(!showCreateForm)}
          className="dashboard-secondary-button px-4 py-2 text-sm"
          data-testid="agent-toggle-create"
        >
          {showCreateForm ? t("close") : t("newAgent")}
        </button>
      </section>

      {/* Create Team Form */}
      {showCreateTeamForm && (
        <form
          onSubmit={handleCreateTeam}
          className="glass-panel space-y-3 rounded-[28px] p-5"
          data-testid="team-create-form"
        >
          <input
            value={teamForm.name}
            onChange={(event) =>
              setTeamForm((current) => ({
                ...current,
                name: event.target.value,
              }))
            }
            placeholder={t("teamNamePlaceholder")}
            className="dashboard-input"
            required
          />
          <input
            value={teamForm.description}
            onChange={(event) =>
              setTeamForm((current) => ({
                ...current,
                description: event.target.value,
              }))
            }
            placeholder={t("teamDescriptionPlaceholder")}
            className="dashboard-input"
            required
          />
          <button
            type="submit"
            disabled={createTeam.isPending}
            className="dashboard-button w-full justify-center"
            data-testid="team-create-submit"
          >
            {createTeam.isPending ? t("creating") : t("createTeam")}
          </button>
        </form>
      )}

      {/* Create Agent Form */}
      {showCreateForm && (
        <form
          onSubmit={handleCreateAgent}
          className="glass-panel space-y-3 rounded-[28px] p-5"
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
            className="dashboard-input"
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
            className="dashboard-input"
          >
            <option value="agent">{t("kindAgent")}</option>
            <option value="specialist">{t("kindSpecialist")}</option>
            <option value="captain">{t("kindCaptain")}</option>
          </select>
          <input
            value={createForm.name}
            onChange={handleCreateFormField("name")}
            placeholder={t("agentNamePlaceholder")}
            className="dashboard-input"
            required
          />
          <input
            value={createForm.description}
            onChange={handleCreateFormField("description")}
            placeholder={t("oneLineMission")}
            className="dashboard-input"
            required
          />
          <textarea
            value={createForm.instructions}
            onChange={handleCreateFormField("instructions")}
            placeholder={t("systemInstructions")}
            rows={4}
            className="dashboard-input resize-none"
            required
          />
          <button
            type="submit"
            disabled={createAgent.isPending}
            className="dashboard-button w-full justify-center"
            data-testid="agent-create-submit"
          >
            {createAgent.isPending ? t("creating") : t("createSpecialist")}
          </button>
        </form>
      )}

      {/* Error Display */}
      {dispatchError && (
        <div className="glass-panel rounded-[28px] border border-rose-200 bg-rose-50 p-4 text-sm text-rose-700">
          {dispatchError}
        </div>
      )}

      {/* Loading/Error States */}
      {isLoading && (
        <div className="glass-panel rounded-[28px] p-5 text-sm text-slate-500">
          {t("loadingAgents")}
        </div>
      )}
      {error && (
        <div className="glass-panel rounded-[28px] border border-rose-200 bg-rose-50 p-5 text-sm text-rose-700">
          {error.message}
        </div>
      )}

      {/* Teams Section */}
      <section>
        <div className="mb-4">
          <p className="text-xs uppercase tracking-[0.28em] text-slate-500">
            {t("teams")}
          </p>
          <h2 className="mt-2 text-2xl font-semibold text-slate-900">
            {t("teams")}
          </h2>
        </div>

        {!isLoading && teams.length === 0 && (
          <div className="glass-panel rounded-[28px] border border-dashed border-sky-100 bg-sky-50/60 p-6 text-sm text-slate-500">
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
            <p className="text-xs uppercase tracking-[0.28em] text-slate-500">
              {t("builtinAgents") || "Built-in"}
            </p>
            <h2 className="mt-2 text-2xl font-semibold text-slate-900">
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
          <p className="text-xs uppercase tracking-[0.28em] text-slate-500">
            {t("customAgents") || "Custom"}
          </p>
          <h2 className="mt-2 text-2xl font-semibold text-slate-900">
            {t("agents")}
          </h2>
        </div>

        {!isLoading && customAgents.length === 0 && (
          <div className="glass-panel rounded-[28px] border border-dashed border-sky-100 bg-sky-50/60 p-6 text-sm text-slate-500">
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
