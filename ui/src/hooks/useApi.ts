import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, QueryRequest, ChatRequest, CreateSkillRequest, AddMCPServerRequest, CallToolRequest, AddMemoryRequest, UpdateConfigRequest, CreateAgentRequest, CreateSquadRequest, ApplySetupRequest, DispatchSquadTaskRequest } from '../lib/api'

// RAG Hooks
export function useQueryRAG() {
  return useMutation({
    mutationFn: (data: QueryRequest) => api.query(data),
  })
}

export function useDocuments() {
  return useQuery({
    queryKey: ['documents'],
    queryFn: api.getDocuments,
  })
}

export function useDocument(id: string) {
  return useQuery({
    queryKey: ['documents', id],
    queryFn: () => api.getDocument(id),
    enabled: !!id,
  })
}

export function useDeleteDocument() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.deleteDocument(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['documents'] })
      queryClient.invalidateQueries({ queryKey: ['collections'] })
    },
  })
}

export function useCollections() {
  return useQuery({
    queryKey: ['collections'],
    queryFn: api.getCollections,
  })
}

export function useStatus() {
  return useQuery({
    queryKey: ['status'],
    queryFn: api.getStatus,
    refetchInterval: 30000,
  })
}

export function useChat() {
  return useMutation({
    mutationFn: (data: ChatRequest) => api.chat(data),
  })
}

export function useChatSessions(limit?: number, type?: string) {
  return useQuery({
    queryKey: ['chat-sessions', limit, type],
    queryFn: () => api.getChatSessions(limit, type),
    refetchInterval: 30000,
  })
}

export function useChatSession(sessionId: string | undefined) {
  return useQuery({
    queryKey: ['chat-session', sessionId],
    queryFn: () => api.getChatSession(sessionId!),
    enabled: !!sessionId,
  })
}

export function useAgents() {
  return useQuery({
    queryKey: ['agents'],
    queryFn: api.getAgents,
    select: (data) => data.agents,
    refetchInterval: 15000,
  })
}

export function useSquads() {
  return useQuery({
    queryKey: ['squads'],
    queryFn: api.getSquads,
    select: (data) => data.squads,
    refetchInterval: 15000,
  })
}

export function useAgent(name: string) {
  return useQuery({
    queryKey: ['agents', name],
    queryFn: () => api.getAgent(name),
    enabled: !!name,
  })
}

export function useCreateAgent() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (data: CreateAgentRequest) => api.createAgent(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['agents'] })
      queryClient.invalidateQueries({ queryKey: ['squads'] })
      queryClient.invalidateQueries({ queryKey: ['status'] })
      queryClient.invalidateQueries({ queryKey: ['ops', 'logs'] })
    },
  })
}

export function useCreateSquad() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (data: CreateSquadRequest) => api.createSquad(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['squads'] })
      queryClient.invalidateQueries({ queryKey: ['agents'] })
      queryClient.invalidateQueries({ queryKey: ['ops', 'logs'] })
    },
  })
}

export function useDispatchAgentTask() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ name, instruction }: { name: string; instruction: string }) =>
      api.dispatchAgentTask(name, { instruction }),
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ['agents'] })
      queryClient.invalidateQueries({ queryKey: ['agents', variables.name] })
      queryClient.invalidateQueries({ queryKey: ['ops', 'logs'] })
    },
  })
}

export function useDispatchSquadTask() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ squadId, message }: { squadId: string; message: string }) =>
      api.dispatchSquadTask(squadId, { message }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ops', 'logs'] })
    },
  })
}

export function useOpsLogs(limit = 20) {
  return useQuery({
    queryKey: ['ops', 'logs', limit],
    queryFn: () => api.getOpsLogs(limit),
    select: (data) => data.logs,
    refetchInterval: 5000,
  })
}

// Skills Hooks
export function useSkills() {
  return useQuery({
    queryKey: ['skills'],
    queryFn: api.getSkills,
  })
}

export function useSkill(id: string) {
  return useQuery({
    queryKey: ['skills', id],
    queryFn: () => api.getSkill(id),
    enabled: !!id,
  })
}

export function useCreateSkill() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (data: CreateSkillRequest) => api.createSkill(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['skills'] })
    },
  })
}

export function useDeleteSkill() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.deleteSkill(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['skills'] })
    },
  })
}

// MCP Hooks
export function useMCPServers() {
  return useQuery({
    queryKey: ['mcp', 'servers'],
    queryFn: api.getMCPServers,
  })
}

export function useMCPTools() {
  return useQuery({
    queryKey: ['mcp', 'tools'],
    queryFn: api.getMCPTools,
  })
}

export function useAddMCPServer() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (data: AddMCPServerRequest) => api.addMCPServer(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['mcp', 'servers'] })
      queryClient.invalidateQueries({ queryKey: ['mcp', 'tools'] })
    },
  })
}

export function useCallMCPTool() {
  return useMutation({
    mutationFn: (data: CallToolRequest) => api.callTool(data),
  })
}

// Memory Hooks
export function useMemories() {
  return useQuery({
    queryKey: ['memories'],
    queryFn: api.getMemories,
  })
}

export function useMemory(id: string) {
  return useQuery({
    queryKey: ['memory', id],
    queryFn: () => api.getMemory(id),
    enabled: !!id,
  })
}

export function useSearchMemories(query: string) {
  return useQuery({
    queryKey: ['memories', 'search', query],
    queryFn: () => api.searchMemories(query),
    enabled: !!query,
  })
}

export function useAddMemory() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (data: AddMemoryRequest) => api.addMemory(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['memories'] })
    },
  })
}

export function useDeleteMemory() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.deleteMemory(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['memories'] })
    },
  })
}

export function useConfig() {
  return useQuery({
    queryKey: ['config'],
    queryFn: api.getConfig,
  })
}

export function useUpdateConfig() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (data: UpdateConfigRequest) => api.updateConfig(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['config'] })
    },
  })
}

export function useSetup() {
  return useQuery({
    queryKey: ['setup'],
    queryFn: api.getSetup,
  })
}

export function useApplySetup() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (data: ApplySetupRequest) => api.applySetup(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['setup'] })
      queryClient.invalidateQueries({ queryKey: ['config'] })
      queryClient.invalidateQueries({ queryKey: ['status'] })
    },
  })
}
