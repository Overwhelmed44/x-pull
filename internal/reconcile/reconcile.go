// Package reconcile converges live Xray users towards an authoritative snapshot.
package reconcile

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/x-pull/x-pull/internal/user"
)

type Backend interface {
	List(context.Context, user.Target) ([]user.User, error)
	Add(context.Context, user.Target, user.User) error
	Remove(context.Context, user.Target, string) error
}

type Result struct{ Added, Updated, Removed int }

type Reconciler struct{ backend Backend }

func New(backend Backend) *Reconciler { return &Reconciler{backend: backend} }

func (r *Reconciler) Sync(ctx context.Context, target user.Target, snapshot []user.User, allowEmpty bool) (Result, error) {
	var result Result
	desired, err := user.Prepare(target, snapshot)
	if err != nil {
		return result, err
	}
	if len(desired) == 0 && !allowEmpty {
		return result, fmt.Errorf("empty snapshot refused; set allow_empty to permit revoking all managed users")
	}
	live, err := r.backend.List(ctx, target)
	if err != nil {
		return result, fmt.Errorf("list live users: %w", err)
	}
	current, wanted := map[string]user.User{}, map[string]user.User{}
	originalEmails := map[string]bool{}
	owners := map[string]string{}
	for _, u := range live {
		credential := u.Credential(target.Protocol)
		if _, exists := owners[credential]; exists {
			return result, fmt.Errorf("duplicate live credential")
		}
		owners[credential] = u.Email
		if !strings.HasPrefix(u.Email, target.EmailPrefix) {
			continue
		}
		if _, exists := current[u.Email]; exists {
			return result, fmt.Errorf("duplicate live user email")
		}
		current[u.Email] = u
		originalEmails[u.Email] = true
	}
	for _, u := range desired {
		if owner, exists := owners[u.Credential(target.Protocol)]; exists && !strings.HasPrefix(owner, target.EmailPrefix) {
			return result, fmt.Errorf("desired credential conflicts with an unmanaged user")
		}
		wanted[u.Email] = u
	}
	sort.Slice(desired, func(i, j int) bool { return desired[i].Email < desired[j].Email })
	// Credential transfers (including renames) require releasing their old owner
	// first. A failed add is rolled back, and unrelated stale users are retained.
	for _, u := range desired {
		old, exists := current[u.Email]
		if exists && old == u {
			continue
		}
		var displaced []user.User
		replacements := []string{}
		if exists {
			replacements = append(replacements, u.Email)
		}
		if owner, taken := owners[u.Credential(target.Protocol)]; taken && owner != u.Email {
			replacements = append(replacements, owner)
		}
		sort.Strings(replacements)
		for _, email := range replacements {
			candidate := current[email]
			if err := r.backend.Remove(ctx, target, email); err != nil {
				rollbackErr := r.restore(ctx, target, displaced)
				return result, mutationError("remove user for replacement", err, rollbackErr)
			}
			displaced = append(displaced, candidate)
			delete(current, email)
			delete(owners, candidate.Credential(target.Protocol))
		}
		if err := r.backend.Add(ctx, target, u); err != nil {
			return result, mutationError("add desired user", err, r.restore(ctx, target, displaced))
		}
		current[u.Email] = u
		owners[u.Credential(target.Protocol)] = u.Email
		if originalEmails[u.Email] {
			result.Updated++
		} else {
			result.Added++
		}
		for _, d := range displaced {
			if _, keep := wanted[d.Email]; !keep {
				result.Removed++
			}
		}
	}
	for _, email := range sortedKeys(current) {
		if _, keep := wanted[email]; keep {
			continue
		}
		if err := r.backend.Remove(ctx, target, email); err != nil {
			return result, fmt.Errorf("remove stale user: %w", err)
		}
		result.Removed++
	}
	return result, nil
}

func (r *Reconciler) restore(ctx context.Context, target user.Target, users []user.User) error {
	var failed bool
	for _, u := range users {
		if r.backend.Add(ctx, target, u) != nil {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("rollback incomplete; next successful sync will retry")
	}
	return nil
}

func mutationError(operation string, err, rollbackErr error) error {
	if rollbackErr != nil {
		return fmt.Errorf("%s: %w; %v", operation, err, rollbackErr)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func sortedKeys(m map[string]user.User) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
