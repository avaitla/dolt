// Copyright 2022 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dprocedures

import (
	"errors"
	"fmt"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/vt/proto/query"

	"github.com/dolthub/dolt/go/cmd/dolt/cli"
	"github.com/dolthub/dolt/go/libraries/doltcore/branch_control"
	"github.com/dolthub/dolt/go/libraries/doltcore/env/actions"
	"github.com/dolthub/dolt/go/libraries/doltcore/sqle/dsess"
)

var hashType = types.MustCreateString(query.Type_TEXT, 32, sql.Collation_ascii_bin)

// doltCommit is the stored procedure version for the CLI command `dolt commit`.
func doltCommit(ctx *sql.Context, args ...string) (sql.RowIter, error) {
	commitHash, skipped, err := doDoltCommit(ctx, args)
	if err != nil {
		return nil, err
	}
	if skipped {
		return nil, nil
	}
	return rowToIter(commitHash), nil
}

// doltCommitHashOut is the stored procedure version for the CLI function `commit`. The first parameter is the variable
// to set the hash of.
func doltCommitHashOut(ctx *sql.Context, outHash *string, args ...string) (sql.RowIter, error) {
	commitHash, skipped, err := doDoltCommit(ctx, args)
	if err != nil {
		return nil, err
	}
	if skipped {
		return nil, nil
	}

	*outHash = commitHash
	return rowToIter(commitHash), nil
}

// doDoltCommit creates a dolt commit using the specified command line |args| provided. The response is the commit hash
// of the new commit (or the empty string if the commit was skipped), a boolean that indicates if creating the commit
// was skipped (e.g. due to --skip-empty), and an error describing any error encountered.
func doDoltCommit(ctx *sql.Context, args []string) (string, bool, error) {
	if err := branch_control.CheckAccess(ctx, branch_control.Permissions_Write); err != nil {
		return "", false, err
	}
	// Get the information for the sql context.
	dbName := ctx.GetCurrentDatabase()

	apr, err := cli.CreateCommitArgParser().Parse(args)
	if err != nil {
		return "", false, err
	}

	if err := cli.VerifyCommitArgs(apr); err != nil {
		return "", false, err
	}

	dSess := dsess.DSessFromSess(ctx.Session)
	roots, ok := dSess.GetRoots(ctx, dbName)
	if !ok {
		return "", false, fmt.Errorf("Could not load database %s", dbName)
	}

	if apr.Contains(cli.UpperCaseAllFlag) {
		roots, err = actions.StageAllTables(ctx, roots, true)
		if err != nil {
			return "", false, fmt.Errorf(err.Error())
		}
	} else if apr.Contains(cli.AllFlag) {
		roots, err = actions.StageModifiedAndDeletedTables(ctx, roots)
		if err != nil {
			return "", false, fmt.Errorf(err.Error())
		}
	}

	var name, email string
	if authorStr, ok := apr.GetValue(cli.AuthorParam); ok {
		name, email, err = cli.ParseAuthor(authorStr)
		if err != nil {
			return "", false, err
		}
	} else {
		// In SQL mode, use the current SQL user as the commit author, instead of the `dolt config` configured values.
		// We won't have an email address for the SQL user though, so instead use the MySQL user@address notation.
		name = ctx.Client().User
		email = fmt.Sprintf("%s@%s", ctx.Client().User, ctx.Client().Address)
	}

	amend := apr.Contains(cli.AmendFlag)

	msg, msgOk := apr.GetValue(cli.MessageArg)
	if !msgOk {
		if amend {
			commit, err := dSess.GetHeadCommit(ctx, dbName)
			if err != nil {
				return "", false, err
			}
			commitMeta, err := commit.GetCommitMeta(ctx)
			if err != nil {
				return "", false, err
			}
			msg = commitMeta.Description
		} else {
			return "", false, fmt.Errorf("Must provide commit message.")
		}
	}

	t := ctx.QueryTime()
	if commitTimeStr, ok := apr.GetValue(cli.DateParam); ok {
		var err error
		t, err = cli.ParseDate(commitTimeStr)

		if err != nil {
			return "", false, fmt.Errorf(err.Error())
		}
	}

	pendingCommit, err := dSess.NewPendingCommit(ctx, dbName, roots, actions.CommitStagedProps{
		Message:    msg,
		Date:       t,
		AllowEmpty: apr.Contains(cli.AllowEmptyFlag),
		SkipEmpty:  apr.Contains(cli.SkipEmptyFlag),
		Amend:      amend,
		Force:      apr.Contains(cli.ForceFlag),
		Name:       name,
		Email:      email,
	})
	if err != nil {
		return "", false, err
	}

	// Nothing to commit, and we didn't pass --allowEmpty
	if pendingCommit == nil && apr.Contains(cli.SkipEmptyFlag) {
		return "", true, nil
	} else if pendingCommit == nil {
		return "", false, errors.New("nothing to commit")
	}

	newCommit, err := dSess.DoltCommit(ctx, dbName, dSess.GetTransaction(), pendingCommit)
	if err != nil {
		return "", false, err
	}

	h, err := newCommit.HashOf()
	if err != nil {
		return "", false, err
	}

	return h.String(), false, nil
}

func getDoltArgs(ctx *sql.Context, row sql.Row, children []sql.Expression) ([]string, error) {
	args := make([]string, len(children))
	for i := range children {
		childVal, err := children[i].Eval(ctx, row)

		if err != nil {
			return nil, err
		}

		text, _, err := types.Text.Convert(childVal)

		if err != nil {
			return nil, err
		}

		args[i] = text.(string)
	}

	return args, nil
}
