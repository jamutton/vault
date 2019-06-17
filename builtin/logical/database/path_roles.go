package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/vault/sdk/database/dbplugin"
	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/helper/locksutil"
	"github.com/hashicorp/vault/sdk/helper/strutil"
	"github.com/hashicorp/vault/sdk/logical"
	"github.com/hashicorp/vault/sdk/queue"
)

func pathListRoles(b *databaseBackend) []*framework.Path {
	return []*framework.Path{
		&framework.Path{
			Pattern: "roles/?$",

			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.ListOperation: b.pathRoleList,
			},

			HelpSynopsis:    pathRoleHelpSyn,
			HelpDescription: pathRoleHelpDesc,
		},
		&framework.Path{
			Pattern: "static-roles/?$",

			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.ListOperation: b.pathRoleList,
			},

			HelpSynopsis:    pathStaticRoleHelpSyn,
			HelpDescription: pathStaticRoleHelpDesc,
		},
	}
}

func pathRoles(b *databaseBackend) []*framework.Path {
	return []*framework.Path{
		&framework.Path{
			Pattern:        "roles/" + framework.GenericNameRegex("name"),
			Fields:         fieldsForType(databaseRolePath),
			ExistenceCheck: b.pathRoleExistenceCheck,
			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.ReadOperation:   b.pathRoleRead,
				logical.CreateOperation: b.pathRoleCreateUpdate,
				logical.UpdateOperation: b.pathRoleCreateUpdate,
				logical.DeleteOperation: b.pathRoleDelete,
			},

			HelpSynopsis:    pathRoleHelpSyn,
			HelpDescription: pathRoleHelpDesc,
		},

		&framework.Path{
			Pattern:        "static-roles/" + framework.GenericNameRegex("name"),
			Fields:         fieldsForType(databaseStaticRolePath),
			ExistenceCheck: b.pathStaticRoleExistenceCheck,
			Callbacks: map[logical.Operation]framework.OperationFunc{
				logical.ReadOperation:   b.pathStaticRoleRead,
				logical.CreateOperation: b.pathStaticRoleCreateUpdate,
				logical.UpdateOperation: b.pathStaticRoleCreateUpdate,
				logical.DeleteOperation: b.pathStaticRoleDelete,
			},

			HelpSynopsis:    pathStaticRoleHelpSyn,
			HelpDescription: pathStaticRoleHelpDesc,
		},
	}
}

// fieldsForType returns a map of string/FieldSchema items for the given role
// type. The purpose is to keep the shared fields between dynamic and static
// roles consistent, and allow for each type to override or provide their own
// specific fields
func fieldsForType(roleType string) map[string]*framework.FieldSchema {
	fields := map[string]*framework.FieldSchema{
		"name": {
			Type:        framework.TypeString,
			Description: "Name of the role.",
		},
		"db_name": {
			Type:        framework.TypeString,
			Description: "Name of the database this role acts on.",
		},
		"creation_statements": {
			Type: framework.TypeStringSlice,
			Description: `Specifies the database statements executed to
	create and configure a user. See the plugin's API page for more
	information on support and formatting for this parameter.`,
		},
		"revocation_statements": {
			Type: framework.TypeStringSlice,
			Description: `Specifies the database statements to be executed
	to revoke a user. See the plugin's API page for more information
	on support and formatting for this parameter.`,
		},
		"renew_statements": {
			Type: framework.TypeStringSlice,
			Description: `Specifies the database statements to be executed
	to renew a user. Not every plugin type will support this
	functionality. See the plugin's API page for more information on
	support and formatting for this parameter. `,
		},
		"rollback_statements": {
			Type: framework.TypeStringSlice,
			Description: `Specifies the database statements to be executed
	rollback a create operation in the event of an error. Not every plugin
	type will support this functionality. See the plugin's API page for
	more information on support and formatting for this parameter.`,
		},
	}

	// Get the fields that are specific to the type of role, and add them to the
	// common fields
	var typeFields map[string]*framework.FieldSchema
	switch roleType {
	case databaseStaticRolePath:
		typeFields = staticFields()
	default:
		typeFields = dynamicFields()
	}

	for k, v := range typeFields {
		fields[k] = v
	}

	return fields
}

