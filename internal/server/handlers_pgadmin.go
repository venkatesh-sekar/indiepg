package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/venkatesh-sekar/indiepg/internal/auth"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/pg/admin"
)

// credentialResult is the response for guided actions that mint a secret (a new
// login user, a one-click app, a password rotation). The plaintext secret is
// returned exactly once, here, and never persisted by the panel — the operator
// must copy it now. Secrets is omitted when an action produces no new secret.
type credentialResult struct {
	Result  core.Result       `json:"result"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

// --- query box ---

// queryRequest is the body for POST /api/query: a single read statement.
type queryRequest struct {
	SQL string `json:"sql"`
}

// queryColumn mirrors one result column for the SPA grid.
type queryColumn struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
}

// queryResult is the full outcome of a query-box execution. ExecutedSQL is the
// statement actually run (the guard may have injected a LIMIT); Limited reports
// whether a LIMIT is in effect so the UI can warn the rows may be truncated.
type queryResult struct {
	Columns        []queryColumn `json:"columns"`
	Rows           [][]any       `json:"rows"`
	RowCount       int           `json:"row_count"`
	Limited        bool          `json:"limited"`
	ExecutedSQL    string        `json:"executed_sql"`
	DurationMS     int64         `json:"duration_ms"`
	Classification string        `json:"classification"`
}

// handleQuery runs a single read statement from the query box. The guard
// classifies and (for unbounded reads) auto-LIMITs the SQL; a non-read or
// multi-statement input is rejected with a typed safety error before any
// execution. This is read-only and is not audited.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req queryRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}
	if req.SQL == "" {
		writeError(w, core.ValidationError("sql is required"))
		return
	}

	rewritten, cls, err := s.guard.Check(req.SQL)
	if err != nil {
		writeError(w, err)
		return
	}

	t0 := time.Now()
	rows, err := s.pg.ExecuteRead(ctx, rewritten)
	if err != nil {
		writeError(w, err)
		return
	}
	durationMS := time.Since(t0).Milliseconds()

	cols := make([]queryColumn, len(rows.Columns))
	for i, c := range rows.Columns {
		cols[i] = queryColumn{Name: c.Name, DataType: c.DataType}
	}
	outRows := rows.Rows
	if outRows == nil {
		outRows = [][]any{}
	}

	writeData(w, http.StatusOK, queryResult{
		Columns:        cols,
		Rows:           outRows,
		RowCount:       rows.RowCount,
		Limited:        cls.HasLimit,
		ExecutedSQL:    rewritten,
		DurationMS:     durationMS,
		Classification: cls.Class.String(),
	})
}

// --- databases ---

// databaseResponse mirrors one database for the list view.
type databaseResponse struct {
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	SizeBytes int64  `json:"size_bytes"`
}

// handleListDatabases returns the cluster's databases. Read-only; not audited.
func (s *Server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	dbs, err := s.pg.ListDatabases(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]databaseResponse, 0, len(dbs))
	for _, d := range dbs {
		out = append(out, databaseResponse{Name: d.Name, Owner: d.Owner, SizeBytes: d.SizeBytes})
	}
	writeData(w, http.StatusOK, out)
}

// createDatabaseRequest is the body for POST /api/databases.
type createDatabaseRequest struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// handleCreateDatabase creates a database (optionally owned by an existing role).
func (s *Server) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req createDatabaseRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	res, err := s.pg.AdminCreateDatabase(ctx, req.Name, req.Owner)
	if err != nil {
		s.audit(ctx, "create_database", req.Name, "failure", "create database failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "create_database", req.Name, "success", "database created", "")
	writeData(w, http.StatusOK, res)
}

// newAppRequest is the body for POST /api/databases/new-app: the operator names
// the database and the panel derives the role set and generates the secrets.
type newAppRequest struct {
	Database string `json:"database"`
}

// handleNewApp provisions a one-click app bundle: an owner group role, the
// database owned by it, and read-write + read-only login users. Role names are
// derived from the database; the two login passwords are generated server-side
// and returned exactly once in Secrets.
func (s *Server) handleNewApp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req newAppRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	rwUser := req.Database + "_app"
	roUser := req.Database + "_readonly"
	rwPass := auth.GeneratePassword()
	roPass := auth.GeneratePassword()

	plan := admin.NewAppPlan{
		Database:      req.Database,
		OwnerRole:     req.Database + "_owner",
		ReadwriteUser: rwUser,
		ReadwritePass: rwPass,
		ReadonlyUser:  roUser,
		ReadonlyPass:  roPass,
	}

	res, err := s.pg.AdminNewApp(ctx, plan)
	if err != nil {
		s.audit(ctx, "create_new_app", req.Database, "failure", "provision new app failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "create_new_app", req.Database, "success", "new app provisioned", "")
	writeData(w, http.StatusOK, credentialResult{
		Result: res,
		Secrets: map[string]string{
			rwUser: rwPass,
			roUser: roPass,
		},
	})
}

// dropRequest is the typed-name confirmation body shared by destructive
// database/role drops. The drop foundation methods enforce the confirmation
// against the resource name and refuse panel-managed objects.
type dropRequest struct {
	Confirm string `json:"confirm"`
}

// handleDropDatabase drops a database after a typed-name confirmation.
func (s *Server) handleDropDatabase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := chi.URLParam(r, "name")

	var req dropRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	res, err := s.pg.AdminDropDatabase(ctx, name, req.Confirm)
	if err != nil {
		s.audit(ctx, "drop_database", name, "failure", "drop database failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "drop_database", name, "success", "database dropped", "")
	writeData(w, http.StatusOK, res)
}

// --- roles ---

// roleResponse mirrors one role for the list view.
type roleResponse struct {
	Name        string   `json:"name"`
	CanLogin    bool     `json:"can_login"`
	IsSuperuser bool     `json:"is_superuser"`
	MemberOf    []string `json:"member_of"`
}

// handleListRoles returns the cluster's roles. Read-only; not audited.
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.pg.ListRoles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]roleResponse, 0, len(roles))
	for _, ro := range roles {
		memberOf := ro.MemberOf
		if memberOf == nil {
			memberOf = []string{}
		}
		out = append(out, roleResponse{
			Name:        ro.Name,
			CanLogin:    ro.CanLogin,
			IsSuperuser: ro.IsSuperuser,
			MemberOf:    memberOf,
		})
	}
	writeData(w, http.StatusOK, out)
}

// createRoleRequest is the body for POST /api/roles. A login role requires a
// generated password; the SPA never transmits an operator-chosen one.
type createRoleRequest struct {
	Username         string `json:"username"`
	CanLogin         bool   `json:"can_login"`
	GeneratePassword bool   `json:"generate_password"`
}

// handleCreateRole creates a login or group role. For a login role the panel
// generates the password server-side and returns it once in Secrets; a login
// role without generate_password is rejected (the panel never accepts a plaintext
// password from the client). A group (NOLOGIN) role produces no secret.
func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req createRoleRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	if req.CanLogin && !req.GeneratePassword {
		writeError(w, core.ValidationError("a generated password is required for a login role").
			WithHint("enable generate_password"))
		return
	}

	var (
		res     core.Result
		err     error
		secrets map[string]string
	)
	if req.CanLogin {
		pw := auth.GeneratePassword()
		res, err = s.pg.AdminCreateRole(ctx, req.Username, pw, true)
		if err == nil {
			secrets = map[string]string{req.Username: pw}
		}
	} else {
		res, err = s.pg.AdminCreateRole(ctx, req.Username, "", false)
	}
	if err != nil {
		s.audit(ctx, "create_role", req.Username, "failure", "create role failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "create_role", req.Username, "success", "role created", "")
	writeData(w, http.StatusOK, credentialResult{Result: res, Secrets: secrets})
}

// createReadonlyUserRequest is the body for POST /api/roles/readonly.
type createReadonlyUserRequest struct {
	Username string `json:"username"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
}

