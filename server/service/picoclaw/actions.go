package picoclaw

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultClickHold  = 40 * time.Millisecond
	defaultKeyDelay   = 30 * time.Millisecond
	defaultDragSteps  = 10
	defaultScrollStep = 20 * time.Millisecond
)

func (s *Service) Actions(c *gin.Context) {
	sessionID, sessionErr := s.requireSessionID(c)
	if sessionErr != nil {
		writePicoclawError(c, sessionErr)
		return
	}

	releaseAfter, lockErr := s.lock.AcquireTemporary(sessionID)
	if lockErr != nil {
		writePicoclawError(c, lockErr)
		return
	}
	if releaseAfter {
		defer s.lock.Release(sessionID)
	}

	actions, actionErr := normalizeActions(c)
	if actionErr != nil {
		writePicoclawError(c, actionErr)
		return
	}

	result, execErr := s.executeActions(sessionID, actions)
	if execErr != nil {
		writePicoclawError(c, execErr)
		return
	}

	writeSuccess(c, result)
}

func normalizeActions(c *gin.Context) ([]Action, *PicoclawError) {
	body := bytes.NewBuffer(nil)
	if _, err := body.ReadFrom(c.Request.Body); err != nil {
		return nil, newPicoclawError(CodeInvalidAction, "failed to read action payload")
	}

	raw := body.Bytes()
	if len(raw) == 0 {
		return nil, newPicoclawError(CodeInvalidAction, "empty action payload")
	}

	var batch ActionBatch
	if err := json.Unmarshal(raw, &batch); err == nil && len(batch.Actions) > 0 {
		return batch.Actions, nil
	}

	var action Action
	if err := json.Unmarshal(raw, &action); err != nil || action.Action == "" {
		return nil, newPicoclawError(CodeInvalidAction, "invalid action payload")
	}

	return []Action{action}, nil
}

func (s *Service) executeActions(sessionID string, actions []Action) (result ActionResult, err *PicoclawError) {
	if len(actions) == 0 {
		return ActionResult{}, newPicoclawError(CodeInvalidAction, "empty actions")
	}

	return ActionResult{}, newPicoclawError(CodeInvalidAction, "HID not available in serial-only mode")
}

func (s *Service) executeAction(action Action) (int, *PicoclawError) {
	return 0, newPicoclawError(CodeInvalidAction, "HID not available in serial-only mode")
}

func normalizedPoint(x *float64, y *float64) (float64, float64, *PicoclawError) {
	if x == nil || y == nil {
		return 0, 0, newPicoclawError(CodeInvalidAction, "action requires x and y")
	}
	if *x < 0 || *x > 1 || *y < 0 || *y > 1 {
		return 0, 0, newPicoclawError(CodeInvalidAction, "coordinates must be within [0,1]")
	}
	return *x, *y, nil
}

func normalizedNestedPoint(point *Point) (float64, float64, *PicoclawError) {
	if point == nil {
		return 0, 0, newPicoclawError(CodeInvalidAction, "action requires point coordinates")
	}
	return normalizedPoint(point.X, point.Y)
}

func mouseButton(button string) (byte, *PicoclawError) {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "", "left":
		return 1 << 0, nil
	case "right":
		return 1 << 1, nil
	case "middle":
		return 1 << 2, nil
	case "back":
		return 1 << 3, nil
	case "forward":
		return 1 << 4, nil
	default:
		return 0, newPicoclawError(CodeInvalidAction, "invalid mouse button")
	}
}
