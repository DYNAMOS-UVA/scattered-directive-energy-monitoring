package repository

import (
	"github.com/Jorrit05/DYNAMOS/pkg/api"
)

// AgreementRepository defines the interface for retrieving agreements.
// This abstraction allows swapping between different backends (etcd, EFLINT reasoner, etc.)
type AgreementRepository interface {
	// GetAgreement retrieves an agreement for a specific data steward.
	// Returns the agreement and a boolean indicating if the agreement was found.
	GetAgreement(steward string) (*api.Agreement, bool, error)

	// SaveAgreement saves an agreement for a specific data steward.
	SaveAgreement(steward string, agreement *api.Agreement) error

	// DeleteAgreement removes the legacy JSON agreement for a specific data
	// steward. Used when reconciling a format switch (eFLINT replaces legacy).
	// Deleting a non-existent key is not an error.
	DeleteAgreement(steward string) error
}

// UserRelation represents the relationship between a user and a data provider.
type UserRelation struct {
	UserName                string
	AllowedArchetypes       []string
	AllowedComputeProviders []string
}

// ValidatedAgreement represents an agreement that has been validated for a specific user.
type ValidatedAgreement struct {
	Steward           string
	Agreement         *api.Agreement
	UserRelation      *api.Relation
	MatchedArchetypes []string
	MatchedCompute    []string
}