// handleCreateReadonlyUser creates a read-only login user on a database/schema
// (defaulting to the public schema), including default privileges for future
// objects. The generated password is returned once in Secrets.
func (s *Server) handleCreateReadonlyUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req createReadonlyUserRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	schema := req.Schema
	if schema == "" {
		schema = "public"
	}
	pw := auth.GeneratePassword()

	res, err := s.pg.AdminCreateReadonlyUser(ctx, req.Username, pw, req.Database, schema)
	if err != nil {
		s.audit(ctx, "create_readonly_user", req.Username, "failure", "create read-only user failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "create_readonly_user", req.Username, "success", "read-only user created", "")
	writeData(w, http.StatusOK, credentialResult{
		Result:  res,
		Secrets: map[string]string{req.Username: pw},
	})
}

// handleRotatePassword sets a fresh generated password on a role and returns it
// once in Secrets. The role is taken from the path; no body is read.
func (s *Server) handleRotatePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := chi.URLParam(r, "role")

	pw := auth.GeneratePassword()
	res, err := s.pg.AdminRotatePassword(ctx, role, pw)
	if err != nil {
		s.audit(ctx, "rotate_password", role, "failure", "rotate password failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "rotate_password", role, "success", "password rotated", "")
	writeData(w, http.StatusOK, credentialResult{
		Result:  res,
		Secrets: map[string]string{role: pw},
	})
}

// handleDropRole drops a role after a typed-name confirmation.
func (s *Server) handleDropRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := chi.URLParam(r, "role")

	var req dropRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	res, err := s.pg.AdminDropRole(ctx, role, req.Confirm)
	if err != nil {
		s.audit(ctx, "drop_role", role, "failure", "drop role failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "drop_role", role, "success", "role dropped", "")
	writeData(w, http.StatusOK, res)
}

// --- grants ---

// grantRequest is the body shared by grant (POST) and revoke (DELETE). Schema
// defaults to public when omitted.
type grantRequest struct {
	Level    string `json:"level"`
	Role     string `json:"role"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
}

// handleGrant applies an access grant (readonly/readwrite/owner) to a role on a
// database/schema.
func (s *Server) handleGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req grantRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	lvl, err := admin.ParseAccessLevel(req.Level)
	if err != nil {
		writeError(w, err)
		return
	}
	schema := req.Schema
	if schema == "" {
		schema = "public"
	}

	res, err := s.pg.AdminGrant(ctx, lvl, req.Role, req.Database, schema)
	if err != nil {
		s.audit(ctx, "grant", req.Role, "failure", "grant access failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "grant", req.Role, "success", "access granted", "")
	writeData(w, http.StatusOK, res)
}

// handleRevoke removes an access grant, mirroring handleGrant.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req grantRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	lvl, err := admin.ParseAccessLevel(req.Level)
	if err != nil {
		writeError(w, err)
		return
	}
	schema := req.Schema
	if schema == "" {
		schema = "public"
	}

	res, err := s.pg.AdminRevoke(ctx, lvl, req.Role, req.Database, schema)
	if err != nil {
		s.audit(ctx, "revoke", req.Role, "failure", "revoke access failed", core.CodeOf(err))
		writeError(w, err)
		return
	}
	s.audit(ctx, "revoke", req.Role, "success", "access revoked", "")
	writeData(w, http.StatusOK, res)
}
