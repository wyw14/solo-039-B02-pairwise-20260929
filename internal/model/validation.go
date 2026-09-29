package model

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

func ValidateGrade(grade string) error {
	grade = strings.TrimSpace(grade)
	if grade == "" {
		return errors.New("grade is required")
	}
	if len(grade) > 32 {
		return errors.New("grade is too long")
	}
	return nil
}

func ValidateSource(source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return errors.New("feed source is required")
	}
	if len(source) > 64 {
		return errors.New("feed source is too long")
	}
	return nil
}

func ValidatePlan(plan ProductPlan) error {
	if plan.Campaign == uuid.Nil || plan.Revision == uuid.Nil {
		return errors.New("plan identity is required")
	}
	if plan.Target == "" {
		return errors.New("target is required")
	}
	return nil
}

func PhaseAllowsHeat(p Phase) bool {
	return p == Warming || p == Stabilizing || p == OnSpec || p == Transitioning
}
