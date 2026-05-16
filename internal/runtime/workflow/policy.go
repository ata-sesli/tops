package workflow

import "strings"

type ActionClass string

const (
	ActionClassReadOnly ActionClass = "read_only"
	ActionClassWrite    ActionClass = "write"
)

type ActionPermission string

const (
	ActionPermissionAllow    ActionPermission = "allow"
	ActionPermissionRequest  ActionPermission = "request"
	ActionPermissionDisallow ActionPermission = "disallow"
)

type ExecutionPolicy struct {
	ReadOnly ActionPermission
	Write    ActionPermission
}

func DefaultExecutionPolicy() ExecutionPolicy {
	return ExecutionPolicy{
		ReadOnly: ActionPermissionAllow,
		Write:    ActionPermissionRequest,
	}
}

func (p ExecutionPolicy) PermissionFor(class ActionClass) ActionPermission {
	switch class {
	case ActionClassReadOnly:
		return normalizePermission(p.ReadOnly, DefaultExecutionPolicy().ReadOnly)
	default:
		return normalizePermission(p.Write, DefaultExecutionPolicy().Write)
	}
}

func ClassifyActionClass(labels []string) ActionClass {
	if isReadOnlyOnly(labels) {
		return ActionClassReadOnly
	}
	return ActionClassWrite
}

func normalizePermission(value ActionPermission, fallback ActionPermission) ActionPermission {
	switch strings.ToLower(strings.TrimSpace(string(value))) {
	case string(ActionPermissionAllow):
		return ActionPermissionAllow
	case string(ActionPermissionRequest):
		return ActionPermissionRequest
	case string(ActionPermissionDisallow):
		return ActionPermissionDisallow
	default:
		return fallback
	}
}
