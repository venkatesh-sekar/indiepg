package pg

import (
	"context"
	"strconv"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/exec"
)

// clusterMainMajor resolves the major version of the managed "main" cluster from
// pg_lsclusters, which lists the cluster whether or not it is currently running
// (so it works between a stop and the following start of a restore). The indiepg
// box provisions exactly one main cluster; if several majors carry a main, an
// online one is preferred, otherwise the highest major.
func (m *Manager) clusterMainMajor(ctx context.Context) (int, error) {
	clusters, err := m.listClusters(ctx)
	if err != nil {
		return 0, err
	}
	best, online := 0, 0
	for _, c := range clusters {
		if c.Name != "main" {
			continue
		}
		if c.Major > best {
			best = c.Major
		}
		if c.Status == "online" && c.Major > online {
			online = c.Major
		}
	}
	if online > 0 {
		return online, nil
	}
	if best > 0 {
		return best, nil
	}
	return 0, core.InternalError("pg: no 'main' cluster found to control")
}

// StopCluster stops the managed main cluster with
// `pg_ctlcluster <major> main stop --force`. A destructive pgBackRest restore
// needs this: pgBackRest refuses to restore over a running cluster
// (ERROR [038]: unable to restore while PostgreSQL is running). It mirrors the
// stop pattern used by the major-version rollback path.
func (m *Manager) StopCluster(ctx context.Context) error {
	if m.runner == nil {
		return core.InternalError("pg: StopCluster requires a Runner")
	}
	major, err := m.clusterMainMajor(ctx)
	if err != nil {
		return err
	}
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "pg_ctlcluster",
		Args:    []string{strconv.Itoa(major), "main", "stop", "--force"},
		Timeout: commandTimeout,
	}); err != nil {
		return core.ExecError("pg: stopping cluster %d/main failed", major).Wrap(err)
	}
	return nil
}

// StartCluster starts the managed main cluster with
// `pg_ctlcluster <major> main start`. After a restore this is what makes
// PostgreSQL replay WAL up to the recovery target and promote.
func (m *Manager) StartCluster(ctx context.Context) error {
	if m.runner == nil {
		return core.InternalError("pg: StartCluster requires a Runner")
	}
	major, err := m.clusterMainMajor(ctx)
	if err != nil {
		return err
	}
	if _, err := m.runner.Run(ctx, exec.RunSpec{
		Name:    "pg_ctlcluster",
		Args:    []string{strconv.Itoa(major), "main", "start"},
		Timeout: commandTimeout,
	}); err != nil {
		return core.ExecError("pg: starting cluster %d/main failed", major).Wrap(err)
	}
	return nil
}