// dynamicFields returns a map of key and field schema items that are specific
// only to dynamic roles
func dynamicFields() map[string]*framework.FieldSchema {
	fields := map[string]*framework.FieldSchema{
		"default_ttl": {
			Type:        framework.TypeDurationSecond,
			Description: "Default ttl for role.",
		},

		"max_ttl": {
			Type:        framework.TypeDurationSecond,
			Description: "Maximum time a credential is valid for",
		},
	}
	return fields
}

// staticFields returns a map of key and field schema items that are specific
// only to static roles
func staticFields() map[string]*framework.FieldSchema {
	fields := map[string]*framework.FieldSchema{
		"username": {
			Type: framework.TypeString,
			Description: `Name of the static user account for Vault to manage.
	Requires "rotation_period" to be specified`,
		},
		"rotation_period": {
			Type: framework.TypeDurationSecond,
			Description: `Period for automatic
	credential rotation of the given username. Not valid unless used with
	"username".`,
		},
		"rotation_statements": {
			Type: framework.TypeStringSlice,
			Description: `Specifies the database statements to be executed to
	rotate the accounts credentials. Not every plugin type will support
	this functionality. See the plugin's API page for more information on
	support and formatting for this parameter.`,
		},
		"revoke_user_on_delete": {
			Type:    framework.TypeBool,
			Default: false,
			Description: `Revoke the database user identified by the username when
	this Role is deleted. Revocation will use the configured
	revocation_statements if provided. Default false.`,
		},
	}
	return fields
}

func (b *databaseBackend) pathRoleExistenceCheck(ctx context.Context, req *logical.Request, data *framework.FieldData) (bool, error) {
	role, err := b.Role(ctx, req.Storage, data.Get("name").(string))
	if err != nil {
		return false, err
	}
	return role != nil, nil
}

func (b *databaseBackend) pathStaticRoleExistenceCheck(ctx context.Context, req *logical.Request, data *framework.FieldData) (bool, error) {
	role, err := b.StaticRole(ctx, req.Storage, data.Get("name").(string))
	if err != nil {
		return false, err
	}
	return role != nil, nil
}

func (b *databaseBackend) pathRoleDelete(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	err := req.Storage.Delete(ctx, databaseRolePath+data.Get("name").(string))
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (b *databaseBackend) pathStaticRoleDelete(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	name := data.Get("name").(string)

	// Grab the exclusive lock
	lock := locksutil.LockForKey(b.roleLocks, name)
	lock.Lock()
	defer lock.Unlock()

	// Remove the item from the queue
	_, _ = b.popFromRotationQueueByKey(name)

	// If this role is a static account, we need to revoke the user from the
	// database
	role, err := b.StaticRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, nil
	}

	// Clean up the static useraccount, if it exists
	revoke := role.StaticAccount.RevokeUserOnDelete
	if revoke {
		db, err := b.GetConnection(ctx, req.Storage, role.DBName)
		if err != nil {
			return nil, err
		}

		db.RLock()
		defer db.RUnlock()

		if err := db.RevokeUser(ctx, role.Statements, role.StaticAccount.Username); err != nil {
			b.CloseIfShutdown(db, err)
			return nil, err
		}
	}

	err = req.Storage.Delete(ctx, databaseStaticRolePath+name)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (b *databaseBackend) pathStaticRoleRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	role, err := b.StaticRole(ctx, req.Storage, d.Get("name").(string))
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, nil
	}
	data := pathRoleReadCommon(role)
	if role.StaticAccount != nil {
		data["username"] = role.StaticAccount.Username
		data["rotation_period"] = role.StaticAccount.RotationPeriod.Seconds()
		if !role.StaticAccount.LastVaultRotation.IsZero() {
			data["last_vault_rotation"] = role.StaticAccount.LastVaultRotation
		}
		data["revoke_user_on_delete"] = role.StaticAccount.RevokeUserOnDelete
	}

	return &logical.Response{
		Data: data,
	}, nil
}

func (b *databaseBackend) pathRoleRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	role, err := b.Role(ctx, req.Storage, d.Get("name").(string))
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, nil
	}

	return &logical.Response{
		Data: pathRoleReadCommon(role),
	}, nil
}

