package mcp

import (
	"context"
)

const designFinalizeToolName = "design_finalize"

func designFinalizeUsable(hooks *SwarmHooks, ctx context.Context) bool {
	threadID := scopeThreadID(ctx)
	return threadID != "" && hooks != nil && hooks.IsDesignBee != nil && hooks.FinalizeDesign != nil && hooks.IsDesignBee(threadID)
}

func (r *Registry) registerDesignTools(s *Server) {
	r.Register(ToolDefinition{
		Name: designFinalizeToolName,
		Meta: map[string]any{"anthropic/alwaysLoad": true},
		Description: "Finalize this design bee session when the user explicitly confirms the design (including phrases like \"go with B\" or \"looks good as-is\"). " +
			"If the user requested edits alongside finalizing, finish updating the design files before calling this tool. " +
			"After calling this tool, do not edit design files again; your reply for the current turn becomes the final report to the lead agent and must state the design file path(s), key decisions confirmed by the user across rounds, and important implementation notes.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		IsWrite: true,
		Handler: handleDesignFinalize,
	})
}

func handleDesignFinalize(tc ToolContext, _ map[string]any) ToolResult {
	hooks := tc.Server.swarmHooks()
	if !designFinalizeUsable(hooks, tc.Context) {
		return toolError(CodeNotFound, "Unknown tool: "+designFinalizeToolName, false)
	}
	if err := hooks.FinalizeDesign(tc.Context, scopeThreadID(tc.Context)); err != nil {
		return toolError(CodeInternal, err.Error(), false)
	}
	return toolSuccess("Design finalized. Output your final report in this turn without editing design files.")
}
