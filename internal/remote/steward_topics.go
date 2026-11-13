package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"mcode/internal/remote/wire"
)

const stewardExtServiceName = "steward"
const stewardOpenMethod = "steward.open"
const stewardResetMethod = "steward.reset"

type stewardExtService interface {
	Open(ctx context.Context, projectID string) (string, error)
	Reset(ctx context.Context, projectID string) (string, error)
}

func init() {
	RegisterMethod(stewardOpenMethod, handleStewardOpen)
	RegisterMethod(stewardResetMethod, handleStewardReset)
}

func handleStewardOpen(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.StewardOpenParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if projectID == "" {
		return nil, ExtInvalidParams("Invalid params: 'projectID' is required")
	}
	svc, ok := backendService[stewardExtService](d.backend, stewardExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	threadID, err := svc.Open(ctx, projectID)
	if err != nil {
		var extErr *ExtError
		if errors.As(err, &extErr) {
			return nil, extErr
		}
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
	}
	return wire.StewardOpenResult{ThreadID: threadID}, nil
}

func handleStewardReset(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.StewardResetParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(p.ProjectID)
	if projectID == "" {
		return nil, ExtInvalidParams("Invalid params: 'projectID' is required")
	}
	svc, ok := backendService[stewardExtService](d.backend, stewardExtServiceName)
	if !ok {
		return nil, ExtNotImplemented()
	}
	threadID, err := svc.Reset(ctx, projectID)
	if err != nil {
		var extErr *ExtError
		if errors.As(err, &extErr) {
			return nil, extErr
		}
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
	}
	return wire.StewardResetResult{ThreadID: threadID}, nil
}