func pathRoleReadCommon(role *roleEntry) map[string]interface{} {
	data := map[string]interface{}{
		"db_name":               role.DBName,
		"creation_statements":   role.Statements.Creation,
		"revocation_statements": role.Statements.Revocation,
		"rollback_statements":   role.Statements.Rollback,
		"renew_statements":      role.Statements.Renewal,
		"rotation_statements":   role.Statements.Rotation,
		"default_ttl":           role.DefaultTTL.Seconds(),
		"max_ttl":               role.MaxTTL.Seconds(),
	}
	if len(role.Statements.Creation) == 0 {
		data["creation_statements"] = []string{}
	}
	if len(role.Statements.Revocation) == 0 {
		data["revocation_statements"] = []string{}
	}
	if len(role.Statements.Rollback) == 0 {
		data["rollback_statements"] = []string{}
	}
	if len(role.Statements.Renewal) == 0 {
		data["renew_statements"] = []string{}
	}
	if len(role.Statements.Rotation) == 0 {
		data["rotation_statements"] = []string{}
	}
	return data
}

func (b *databaseBackend) pathRoleList(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	path := databaseRolePath
	if strings.HasPrefix(req.Path, "static-roles") {
		path = databaseStaticRolePath
	}
	entries, err := req.Storage.List(ctx, path)
	if err != nil {
		return nil, err
	}

	return logical.ListResponse(entries), nil
}

func (b *databaseBackend) pathRoleCreateUpdate(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	name := data.Get("name").(string)
	if name == "" {
		return logical.ErrorResponse("empty role name attribute given"), nil
	}

	exists, err := b.pathStaticRoleExistenceCheck(ctx, req, data)
	if err != nil {
		return nil, err
	}
	if exists {
		return logical.ErrorResponse("Role and Static Role names must be unique"), nil
	}

	role, err := b.Role(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if role == nil {
		role = &roleEntry{}
	}

	if err := pathRoleCreateUpdateCommon(ctx, role, req.Operation, data); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	// TTLs
	{
		if defaultTTLRaw, ok := data.GetOk("default_ttl"); ok {
			role.DefaultTTL = time.Duration(defaultTTLRaw.(int)) * time.Second
		} else if req.Operation == logical.CreateOperation {
			role.DefaultTTL = time.Duration(data.Get("default_ttl").(int)) * time.Second
		}
		if maxTTLRaw, ok := data.GetOk("max_ttl"); ok {
			role.MaxTTL = time.Duration(maxTTLRaw.(int)) * time.Second
		} else if req.Operation == logical.CreateOperation {
			role.MaxTTL = time.Duration(data.Get("max_ttl").(int)) * time.Second
		}
	}

	// Store it
	entry, err := logical.StorageEntryJSON(databaseRolePath+name, role)
	if err != nil {
		return nil, err
	}

	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, err
	}

	return nil, nil
}

