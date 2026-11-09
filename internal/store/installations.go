package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Installation is one installation of the GitHub App as the deployment keeps
// it: ids and names, never a token.
type Installation struct {
	ID        int64
	Account   string
	Suspended bool
	// Repositories are the repositories the installation reaches, by id.
	Repositories []Repository
}

// Repository is one repository an installation reaches.
type Repository struct {
	ID       int64
	FullName string
}

const keepInstallation = `
INSERT INTO app_installations (installation_id, account) VALUES ($1, $2)
ON CONFLICT (installation_id) DO UPDATE SET account = excluded.account`

const addRepositories = `
INSERT INTO app_installation_repositories (installation_id, repository_id, full_name)
SELECT $1, id, name FROM unnest($2::bigint[], $3::text[]) AS t(id, name)
ON CONFLICT (installation_id, repository_id) DO UPDATE SET full_name = excluded.full_name`

// AddRepositories keeps an installation and the repositories it now reaches.
// An installation event states its account each time, so an installation the
// table does not hold yet is added, and a renamed account or repository takes
// its new name. It leaves the installation's suspension as it is. The App's
// installations belong to the deployment, so no tenant is set.
func (s *Store) AddRepositories(ctx context.Context, installation int64, account string, repositories []Repository) error {
	ids, names := make([]int64, len(repositories)), make([]string, len(repositories))
	for i, repository := range repositories {
		ids[i], names[i] = repository.ID, repository.FullName
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, keepInstallation, installation, account); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, addRepositories, installation, ids, names)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: keep installation %d: %w", installation, err)
	}
	return nil
}

// RemoveRepositories forgets repositories an installation no longer reaches.
func (s *Store) RemoveRepositories(ctx context.Context, installation int64, repositories []int64) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM app_installation_repositories
		WHERE installation_id = $1 AND repository_id = ANY($2::bigint[])`, installation, repositories)
	if err != nil {
		return fmt.Errorf("store: remove repositories of installation %d: %w", installation, err)
	}
	return nil
}

// SuspendInstallation keeps whether an installation is suspended, and keeps
// the installation when the table does not hold it yet.
func (s *Store) SuspendInstallation(ctx context.Context, installation int64, account string, suspended bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app_installations (installation_id, account, suspended) VALUES ($1, $2, $3)
		ON CONFLICT (installation_id) DO UPDATE SET account = excluded.account, suspended = excluded.suspended`,
		installation, account, suspended)
	if err != nil {
		return fmt.Errorf("store: suspend installation %d: %w", installation, err)
	}
	return nil
}

// DeleteInstallation forgets an installation and its repositories.
func (s *Store) DeleteInstallation(ctx context.Context, installation int64) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM app_installations WHERE installation_id = $1`, installation); err != nil {
		return fmt.Errorf("store: delete installation %d: %w", installation, err)
	}
	return nil
}

// Installation reads one installation and its repositories, ordered by id. An
// installation the table does not hold is ErrNotFound.
func (s *Store) Installation(ctx context.Context, installation int64) (Installation, error) {
	kept := Installation{ID: installation}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT account, suspended FROM app_installations WHERE installation_id = $1`, installation).
			Scan(&kept.Account, &kept.Suspended)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT repository_id, full_name FROM app_installation_repositories
			WHERE installation_id = $1 ORDER BY repository_id`, installation)
		if err != nil {
			return err
		}
		kept.Repositories, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Repository])
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return Installation{}, ErrNotFound
	}
	if err != nil {
		return Installation{}, fmt.Errorf("store: read installation %d: %w", installation, err)
	}
	return kept, nil
}
