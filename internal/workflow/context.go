package workflow

import "context"

type approvalContextKey struct{}
type auditContextKey struct{}
type executionPolicyContextKey struct{}

type AuditContext struct {
	Store         AuditStore
	ChatSessionID *int64
}

func WithApprovalPrompter(ctx context.Context, prompter ApprovalPrompter) context.Context {
	return context.WithValue(ctx, approvalContextKey{}, prompter)
}

func ApprovalPrompterFromContext(ctx context.Context) ApprovalPrompter {
	if ctx == nil {
		return nil
	}
	prompter, _ := ctx.Value(approvalContextKey{}).(ApprovalPrompter)
	return prompter
}

func WithAuditStore(ctx context.Context, store AuditStore, chatSessionID *int64) context.Context {
	return context.WithValue(ctx, auditContextKey{}, AuditContext{
		Store:         store,
		ChatSessionID: chatSessionID,
	})
}

func AuditFromContext(ctx context.Context) AuditContext {
	if ctx == nil {
		return AuditContext{}
	}
	value, _ := ctx.Value(auditContextKey{}).(AuditContext)
	return value
}

func WithExecutionPolicy(ctx context.Context, policy ExecutionPolicy) context.Context {
	return context.WithValue(ctx, executionPolicyContextKey{}, policy)
}

func ExecutionPolicyFromContext(ctx context.Context) ExecutionPolicy {
	if ctx == nil {
		return DefaultExecutionPolicy()
	}
	value, ok := ctx.Value(executionPolicyContextKey{}).(ExecutionPolicy)
	if !ok {
		return DefaultExecutionPolicy()
	}
	return ExecutionPolicy{
		ReadOnly: normalizePermission(value.ReadOnly, DefaultExecutionPolicy().ReadOnly),
		Write:    normalizePermission(value.Write, DefaultExecutionPolicy().Write),
	}
}