func (b *databaseBackend) pathStaticRoleCreateUpdate(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	name := data.Get("name").(string)
	if name == "" {
		return logical.ErrorResponse("empty role name attribute given"), nil
	}

	// Grab the exclusive lock as well potentially pop and re-push the queue item
	// for this role
	lock := locksutil.LockForKey(b.roleLocks, name)
	lock.Lock()
	defer lock.Unlock()

	exists, err := b.pathRoleExistenceCheck(ctx, req, data)
	if err != nil {
		return nil, err
	}
	if exists {
		return logical.ErrorResponse("Role and Static Role names must be unique"), nil
	}

	role, err := b.StaticRole(ctx, req.Storage, data.Get("name").(string))
	if err != nil {
		return nil, err
	}

	// createRole is a boolean to indicate if this is a new role creation. This is
	// can be used later by database plugins that distinguish between creating and
	// updating roles, and may use seperate statements depending on the context.
	createRole := req.Operation == logical.CreateOperation
	if role == nil {
		role = &roleEntry{
			StaticAccount: &staticAccount{},
		}
		createRole = true
	}

	if err := pathRoleCreateUpdateCommon(ctx, role, req.Operation, data); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	username := data.Get("username").(string)
	if username == "" && createRole {
		return logical.ErrorResponse("username is a required field to create a static account"), nil
	}

	if role.StaticAccount.Username != "" && role.StaticAccount.Username != username {
		return logical.ErrorResponse("cannot update static account username"), nil
	}
	role.StaticAccount.Username = username

	// If it's a Create operation, both username and rotation_period must be included
	rotationPeriodSecondsRaw, ok := data.GetOk("rotation_period")
	if !ok && createRole {
		return logical.ErrorResponse("rotation_period is required to create static accounts"), nil
	}
	if ok {
		rotationPeriodSeconds := rotationPeriodSecondsRaw.(int)
		if rotationPeriodSeconds < queueTickSeconds {
			// If rotation frequency is specified, and this is an update, the value
			// must be at least that of the constant queueTickSeconds (5 seconds at
			// time of writing), otherwise we wont be able to rotate in time
			return logical.ErrorResponse(fmt.Sprintf("rotation_period must be %d seconds or more", queueTickSeconds)), nil
		}
		role.StaticAccount.RotationPeriod = time.Duration(rotationPeriodSeconds) * time.Second
	}

	if rotationStmtsRaw, ok := data.GetOk("rotation_statements"); ok {
		role.Statements.Rotation = rotationStmtsRaw.([]string)
	} else if req.Operation == logical.CreateOperation {
		role.Statements.Rotation = data.Get("rotation_statements").([]string)
	}

	role.StaticAccount.RevokeUserOnDelete = data.Get("revoke_user_on_delete").(bool)

	// lvr represents the roles' LastVaultRotation
	lvr := role.StaticAccount.LastVaultRotation

	// Only call setStaticAccount if we're creating the role for the
	// first time
	switch req.Operation {
	case logical.CreateOperation:
		// setStaticAccount calls Storage.Put and saves the role to storage
		resp, err := b.setStaticAccount(ctx, req.Storage, &setStaticAccountInput{
			RoleName:   name,
			Role:       role,
			CreateUser: createRole,
		})
		if err != nil {
			return nil, err
		}
		// guard against RotationTime not being set or zero-value
		lvr = resp.RotationTime
	case logical.UpdateOperation:
		// store updated Role
		entry, err := logical.StorageEntryJSON(databaseStaticRolePath+name, role)
		if err != nil {
			return nil, err
		}
		if err := req.Storage.Put(ctx, entry); err != nil {
			return nil, err
		}

		// In case this is an update, remove any previous version of the item from
		// the queue
		b.popFromRotationQueueByKey(name)
	}

	// Add their rotation to the queue
	if err := b.pushItem(&queue.Item{
		Key:      name,
		Priority: lvr.Add(role.StaticAccount.RotationPeriod).Unix(),
	}); err != nil {
		return nil, err
	}

	return nil, nil
}

func pathRoleCreateUpdateCommon(ctx context.Context, role *roleEntry, operation logical.Operation, data *framework.FieldData) error {
	// DB Attributes
	{
		if dbNameRaw, ok := data.GetOk("db_name"); ok {
			role.DBName = dbNameRaw.(string)
		} else if operation == logical.CreateOperation {
			role.DBName = data.Get("db_name").(string)
		}
		if role.DBName == "" {
			return errors.New("empty database name attribute")
		}
	}

	// Statements
	{
		if creationStmtsRaw, ok := data.GetOk("creation_statements"); ok {
			role.Statements.Creation = creationStmtsRaw.([]string)
		} else if operation == logical.CreateOperation {
			role.Statements.Creation = data.Get("creation_statements").([]string)
		}

		if revocationStmtsRaw, ok := data.GetOk("revocation_statements"); ok {
			role.Statements.Revocation = revocationStmtsRaw.([]string)
		} else if operation == logical.CreateOperation {
			role.Statements.Revocation = data.Get("revocation_statements").([]string)
		}

		if rollbackStmtsRaw, ok := data.GetOk("rollback_statements"); ok {
			role.Statements.Rollback = rollbackStmtsRaw.([]string)
		} else if operation == logical.CreateOperation {
			role.Statements.Rollback = data.Get("rollback_statements").([]string)
		}

		if renewStmtsRaw, ok := data.GetOk("renew_statements"); ok {
			role.Statements.Renewal = renewStmtsRaw.([]string)
		} else if operation == logical.CreateOperation {
			role.Statements.Renewal = data.Get("renew_statements").([]string)
		}

		// Do not persist deprecated statements that are populated on role read
		role.Statements.CreationStatements = ""
		role.Statements.RevocationStatements = ""
		role.Statements.RenewStatements = ""
		role.Statements.RollbackStatements = ""
	}

	role.Statements.Revocation = strutil.RemoveEmpty(role.Statements.Revocation)

	return nil
}

type roleEntry struct {
	DBName        string              `json:"db_name"`
	Statements    dbplugin.Statements `json:"statements"`
	DefaultTTL    time.Duration       `json:"default_ttl"`
	MaxTTL        time.Duration       `json:"max_ttl"`
	StaticAccount *staticAccount      `json:"static_account" mapstructure:"static_account"`
}

type staticAccount struct {
	// Username to create or assume management for static accounts
	Username string `json:"username"`

	// Password is the current password for static accounts. As an input, this is
	// used/required when trying to assume management of an existing static
	// account. Return this on credential request if it exists.
	Password string `json:"password"`

	// LastVaultRotation represents the last time Vault rotated the password
	LastVaultRotation time.Time `json:"last_vault_rotation"`

	// RotationPeriod is number in seconds between each rotation, effectively a
	// "time to live". This value is compared to the LastVaultRotation to
	// determine if a password needs to be rotated
	RotationPeriod time.Duration `json:"rotation_period"`

	// RevokeUser is a boolean flag to indicate if Vault should revoke the
	// database user when the role is deleted
	RevokeUserOnDelete bool `json:"revoke_user_on_delete"`
}

// NextRotationTime calculates the next rotation by adding the Rotation Period
// to the last known vault rotation
func (s *staticAccount) NextRotationTime() time.Time {
	return s.LastVaultRotation.Add(s.RotationPeriod)
}

// PasswordTTL calculates the approximate time remaining until the password is
// no longer valid. This is approximate because the periodic rotation is only
// checked approximately every 5 seconds, and each rotation can take a small
// amount of time to process. This can result in a negative TTL time while the
// rotation function processes the Static Role and performs the rotation. If the
// TTL is negative, zero is returned. Users should not trust passwords with a
// Zero TTL, as they are likely in the process of being rotated and will quickly
// be invalidated.
func (s *staticAccount) PasswordTTL() time.Duration {
	next := s.NextRotationTime()
	ttl := next.Sub(time.Now()).Round(time.Second)
	if ttl < 0 {
		ttl = time.Duration(0)
	}
	return ttl
}

const pathRoleHelpSyn = `
Manage the roles that can be created with this backend.
`

const pathStaticRoleHelpSyn = `
Manage the static roles that can be created with this backend.
`

const pathRoleHelpDesc = `
This path lets you manage the roles that can be created with this backend.

The "db_name" parameter is required and configures the name of the database
connection to use.

The "creation_statements" parameter customizes the string used to create the
credentials. This can be a sequence of SQL queries, or other statement formats
for a particular database type. Some substitution will be done to the statement
strings for certain keys. The names of the variables must be surrounded by "{{"
and "}}" to be replaced.

  * "name" - The random username generated for the DB user.

  * "password" - The random password generated for the DB user.

  * "expiration" - The timestamp when this user will expire.

Example of a decent creation_statements for a postgresql database plugin:

	CREATE ROLE "{{name}}" WITH
	  LOGIN
	  PASSWORD '{{password}}'
	  VALID UNTIL '{{expiration}}';
	GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO "{{name}}";

The "revocation_statements" parameter customizes the statement string used to
revoke a user. Example of a decent revocation_statements for a postgresql
database plugin:

	REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM {{name}};
	REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM {{name}};
	REVOKE USAGE ON SCHEMA public FROM {{name}};
	DROP ROLE IF EXISTS {{name}};

The "renew_statements" parameter customizes the statement string used to renew a
user.
The "rollback_statements' parameter customizes the statement string used to
rollback a change if needed.
`

const pathStaticRoleHelpDesc = `
This path lets you manage the static roles that can be created with this
backend. Static Roles are associated with a single database user, and manage the
password based on a rotation period, automatically rotating the password.

The "db_name" parameter is required and configures the name of the database
connection to use.

The "creation_statements" parameter customizes the string used to create the
credentials. This can be a sequence of SQL queries, or other statement formats
for a particular database type. Some substitution will be done to the statement
strings for certain keys. The names of the variables must be surrounded by "{{"
and "}}" to be replaced.

  * "name" - The random username generated for the DB user.

  * "password" - The random password generated for the DB user.

Example of a decent creation_statements for a postgresql database plugin:

        CREATE ROLE "{{name}}" WITH
          LOGIN
          PASSWORD '{{password}}'
        GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO "{{name}}";

The "revocation_statements" parameter customizes the statement string used to
revoke a user. Example of a decent revocation_statements for a postgresql
database plugin:

        REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM {{name}};
        REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM {{name}};
        REVOKE USAGE ON SCHEMA public FROM {{name}};
        DROP ROLE IF EXISTS {{name}};

The "renew_statements" parameter customizes the statement string used to renew a
user.
The "rollback_statements' parameter customizes the statement string used to
rollback a change if needed.
`
